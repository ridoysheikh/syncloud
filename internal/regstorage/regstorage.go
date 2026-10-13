// Package regstorage chooses where the private registry keeps its images
// (Phase 19): the controller's disk (the syncloud-registry volume, the
// default) or an S3 bucket. Switching copies every image to the new
// storage through a temporary registry, switches the registry over and only
// then frees the controller's disk.
package regstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/events"
	"github.com/ridoysheikh/syncloud/internal/registry"
	"github.com/ridoysheikh/syncloud/internal/regmaint"
	"github.com/ridoysheikh/syncloud/internal/s3"
	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/system"
)

// TopicStatus carries the Status whenever a switch makes progress.
const TopicStatus = "registry.storage"

const (
	settingKey = "registry.storage"
	lastKey    = "registry.storage.last"
	// DriverDisk and DriverS3 are the storage kinds.
	DriverDisk = "disk"
	DriverS3   = "s3"
	// blobsDir is where distribution keeps layers under its root.
	blobsDir = "docker/registry/v2/blobs/"
)

// ErrInvalid wraps validation errors.
type ErrInvalid struct{ Err error }

func (e ErrInvalid) Error() string { return e.Err.Error() }

// Target is a storage the registry can keep images in.
type Target struct {
	Driver   string `json:"driver"`             // disk | s3
	Endpoint string `json:"endpoint,omitempty"` // S3 endpoint ID (name accepted on input)
	Bucket   string `json:"bucket,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
}

func (t Target) same(o Target) bool {
	return t.Driver == o.Driver && (t.Driver == DriverDisk || (t.Endpoint == o.Endpoint && t.Bucket == o.Bucket && t.Prefix == o.Prefix))
}

// Switch is a storage switch, running or finished.
type Switch struct {
	From      Target                `json:"from"`
	To        Target                `json:"to"`
	State     string                `json:"state"` // running | succeeded | failed
	Step      string                `json:"step"`
	Progress  registry.CopyProgress `json:"progress"`
	Message   string                `json:"message,omitempty"`
	Kept      string                `json:"kept,omitempty"` // where the old images were left (S3)
	Freed     int64                 `json:"freedBytes,omitempty"`
	StartedAt time.Time             `json:"startedAt"`
	EndedAt   *time.Time            `json:"endedAt,omitempty"`
	Actor     string                `json:"actor,omitempty"`
}

// Status is the registry's storage as the API shows it.
type Status struct {
	Target
	EndpointName string  `json:"endpointName,omitempty"`
	EndpointURL  string  `json:"endpointUrl,omitempty"`
	Switch       *Switch `json:"switch,omitempty"` // the running or last switch
}

type Manager struct {
	st  *store.Store
	s3  *s3.Manager
	log *slog.Logger
	// Set before Switch is used.
	Sys   *system.Manager
	Maint *regmaint.Manager
	Bus   *events.Bus
	// Main reads the registry, Copy the temporary registry.
	Main, Copy *registry.Browser
	// OnChange re-renders the system tasks.
	OnChange func()

	mu       sync.Mutex
	cur      Target
	resolved *system.RegistryS3 // cur with credentials (nil on disk)
	copyTo   *system.RegistryCopy
	sw       *Switch
}

func New(st *store.Store, s3m *s3.Manager, log *slog.Logger) *Manager {
	return &Manager{st: st, s3: s3m, log: log, cur: Target{Driver: DriverDisk}}
}

// Load reads the setting and the endpoint's credentials. Call it before
// the system tasks start, so the registry never starts on the wrong storage.
func (m *Manager) Load(ctx context.Context) error {
	cur := Target{Driver: DriverDisk}
	if v, ok, err := m.st.GetSetting(ctx, settingKey); err != nil {
		return err
	} else if ok {
		if err := json.Unmarshal([]byte(v), &cur); err != nil {
			return fmt.Errorf("registry storage setting: %w", err)
		}
	}
	var sw *Switch
	if v, ok, err := m.st.GetSetting(ctx, lastKey); err == nil && ok {
		sw = &Switch{}
		if json.Unmarshal([]byte(v), sw) != nil {
			sw = nil
		} else if sw.State == "running" { // the controller stopped mid-switch
			sw.State, sw.Message = "failed", "the controller restarted during the switch; the registry stayed on its old storage"
		}
	}
	res, err := m.resolve(ctx, cur)
	if err != nil {
		return fmt.Errorf("registry storage: %w", err)
	}
	m.mu.Lock()
	m.cur, m.resolved, m.sw = cur, res, sw
	m.mu.Unlock()
	return nil
}

func (m *Manager) resolve(ctx context.Context, t Target) (*system.RegistryS3, error) {
	if t.Driver != DriverS3 {
		return nil, nil
	}
	c, err := m.s3.Credentials(ctx, t.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("S3 endpoint %s: %w", t.Endpoint, err)
	}
	return &system.RegistryS3{URL: c.URL, Region: c.Region, AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey,
		PathStyle: c.PathStyle, Bucket: t.Bucket, Prefix: t.Prefix}, nil
}

// S3 is the registry's S3 storage for its system task (nil = the disk).
func (m *Manager) S3() *system.RegistryS3 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resolved
}

// CopyTarget is the temporary registry's storage (nil = not running).
func (m *Manager) CopyTarget() *system.RegistryCopy {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.copyTo
}

// EndpointInUse blocks deleting the endpoint the registry keeps images in
// (or is switching to).
func (m *Manager) EndpointInUse(_ context.Context, id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur.Driver == DriverS3 && m.cur.Endpoint == id {
		return "the registry's storage; switch the registry to another storage first"
	}
	if m.sw != nil && m.sw.State == "running" && m.sw.To.Endpoint == id {
		return "a running registry storage switch"
	}
	return ""
}

// EndpointChanged restarts the registry with new credentials when its
// endpoint was edited.
func (m *Manager) EndpointChanged(id string) {
	m.mu.Lock()
	cur := m.cur
	m.mu.Unlock()
	if cur.Driver != DriverS3 || cur.Endpoint != id {
		return
	}
	res, err := m.resolve(context.Background(), cur)
	if err != nil {
		m.log.Warn("registry storage credentials", "err", err)
		return
	}
	m.mu.Lock()
	m.resolved = res
	m.mu.Unlock()
	if m.OnChange != nil {
		m.OnChange()
	}
}

// S3Size measures the S3 storage's layers; ok is false on disk.
func (m *Manager) S3Size(ctx context.Context) (size int64, ok bool, err error) {
	m.mu.Lock()
	cur := m.cur
	m.mu.Unlock()
	if cur.Driver != DriverS3 {
		return 0, false, nil
	}
	u, err := m.s3.Usage(ctx, cur.Endpoint, cur.Bucket, join(cur.Prefix, blobsDir))
	return u.Bytes, true, err
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "/" + key
}

// Status returns the current storage and the running or last switch.
func (m *Manager) Status(ctx context.Context) Status {
	m.mu.Lock()
	out := Status{Target: m.cur}
	if m.sw != nil {
		sw := *m.sw
		out.Switch = &sw
	}
	m.mu.Unlock()
	if out.Driver == DriverS3 {
		if e, err := m.s3.Get(ctx, out.Endpoint); err == nil {
			out.EndpointName, out.EndpointURL = e.Name, e.URL
		}
	}
	return out
}

var prefixRE = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

// normalize validates a target and resolves an endpoint name to its ID.
func (m *Manager) normalize(ctx context.Context, t Target) (Target, error) {
	switch t.Driver {
	case DriverDisk:
		return Target{Driver: DriverDisk}, nil
	case DriverS3:
	default:
		return t, ErrInvalid{errors.New(`driver must be "disk" or "s3"`)}
	}
	t.Bucket = strings.TrimSpace(t.Bucket)
	t.Prefix = strings.Trim(strings.TrimSpace(t.Prefix), "/")
	if t.Endpoint == "" || t.Bucket == "" {
		return t, ErrInvalid{errors.New("an S3 endpoint and a bucket are required")}
	}
	if t.Prefix != "" && (len(t.Prefix) > 200 || !prefixRE.MatchString(t.Prefix)) {
		return t, ErrInvalid{errors.New("the prefix may hold letters, digits, dots, hyphens, underscores and slashes")}
	}
	e, err := m.s3.Get(ctx, t.Endpoint)
	if errors.Is(err, store.ErrNotFound) {
		return t, ErrInvalid{fmt.Errorf("no S3 endpoint %q", t.Endpoint)}
	} else if err != nil {
		return t, err
	}
	t.Endpoint = e.ID
	return t, nil
}

// Start validates the target and begins switching to it in the background.
func (m *Manager) Start(ctx context.Context, to Target, actor string) (Status, error) {
	to, err := m.normalize(ctx, to)
	if err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	from := m.cur
	m.mu.Unlock()
	if from.same(to) {
		return Status{}, ErrInvalid{errors.New("the registry already keeps its images there")}
	}
	if t, ok := m.Sys.Task("sys-registry"); !ok || t.State != "running" || m.Sys.NodeID() == "" {
		return Status{}, regmaint.ErrNoRegistry
	}
	var res *system.RegistryS3
	if to.Driver == DriverS3 {
		if err := m.s3.CheckWritable(ctx, to.Endpoint, to.Bucket, to.Prefix); err != nil {
			return Status{}, err
		}
		if res, err = m.resolve(ctx, to); err != nil {
			return Status{}, err
		}
	}
	if err := m.Maint.Acquire(); err != nil {
		return Status{}, err
	}
	sw := &Switch{From: from, To: to, State: "running", Step: "Pausing pushes", StartedAt: time.Now().UTC().Truncate(time.Second), Actor: actor}
	m.mu.Lock()
	m.sw = sw
	m.mu.Unlock()
	m.publish()
	go m.run(to, res)
	return m.Status(ctx), nil
}

// update changes the running switch and publishes it.
func (m *Manager) update(f func(*Switch)) {
	m.mu.Lock()
	f(m.sw)
	m.mu.Unlock()
	m.publish()
}

func (m *Manager) publish() {
	st := m.Status(context.Background())
	if st.Switch != nil {
		if b, err := json.Marshal(st.Switch); err == nil {
			_ = m.st.SetSetting(context.Background(), lastKey, string(b))
		}
	}
	if m.Bus != nil {
		m.Bus.Publish(TopicStatus, st)
	}
}

func (m *Manager) run(to Target, res *system.RegistryS3) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
	defer cancel()
	defer m.Maint.Release()
	from, kept, freed, err := m.switchTo(ctx, to, res)
	now := time.Now().UTC().Truncate(time.Second)
	m.update(func(s *Switch) {
		s.EndedAt, s.Step = &now, ""
		if err != nil {
			s.State, s.Message = "failed", err.Error()
			return
		}
		s.State, s.Kept, s.Freed = "succeeded", kept, freed
	})
	if err != nil {
		m.log.Warn("registry storage switch failed", "from", from.Driver, "to", to.Driver, "err", err)
	} else {
		m.log.Info("registry storage switched", "from", from.Driver, "to", to.Driver, "bucket", to.Bucket, "prefix", to.Prefix)
	}
}

// switchTo performs the steps of Phase 19. Until the setting is saved,
// any failure leaves the registry on its old storage, writable again.
func (m *Manager) switchTo(ctx context.Context, to Target, res *system.RegistryS3) (from Target, kept string, freed int64, err error) {
	m.mu.Lock()
	from = m.cur
	m.mu.Unlock()
	step := func(s string) { m.update(func(sw *Switch) { sw.Step = s }) }

	if err := m.Maint.SetReadOnly(ctx, true); err != nil {
		return from, "", 0, fmt.Errorf("pause pushes: %w", err)
	}
	switched := false
	defer func() {
		if switched {
			return
		}
		m.mu.Lock()
		m.copyTo = nil
		m.mu.Unlock()
		m.OnChange()
		_ = m.Maint.SetReadOnly(context.Background(), false)
	}()

	step("Starting the temporary registry")
	m.mu.Lock()
	m.copyTo = &system.RegistryCopy{S3: res}
	m.mu.Unlock()
	m.OnChange()
	if err := m.waitCopyRegistry(ctx); err != nil {
		return from, "", 0, err
	}

	step("Copying images")
	if _, err := registry.Copy(ctx, m.Main, m.Copy, func(p registry.CopyProgress) {
		m.update(func(sw *Switch) { sw.Progress = p })
	}); err != nil {
		return from, "", 0, fmt.Errorf("copy: %w", err)
	}
	step("Checking the copy")
	want, err := m.Main.Repositories(ctx)
	if err != nil {
		return from, "", 0, fmt.Errorf("list repositories: %w", err)
	}
	if err := registry.Verify(ctx, m.Copy, want); err != nil {
		return from, "", 0, err
	}
	var diskBefore int64
	if from.Driver == DriverDisk {
		diskBefore, _ = m.Maint.DiskSize(ctx)
	}

	step("Switching the registry")
	b, _ := json.Marshal(to)
	if err := m.st.SetSetting(ctx, settingKey, string(b)); err != nil {
		return from, "", 0, err
	}
	switched = true
	before, _ := m.Sys.Task("sys-registry")
	m.mu.Lock()
	m.cur, m.resolved, m.copyTo = to, res, nil
	m.mu.Unlock()
	m.OnChange() // still read-only: the registry restarts on the new storage first
	if err := m.Maint.WaitRestarted(ctx, before.ContainerID); err != nil {
		_ = m.Maint.SetReadOnly(context.Background(), false)
		return from, "", 0, fmt.Errorf("the registry switched but did not restart: %w", err)
	}
	if err := m.Maint.SetReadOnly(ctx, false); err != nil {
		return from, "", 0, fmt.Errorf("resume pushes: %w", err)
	}
	if err := m.waitServing(ctx, m.Main); err != nil {
		return from, "", 0, err
	}
	if err := registry.Verify(ctx, m.Main, want); err != nil {
		return from, "", 0, fmt.Errorf("the registry switched, but the old storage was kept: %w", err)
	}

	switch from.Driver {
	case DriverDisk:
		step("Freeing the controller's disk")
		out, code, err := m.Maint.Execute(ctx, []string{"sh", "-c", "rm -rf /var/lib/registry/docker"})
		if err != nil || code != 0 {
			return from, "", 0, fmt.Errorf("the registry switched, but emptying the disk failed (%v): %s", err, strings.TrimSpace(string(out)))
		}
		after, _ := m.Maint.DiskSize(ctx)
		freed = max(diskBefore-after, 0)
	case DriverS3:
		name := from.Endpoint
		if e, err := m.s3.Get(ctx, from.Endpoint); err == nil {
			name = e.Name
		}
		kept = fmt.Sprintf("s3://%s/%s on %s", from.Bucket, join(from.Prefix, "docker/"), name)
	}
	return from, kept, freed, nil
}

// waitCopyRegistry waits until the temporary registry runs and answers.
func (m *Manager) waitCopyRegistry(ctx context.Context) error {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if t, ok := m.Sys.Task(system.RegistryCopyTaskID); ok && t.State == "running" {
			if m.waitServing(ctx, m.Copy) == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if t, ok := m.Sys.Task(system.RegistryCopyTaskID); ok && t.Error != "" {
		return fmt.Errorf("the temporary registry did not start: %s", t.Error)
	}
	return errors.New("the temporary registry did not start in time")
}

// waitServing waits up to 30 s until a registry answers /v2/.
func (m *Manager) waitServing(ctx context.Context, b *registry.Browser) error {
	c := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, b.URL+"/v2/", nil)
		if resp, err := c.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the registry at %s does not answer", b.URL)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
