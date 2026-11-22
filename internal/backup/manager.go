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

	"github.com/ridoysheikh/syncloud/internal/events"
	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

const (
	settingConfig = "backup.config"
	settingSecret = "backup.secret"
	settingStatus = "backup.status"

	// TopicUpdated carries Status after every backup run.
	TopicUpdated = "backup.updated"
)

// Config is where and how often backups go. With EndpointID the
// credentials are a registered S3 connection's, read live at every run, so
// rotating its key there applies here too; otherwise they are the ones
// entered with the settings, and the secret is stored sealed.
type Config struct {
	// EndpointID is a registered S3 connection (ID; name accepted on input).
	EndpointID string `json:"endpointId,omitempty"`
	// EndpointName is the connection's name (shown, not stored).
	EndpointName    string `json:"endpointName,omitempty"`
	Endpoint        string `json:"endpoint"` // e.g. https://s3.eu-central-1.amazonaws.com, http://10.0.0.5:9000
	Region          string `json:"region"`
	PathStyle       bool   `json:"pathStyle,omitempty"`
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

// Creds are a registered S3 connection's live credentials.
type Creds struct {
	ID, Name, URL, Region, AccessKeyID, SecretAccessKey string
	PathStyle                                           bool
}

type Manager struct {
	// Connection resolves a registered S3 connection (by ID or name); nil
	// means only entered credentials can be used.
	Connection func(ctx context.Context, ref string) (Creds, error)
	// Connections lists the registered S3 connections, so backups set up
	// with entered keys can move to the matching one (adopt).
	Connections func(ctx context.Context) ([]Creds, error)

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
		// With a connection the credentials are read at every run (live).
		if c.EndpointID == "" {
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
		}
		m.cfg = &c
	}
	if raw, ok, _ := m.st.GetSetting(ctx, settingStatus); ok {
		_ = json.Unmarshal([]byte(raw), &m.status)
	}
	// Entered keys that are a registered connection's: use the connection,
	// so a key rotated there applies to backups too.
	if cr, ok := m.matchConnection(ctx, false); ok {
		if err := m.useConnection(ctx, cr); err != nil {
			m.log.Warn("backups: use the matching S3 connection", "connection", cr.Name, "err", err)
		}
	}
	return nil
}

func sameURL(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/")) }
	return norm(a) == norm(b)
}

// matchConnection finds the registered connection backups set up with
// entered keys should use: the one with the same address and access key
// ID or, once the entered key is refused (rejected), the only one with the
// same address (the key was rotated there).
func (m *Manager) matchConnection(ctx context.Context, rejected bool) (Creds, bool) {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	if cfg == nil || cfg.EndpointID != "" || m.Connections == nil || m.Connection == nil {
		return Creds{}, false
	}
	conns, err := m.Connections(ctx)
	if err != nil {
		return Creds{}, false
	}
	var same []Creds
	for _, c := range conns {
		if sameURL(c.URL, cfg.Endpoint) {
			if c.AccessKeyID == cfg.AccessKeyID {
				return c, true
			}
			same = append(same, c)
		}
	}
	if rejected && len(same) == 1 {
		return same[0], true
	}
	return Creds{}, false
}

// useConnection switches the backup settings to a connection, keeping the
// bucket, prefix and schedule; the entered secret is deleted.
func (m *Manager) useConnection(ctx context.Context, cr Creds) error {
	m.mu.Lock()
	if m.cfg == nil {
		m.mu.Unlock()
		return ErrNotConfigured
	}
	c := *m.cfg
	m.mu.Unlock()
	c.EndpointID, c.EndpointName = cr.ID, ""
	c.Endpoint, c.Region, c.AccessKeyID, c.SecretAccessKey, c.PathStyle = "", "", "", "", false
	raw, _ := json.Marshal(c)
	if err := m.st.SetSetting(ctx, settingConfig, string(raw)); err != nil {
		return err
	}
	if err := m.st.DeleteSetting(ctx, settingSecret); err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg = &c
	m.mu.Unlock()
	m.log.Info("backups now use the S3 connection's keys", "connection", cr.Name)
	return nil
}

// keyRejected reports an S3 error that means the key is wrong or gone.
func keyRejected(err error) bool {
	var r minio.ErrorResponse
	return errors.As(err, &r) && (r.Code == "SignatureDoesNotMatch" || r.Code == "InvalidAccessKeyId")
}

// Config returns the configuration without the secret; with a connection,
// its current address and key ID.
func (m *Manager) Config() (Config, bool) {
	m.mu.Lock()
	if m.cfg == nil {
		m.mu.Unlock()
		return Config{}, false
	}
	c := *m.cfg
	m.mu.Unlock()
	if c.EndpointID != "" && m.Connection != nil {
		if cr, err := m.Connection(context.Background(), c.EndpointID); err == nil {
			c.EndpointName, c.Endpoint, c.Region, c.AccessKeyID, c.PathStyle = cr.Name, cr.URL, cr.Region, cr.AccessKeyID, cr.PathStyle
		}
	}
	c.SecretAccessKey = ""
	return c, true
}

// live is the configuration with the credentials to use now: a
// connection's are read again every time, never kept.
func (m *Manager) live(ctx context.Context) (Config, error) {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	if cfg == nil {
		return Config{}, ErrNotConfigured
	}
	c := *cfg
	if c.EndpointID == "" {
		return c, nil
	}
	if m.Connection == nil {
		return c, errors.New("S3 connections are not available")
	}
	cr, err := m.Connection(ctx, c.EndpointID)
	if err != nil {
		return c, fmt.Errorf("S3 connection %s: %w", c.EndpointID, err)
	}
	c.EndpointName, c.Endpoint, c.Region, c.AccessKeyID, c.SecretAccessKey, c.PathStyle = cr.Name, cr.URL, cr.Region, cr.AccessKeyID, cr.SecretAccessKey, cr.PathStyle
	return c, nil
}

// EndpointInUse blocks deleting the S3 connection backups go to.
func (m *Manager) EndpointInUse(_ context.Context, id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg != nil && m.cfg.EndpointID == id {
		return "controller backups; choose another destination under Settings → Backups first"
	}
	return ""
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Validate checks and normalizes c; an empty secret keeps the stored one.
func (m *Manager) normalize(ctx context.Context, c Config) (Config, error) {
	c.EndpointID, c.EndpointName = strings.TrimSpace(c.EndpointID), ""
	if c.EndpointID != "" {
		if m.Connection == nil {
			return c, errors.New("S3 connections are not available")
		}
		cr, err := m.Connection(ctx, c.EndpointID)
		if err != nil {
			return c, fmt.Errorf("no S3 connection %q", c.EndpointID)
		}
		c.EndpointID, c.EndpointName = cr.ID, cr.Name
		c.Endpoint, c.Region, c.AccessKeyID, c.SecretAccessKey, c.PathStyle = cr.URL, cr.Region, cr.AccessKeyID, cr.SecretAccessKey, cr.PathStyle
	}
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	c.Bucket = strings.TrimSpace(c.Bucket)
	c.Prefix = strings.Trim(strings.TrimSpace(c.Prefix), "/")
	c.AccessKeyID = strings.TrimSpace(c.AccessKeyID)
	c.SecretAccessKey = strings.TrimSpace(c.SecretAccessKey) // a pasted newline breaks every signature
	if u, err := url.Parse(c.Endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return c, errors.New("endpoint must be a URL like https://s3.amazonaws.com or http://10.0.0.5:9000")
	}
	if c.Bucket == "" || c.AccessKeyID == "" {
		return c, errors.New("bucket and access key ID are required")
	}
	if c.SecretAccessKey == "" {
		m.mu.Lock()
		if m.cfg != nil && m.cfg.EndpointID == "" {
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
	c, err := m.normalize(ctx, c)
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
		return fmt.Errorf("cannot reach the bucket: %w", explainS3(err, c))
	}
	if !exists {
		return fmt.Errorf("bucket %q does not exist", c.Bucket)
	}
	// Reaching the bucket is not enough: check that this key may upload.
	probe := objectKey(c, ".syncloud-write-check")
	if _, err := cl.PutObject(cctx, c.Bucket, probe, strings.NewReader("ok"), 2, minio.PutObjectOptions{ContentType: "text/plain"}); err != nil {
		return fmt.Errorf("the bucket is reachable but uploads fail: %w", explainS3(err, c))
	}
	_ = cl.RemoveObject(cctx, c.Bucket, probe, minio.RemoveObjectOptions{})
	pub := c
	pub.SecretAccessKey, pub.EndpointName = "", ""
	if c.EndpointID != "" {
		// Only the reference is stored: the connection's keys are read at
		// every run, so a key rotated there is used here at once.
		pub.Endpoint, pub.Region, pub.AccessKeyID, pub.PathStyle = "", "", "", false
	}
	raw, _ := json.Marshal(pub)
	if err := m.st.SetSetting(ctx, settingConfig, string(raw)); err != nil {
		return err
	}
	if c.EndpointID != "" {
		if err := m.st.DeleteSetting(ctx, settingSecret); err != nil {
			return err
		}
	} else {
		sealed := m.box.Seal([]byte(c.SecretAccessKey), []byte(settingSecret))
		if err := m.st.SetSetting(ctx, settingSecret, base64.StdEncoding.EncodeToString(sealed)); err != nil {
			return err
		}
	}
	if c.EndpointID != "" {
		c = pub
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
	configured := m.cfg != nil
	m.mu.Unlock()
	if !configured {
		return Object{}, ErrNotConfigured
	}
	start := m.now().UTC()
	cfg, err := m.live(ctx)
	var obj Object
	if err == nil {
		obj, err = m.upload(ctx, cfg, start)
	}
	// The entered key was refused, but a registered connection to the same
	// S3 has the current one (rotated there): use it, and keep it if it works.
	if keyRejected(err) {
		if cr, ok := m.matchConnection(ctx, true); ok {
			alt := cfg
			alt.EndpointID, alt.EndpointName = cr.ID, cr.Name
			alt.Endpoint, alt.Region, alt.AccessKeyID, alt.SecretAccessKey, alt.PathStyle = cr.URL, cr.Region, cr.AccessKeyID, cr.SecretAccessKey, cr.PathStyle
			if o, aerr := m.upload(ctx, alt, start); aerr == nil {
				obj, err = o, nil
				if uerr := m.useConnection(ctx, cr); uerr != nil {
					m.log.Warn("backups: keep using the S3 connection", "err", uerr)
				}
			}
		}
	}
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
		return Object{}, fmt.Errorf("upload: %w", explainS3(err, cfg))
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
	cfg, err := m.live(ctx)
	if err != nil {
		return nil, err
	}
	cl, err := s3Client(cfg)
	if err != nil {
		return nil, err
	}
	objs, err := list(ctx, cl, cfg)
	return objs, explainS3(err, cfg)
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
	lookup := minio.BucketLookupAuto
	if c.PathStyle {
		lookup = minio.BucketLookupPath
	}
	return minio.New(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(c.AccessKeyID, c.SecretAccessKey, ""),
		Secure:       u.Scheme == "https",
		Region:       c.Region,
		BucketLookup: lookup,
	})
}

// WriteBundle streams a fresh backup bundle (for downloads without S3).
func (m *Manager) WriteBundle(ctx context.Context, w io.Writer) error {
	return Create(ctx, m.st, m.box, m.dataDir, w)
}

// RekeyResult is the outcome of re-keying the stored backups.
type RekeyResult struct {
	Rekeyed int `json:"rekeyed"`
	// Current already opened with the current recovery key.
	Current int `json:"current"`
	// Skipped were made with another master key and left as they are.
	Skipped []string `json:"skipped"`
	// Failed could not be read or written (name: error).
	Failed []string `json:"failed"`
}

// RekeyAll re-keys every stored backup in place so it opens with the
// current recovery key (Phase 19b). Backups are not run meanwhile.
func (m *Manager) RekeyAll(ctx context.Context) (RekeyResult, error) {
	res := RekeyResult{Skipped: []string{}, Failed: []string{}}
	m.run.Lock()
	defer m.run.Unlock()
	live, err := m.live(ctx)
	if err != nil {
		return res, err
	}
	cfg := &live
	cl, err := s3Client(live)
	if err != nil {
		return res, err
	}
	objs, err := list(ctx, cl, live)
	if err != nil {
		return res, explainS3(err, live)
	}
	for _, o := range objs {
		key := objectKey(*cfg, o.Name)
		err := func() error {
			obj, err := cl.GetObject(ctx, cfg.Bucket, key, minio.GetObjectOptions{})
			if err != nil {
				return err
			}
			defer obj.Close()
			var buf bytes.Buffer
			if _, err := buf.ReadFrom(obj); err != nil {
				return err
			}
			out, err := Rekey(buf.Bytes(), m.box)
			if err != nil {
				return err
			}
			_, err = cl.PutObject(ctx, cfg.Bucket, key, bytes.NewReader(out), int64(len(out)), minio.PutObjectOptions{ContentType: "application/octet-stream"})
			return err
		}()
		switch {
		case errors.Is(err, ErrCurrent):
			res.Current++
		case errors.Is(err, ErrOtherMasterKey):
			res.Skipped = append(res.Skipped, o.Name)
		case err != nil:
			res.Failed = append(res.Failed, o.Name+": "+err.Error())
		default:
			res.Rekeyed++
		}
	}
	m.log.Info("backups re-keyed", "rekeyed", res.Rekeyed, "current", res.Current, "skipped", len(res.Skipped), "failed", len(res.Failed))
	return res, nil
}

// explainS3 says what to do about the S3 errors a changed key causes; the
// provider's own text says only that a signature does not match.
func explainS3(err error, c Config) error {
	if err == nil {
		return nil
	}
	where := "under Settings → Backups → Edit"
	if c.EndpointID != "" {
		where = "on the S3 connection " + orName(c) + " (Storage)"
	}
	var hint string
	switch minio.ToErrorResponse(err).Code {
	case "SignatureDoesNotMatch":
		hint = fmt.Sprintf("the secret access key does not belong to access key ID %s: it is wrong, or the key was rotated at the provider. Enter the current secret %s", c.AccessKeyID, where)
	case "InvalidAccessKeyId":
		hint = fmt.Sprintf("the provider does not know access key ID %s: it was deleted or rotated. Enter the current key %s", c.AccessKeyID, where)
	case "AccessDenied":
		hint = fmt.Sprintf("access key ID %s may not write to bucket %s: give it read, write, list and delete there", c.AccessKeyID, c.Bucket)
	case "RequestTimeTooSkewed":
		hint = "this server's clock is off: sync it (timedatectl set-ntp true)"
	default:
		return err
	}
	return fmt.Errorf("%s (%w)", hint, err)
}

func orName(c Config) string {
	if c.EndpointName != "" {
		return c.EndpointName
	}
	return c.EndpointID
}
