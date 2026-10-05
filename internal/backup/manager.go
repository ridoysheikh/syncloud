package backup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"syncloud/internal/events"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
)

const (
	settingConfig = "backup.config"
	settingSecret = "backup.secret"
	settingStatus = "backup.status"

	// TopicUpdated carries Status after every backup run.
	TopicUpdated = "backup.updated"
)

// Config is where and how often backups go. The secret is stored sealed.
type Config struct {
	Endpoint        string `json:"endpoint"` // e.g. https://s3.eu-central-1.amazonaws.com, http://10.0.0.5:9000
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`
	IntervalMinutes int    `json:"intervalMinutes"`
	Retain          int    `json:"retain"`
}

type Status struct {
	LastRunAt     *time.Time `json:"lastRunAt"`
	LastSuccessAt *time.Time `json:"lastSuccessAt"`
	LastError     string     `json:"lastError"`
	LastObject    string     `json:"lastObject"`
	LastSize      int64      `json:"lastSize"`
}

type Object struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"createdAt"`
}

type Manager struct {
	st      *store.Store
	box     *secrets.Box
	dataDir string
	bus     *events.Bus
	log     *slog.Logger
	now     func() time.Time

	mu     sync.Mutex
	cfg    *Config
	status Status
	run    sync.Mutex // one backup at a time
	kick   chan struct{}
}

func NewManager(st *store.Store, box *secrets.Box, dataDir string, bus *events.Bus, log *slog.Logger) *Manager {
	return &Manager{st: st, box: box, dataDir: dataDir, bus: bus, log: log, now: time.Now, kick: make(chan struct{}, 1)}
}

// Load reads the stored configuration and last status.
func (m *Manager) Load(ctx context.Context) error {
	raw, ok, err := m.st.GetSetting(ctx, settingConfig)
	if err != nil {
		return err
	}
	if ok {
		var c Config
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return err
		}
		sec, _, err := m.st.GetSetting(ctx, settingSecret)
		if err != nil {
			return err
		}
		sealed, err := base64.StdEncoding.DecodeString(sec)
		if err != nil {
			return err
		}
		plain, err := m.box.Open(sealed, []byte(settingSecret))
		if err != nil {
			return fmt.Errorf("backup secret: %w", err)
		}
		c.SecretAccessKey = string(plain)
		m.cfg = &c
	}
	if raw, ok, _ := m.st.GetSetting(ctx, settingStatus); ok {
		_ = json.Unmarshal([]byte(raw), &m.status)
	}
	return nil
}

// Config returns the configuration without the secret.
func (m *Manager) Config() (Config, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg == nil {
		return Config{}, false
	}
	c := *m.cfg
	c.SecretAccessKey = ""
	return c, true
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Validate checks and normalizes c; an empty secret keeps the stored one.
func (m *Manager) normalize(c Config) (Config, error) {
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	c.Bucket = strings.TrimSpace(c.Bucket)
	c.Prefix = strings.Trim(strings.TrimSpace(c.Prefix), "/")
	c.AccessKeyID = strings.TrimSpace(c.AccessKeyID)
	if u, err := url.Parse(c.Endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return c, errors.New("endpoint must be a URL like https://s3.amazonaws.com or http://10.0.0.5:9000")
	}
	if c.Bucket == "" || c.AccessKeyID == "" {
		return c, errors.New("bucket and access key ID are required")
	}
	if c.SecretAccessKey == "" {
		m.mu.Lock()
		if m.cfg != nil {
			c.SecretAccessKey = m.cfg.SecretAccessKey
		}
		m.mu.Unlock()
		if c.SecretAccessKey == "" {
			return c, errors.New("secret access key is required")
		}
	}
	if c.IntervalMinutes == 0 {
		c.IntervalMinutes = 60
	}
	if c.Retain == 0 {
		c.Retain = 48
	}
	if c.IntervalMinutes < 5 || c.IntervalMinutes > 7*24*60 {
		return c, errors.New("interval must be between 5 minutes and 7 days")
	}
	if c.Retain < 1 || c.Retain > 1000 {
		return c, errors.New("retain must be between 1 and 1000")
	}
	return c, nil
}

// SetConfig validates c, checks that the bucket is reachable, stores it and
// triggers a first backup.
func (m *Manager) SetConfig(ctx context.Context, c Config) error {
	c, err := m.normalize(c)
	if err != nil {
		return err
	}
	cl, err := s3Client(c)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	exists, err := cl.BucketExists(cctx, c.Bucket)
	if err != nil {
		return fmt.Errorf("cannot reach the bucket: %w", err)
	}
	if !exists {
		return fmt.Errorf("bucket %q does not exist", c.Bucket)
	}
	pub := c
	pub.SecretAccessKey = ""
	raw, _ := json.Marshal(pub)
	if err := m.st.SetSetting(ctx, settingConfig, string(raw)); err != nil {
		return err
	}
	sealed := m.box.Seal([]byte(c.SecretAccessKey), []byte(settingSecret))
	if err := m.st.SetSetting(ctx, settingSecret, base64.StdEncoding.EncodeToString(sealed)); err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg = &c
	m.mu.Unlock()
	select {
	case m.kick <- struct{}{}:
	default:
	}
	return nil
}

// Disable turns backups off (existing objects are kept).
func (m *Manager) Disable(ctx context.Context) error {
	for _, k := range []string{settingConfig, settingSecret} {
		if err := m.st.DeleteSetting(ctx, k); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.cfg = nil
	m.mu.Unlock()
	return nil
}

// Run takes a backup whenever one is due, until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.kick:
		}
		m.mu.Lock()
		cfg, last := m.cfg, m.status.LastRunAt
		m.mu.Unlock()
		if cfg == nil {
			continue
		}
		if last != nil && m.now().Sub(*last) < time.Duration(cfg.IntervalMinutes)*time.Minute {
			continue
		}
		if _, err := m.RunNow(ctx); err != nil {
			m.log.Warn("backup failed", "err", err)
		}
	}
}

// ErrNotConfigured is returned when no S3 destination is set.
var ErrNotConfigured = errors.New("backups are not configured")

// RunNow uploads a backup and applies retention.
func (m *Manager) RunNow(ctx context.Context) (Object, error) {
	m.run.Lock()
	defer m.run.Unlock()
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	if cfg == nil {
		return Object{}, ErrNotConfigured
	}
	start := m.now().UTC()
	obj, err := m.upload(ctx, *cfg, start)
	m.mu.Lock()
	m.status.LastRunAt = &start
	if err != nil {
		m.status.LastError = err.Error()
	} else {
		m.status.LastError = ""
		m.status.LastSuccessAt = &start
		m.status.LastObject = obj.Name
		m.status.LastSize = obj.Size
	}
	st := m.status
	m.mu.Unlock()
	raw, _ := json.Marshal(st)
	if serr := m.st.SetSetting(ctx, settingStatus, string(raw)); serr != nil {
		m.log.Warn("save backup status", "err", serr)
	}
	if m.bus != nil {
		m.bus.Publish(TopicUpdated, st)
	}
	if err == nil {
		m.log.Info("backup uploaded", "object", obj.Name, "bytes", obj.Size)
	}
	return obj, err
}

func (m *Manager) upload(ctx context.Context, cfg Config, at time.Time) (Object, error) {
	var buf bytes.Buffer
	if err := Create(ctx, m.st, m.box, m.dataDir, &buf); err != nil {
		return Object{}, err
	}
	cl, err := s3Client(cfg)
	if err != nil {
		return Object{}, err
	}
	name := "syncloud-" + at.Format("20060102T150405Z") + Ext
	key := objectKey(cfg, name)
	uctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	size := int64(buf.Len())
	if _, err := cl.PutObject(uctx, cfg.Bucket, key, &buf, size, minio.PutObjectOptions{ContentType: "application/octet-stream"}); err != nil {
		return Object{}, fmt.Errorf("upload: %w", err)
	}
	if err := m.prune(uctx, cl, cfg); err != nil {
		m.log.Warn("backup retention", "err", err)
	}
	return Object{Name: name, Size: size, CreatedAt: at}, nil
}

func objectKey(cfg Config, name string) string {
	if cfg.Prefix == "" {
		return name
	}
	return path.Join(cfg.Prefix, name)
}

func (m *Manager) prune(ctx context.Context, cl *minio.Client, cfg Config) error {
	objs, err := list(ctx, cl, cfg)
	if err != nil {
		return err
	}
	for i := cfg.Retain; i < len(objs); i++ { // newest first
		if err := cl.RemoveObject(ctx, cfg.Bucket, objectKey(cfg, objs[i].Name), minio.RemoveObjectOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// List returns stored backups, newest first.
func (m *Manager) List(ctx context.Context) ([]Object, error) {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	if cfg == nil {
		return nil, ErrNotConfigured
	}
	cl, err := s3Client(*cfg)
	if err != nil {
		return nil, err
	}
	return list(ctx, cl, *cfg)
}

func list(ctx context.Context, cl *minio.Client, cfg Config) ([]Object, error) {
	prefix := ""
	if cfg.Prefix != "" {
		prefix = cfg.Prefix + "/"
	}
	var out []Object
	for o := range cl.ListObjects(ctx, cfg.Bucket, minio.ListObjectsOptions{Prefix: prefix + "syncloud-"}) {
		if o.Err != nil {
			return nil, o.Err
		}
		name := strings.TrimPrefix(o.Key, prefix)
		if strings.Contains(name, "/") || !strings.HasSuffix(name, Ext) {
			continue
		}
		out = append(out, Object{Name: name, Size: o.Size, CreatedAt: o.LastModified.UTC()})
	}
	// Names embed the UTC timestamp, so they sort chronologically.
	slices.SortFunc(out, func(a, b Object) int { return strings.Compare(b.Name, a.Name) })
	return out, nil
}

// Download fetches one backup (by name, or the newest when name is "").
func Download(ctx context.Context, cfg Config, name string) ([]byte, string, error) {
	cl, err := s3Client(cfg)
	if err != nil {
		return nil, "", err
	}
	if name == "" {
		objs, err := list(ctx, cl, cfg)
		if err != nil {
			return nil, "", err
		}
		if len(objs) == 0 {
			return nil, "", errors.New("no backups found")
		}
		name = objs[0].Name
	}
	o, err := cl.GetObject(ctx, cfg.Bucket, objectKey(cfg, name), minio.GetObjectOptions{})
	if err != nil {
		return nil, "", err
	}
	defer o.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(o); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), name, nil
}

func s3Client(c Config) (*minio.Client, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, err
	}
	return minio.New(u.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(c.AccessKeyID, c.SecretAccessKey, ""),
		Secure: u.Scheme == "https",
		Region: c.Region,
	})
}

// WriteBundle streams a fresh backup bundle (for downloads without S3).
func (m *Manager) WriteBundle(ctx context.Context, w io.Writer) error {
	return Create(ctx, m.st, m.box, m.dataDir, w)
}
