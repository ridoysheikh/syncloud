// Package regmaint keeps the private registry tidy (§5.10): it applies
// repository lifecycle policies and then runs registry garbage collection,
// on a schedule or on demand, and logs every run.
package regmaint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/events"
	"syncloud/internal/execrelay"
	"syncloud/internal/registry"
	"syncloud/internal/store"
	"syncloud/internal/system"
	"syncloud/internal/workload"
)

// TopicRun carries a store.GCRun whenever a run starts or finishes.
const TopicRun = "registry.gc"

const (
	registryTask = "sys-registry"
	storageDir   = "/var/lib/registry"
	configFile   = "/etc/distribution/config.yml"
)

var (
	// ErrBusy means a run is already in progress.
	ErrBusy = errors.New("a registry cleanup is already running")
	// ErrNoRegistry means the registry system task is not running.
	ErrNoRegistry = errors.New("the registry is not running on the controller node")
)

// ErrInvalid wraps validation errors.
type ErrInvalid struct{ Err error }

func (e ErrInvalid) Error() string { return e.Err.Error() }

type Manager struct {
	st      *store.Store
	browser *registry.Browser
	sys     *system.Manager
	exec    *execrelay.Relay
	wl      *workload.Manager
	bus     *events.Bus
	log     *slog.Logger
	// RegistryHosts are the hostnames task definitions may use for the
	// private registry besides "@registry/".
	RegistryHosts func() []string
	// Every is the time between scheduled runs.
	Every time.Duration
	now   func() time.Time

	mu      sync.Mutex
	running bool
}

func New(st *store.Store, b *registry.Browser, sys *system.Manager, ex *execrelay.Relay, wl *workload.Manager, bus *events.Bus, log *slog.Logger) *Manager {
	return &Manager{st: st, browser: b, sys: sys, exec: ex, wl: wl, bus: bus, log: log, Every: 24 * time.Hour, now: time.Now,
		RegistryHosts: func() []string { return nil }}
}

// Policy is a repository's lifecycle policy.
type Policy struct {
	Repository string          `json:"repository"`
	Rules      []registry.Rule `json:"rules"`
	UpdatedAt  time.Time       `json:"updatedAt"`
}

func (m *Manager) GetPolicy(ctx context.Context, repo string) (Policy, error) {
	p, err := m.st.LifecyclePolicy(ctx, repo)
	if err != nil {
		return Policy{}, err
	}
	out := Policy{Repository: p.Repository, UpdatedAt: p.UpdatedAt}
	return out, json.Unmarshal([]byte(p.Rules), &out.Rules)
}

func (m *Manager) SetPolicy(ctx context.Context, repo string, rules []registry.Rule, actor string) (Policy, error) {
	if repo == "" || strings.ContainsAny(repo, " :@") {
		return Policy{}, ErrInvalid{errors.New("invalid repository name")}
	}
	if err := registry.ValidateRules(rules); err != nil {
		return Policy{}, ErrInvalid{err}
	}
	b, _ := json.Marshal(rules)
	now := m.now().UTC().Truncate(time.Second)
	if err := m.st.PutLifecyclePolicy(ctx, store.LifecyclePolicy{Repository: repo, Rules: string(b), UpdatedAt: now, UpdatedBy: actor}); err != nil {
		return Policy{}, err
	}
	return Policy{Repository: repo, Rules: rules, UpdatedAt: now}, nil
}

// Preview evaluates rules against a repository's images without deleting.
func (m *Manager) Preview(ctx context.Context, repo string, rules []registry.Rule) ([]registry.Decision, error) {
	if err := registry.ValidateRules(rules); err != nil {
		return nil, ErrInvalid{err}
	}
	images, err := m.browser.Images(ctx, repo)
	if err != nil {
		return nil, err
	}
	used, err := m.inUse(ctx)
	if err != nil {
		return nil, err
	}
	return registry.Evaluate(images, rules, used.checker(repo, images), m.now()), nil
}

// refs are the image references services depend on, per repository.
type refs map[string]struct{ tags, digests map[string]bool }

func (r refs) add(repo, tag, digest string) {
	e, ok := r[repo]
	if !ok {
		e.tags, e.digests = map[string]bool{}, map[string]bool{}
	}
	if tag != "" {
		e.tags[tag] = true
	}
	if digest != "" {
		e.digests[digest] = true
	}
	r[repo] = e
}

// checker reports whether a digest of repo is in use; tags are resolved
// to digests through images.
func (r refs) checker(repo string, images []registry.Image) func(string) bool {
	e := r[repo]
	digests := map[string]bool{}
	for d := range e.digests {
		digests[d] = true
	}
	for _, img := range images {
		if e.tags[img.Tag] && img.Digest != "" {
			digests[img.Digest] = true
		}
	}
	return func(d string) bool { return digests[d] }
}

// inUse collects private-registry images of every service's current and
// previous revision (the rollback target) and of in-flight deployments.
func (m *Manager) inUse(ctx context.Context) (refs, error) {
	out := refs{}
	services, err := m.st.ListServices(ctx)
	if err != nil {
		return nil, err
	}
	hosts := m.RegistryHosts()
	for _, sv := range services {
		revs := map[int]bool{sv.Revision: true}
		if sv.Revision > 1 {
			revs[sv.Revision-1] = true
		}
		if d, err := m.st.ActiveDeployment(ctx, sv.ID); err == nil {
			revs[d.FromRev], revs[d.ToRev] = true, true
		}
		for rev := range revs {
			if rev < 1 {
				continue
			}
			spec, err := m.wl.SpecFor(ctx, sv.ID, rev)
			if err != nil {
				continue
			}
			if repo, tag, digest, ok := parseRef(spec.Image, hosts); ok {
				out.add(repo, tag, digest)
			}
		}
	}
	return out, nil
}

// parseRef splits a private-registry image reference into repository,
// tag and digest. Other images return ok=false.
func parseRef(image string, hosts []string) (repo, tag, digest string, ok bool) {
	path, found := strings.CutPrefix(image, "@registry/")
	for _, h := range hosts {
		if found {
			break
		}
		if h != "" {
			path, found = strings.CutPrefix(image, h+"/")
		}
	}
	if !found {
		return "", "", "", false
	}
	if i := strings.IndexByte(path, '@'); i >= 0 {
		path, digest = path[:i], path[i+1:]
	}
	if i := strings.LastIndexByte(path, ':'); i > strings.LastIndexByte(path, '/') {
		path, tag = path[:i], path[i+1:]
	} else if digest == "" {
		tag = "latest"
	}
	return path, tag, digest, path != ""
}

// Running reports whether a run is in progress.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// Start begins a run in the background.
func (m *Manager) Start(trigger string) (store.GCRun, error) {
	if t, ok := m.sys.Task(registryTask); !ok || t.State != "running" || m.sys.NodeID() == "" {
		return store.GCRun{}, ErrNoRegistry
	}
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return store.GCRun{}, ErrBusy
	}
	m.running = true
	m.mu.Unlock()
	run := store.GCRun{ID: auth.NewID("gc_"), Trigger: trigger, Status: "running", StartedAt: m.now().UTC().Truncate(time.Second)}
	if err := m.st.CreateGCRun(context.Background(), run); err != nil {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
		return store.GCRun{}, err
	}
	m.bus.Publish(TopicRun, run)
	go m.run(run)
	return run, nil
}

// Run starts scheduled runs until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	_ = m.st.FailRunningGCRuns(ctx, m.now())
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		runs, err := m.st.GCRuns(ctx, 1)
		if err != nil || (len(runs) == 1 && m.now().Sub(runs[0].StartedAt) < m.Every) {
			continue
		}
		if _, err := m.Start("schedule"); err != nil && !errors.Is(err, ErrNoRegistry) && !errors.Is(err, ErrBusy) {
			m.log.Warn("scheduled registry cleanup", "err", err)
		}
	}
}

// Expired is one image a run deleted.
type Expired struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Digest     string `json:"digest"`
	Reason     string `json:"reason"`
}

func (m *Manager) run(run store.GCRun) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()
	expired, perr := m.applyPolicies(ctx)
	reclaimed, gerr := m.collect(ctx)
	now := m.now().UTC().Truncate(time.Second)
	details, _ := json.Marshal(expired)
	run.Status, run.Expired, run.ReclaimedBytes, run.Details, run.FinishedAt = "succeeded", len(expired), reclaimed, string(details), &now
	var msgs []string
	for _, e := range []error{perr, gerr} {
		if e != nil {
			run.Status = "failed"
			msgs = append(msgs, e.Error())
		}
	}
	run.Message = strings.Join(msgs, "; ")
	_ = m.st.FinishGCRun(context.Background(), run)
	m.bus.Publish(TopicRun, run)
	m.log.Info("registry cleanup finished", "status", run.Status, "expired", run.Expired, "reclaimed_bytes", run.ReclaimedBytes, "message", run.Message)
}

// applyPolicies deletes the images every policy expires.
func (m *Manager) applyPolicies(ctx context.Context) ([]Expired, error) {
	policies, err := m.st.ListLifecyclePolicies(ctx)
	if err != nil {
		return nil, err
	}
	used, err := m.inUse(ctx)
	if err != nil {
		return nil, err
	}
	expired := []Expired{}
	var errs []string
	for _, p := range policies {
		var rules []registry.Rule
		if json.Unmarshal([]byte(p.Rules), &rules) != nil || registry.ValidateRules(rules) != nil {
			continue
		}
		images, err := m.browser.Images(ctx, p.Repository)
		if err != nil {
			errs = append(errs, p.Repository+": "+err.Error())
			continue
		}
		deleted := map[string]bool{}
		for _, d := range registry.Evaluate(images, rules, used.checker(p.Repository, images), m.now()) {
			if !d.Expire {
				continue
			}
			if !deleted[d.Digest] {
				if err := m.browser.DeleteTag(ctx, p.Repository, d.Tag); err != nil {
					errs = append(errs, fmt.Sprintf("%s:%s: %v", p.Repository, d.Tag, err))
					continue
				}
				deleted[d.Digest] = true
			}
			expired = append(expired, Expired{Repository: p.Repository, Tag: d.Tag, Digest: d.Digest, Reason: d.Reason})
		}
	}
	if len(errs) > 0 {
		return expired, errors.New(strings.Join(errs, "; "))
	}
	return expired, nil
}

// collect runs registry garbage collection with the registry read-only
// (pulls keep working) and returns the bytes freed.
func (m *Manager) collect(ctx context.Context) (int64, error) {
	before, _ := m.sys.Task(registryTask)
	m.sys.SetRegistryReadOnly(true)
	defer func() {
		ro, _ := m.sys.Task(registryTask)
		m.sys.SetRegistryReadOnly(false)
		_ = m.waitRestarted(context.Background(), ro.ContainerID)
	}()
	if err := m.waitRestarted(ctx, before.ContainerID); err != nil {
		return 0, err
	}
	sizeBefore, err := m.storageSize(ctx)
	if err != nil {
		return 0, err
	}
	out, code, err := m.execute(ctx, []string{"registry", "garbage-collect", "--delete-untagged", configFile})
	if err != nil {
		return 0, err
	}
	if code != 0 {
		return 0, fmt.Errorf("garbage collection exited with %d: %s", code, lastLine(out))
	}
	sizeAfter, err := m.storageSize(ctx)
	if err != nil {
		return 0, err
	}
	return max(sizeBefore-sizeAfter, 0), nil
}

// waitRestarted waits until the registry runs in a container other than old.
func (m *Manager) waitRestarted(ctx context.Context, old string) error {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if t, ok := m.sys.Task(registryTask); ok && t.State == "running" && t.ContainerID != "" && t.ContainerID != old {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return errors.New("the registry did not restart in time")
}

func (m *Manager) storageSize(ctx context.Context) (int64, error) {
	out, code, err := m.execute(ctx, []string{"du", "-sk", storageDir})
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(out))
	if code != 0 || len(f) == 0 {
		return 0, fmt.Errorf("measure registry storage: %s", lastLine(out))
	}
	kib, err := strconv.ParseInt(f[0], 10, 64)
	return kib * 1024, err
}

// execute runs a command in the registry container and returns its output.
func (m *Manager) execute(ctx context.Context, cmd []string) ([]byte, int, error) {
	s, err := m.exec.Open(m.sys.NodeID(), registryTask, cmd, false, 0, 0)
	if err != nil {
		return nil, 0, err
	}
	defer s.Close()
	var out bytes.Buffer
	for {
		select {
		case <-ctx.Done():
			return out.Bytes(), 0, ctx.Err()
		case o, ok := <-s.Out:
			if !ok {
				return out.Bytes(), 0, errors.New("the exec session ended unexpectedly")
			}
			if o.GetError() != "" {
				return out.Bytes(), 0, errors.New(o.GetError())
			}
			if out.Len() < 1<<20 {
				out.Write(o.GetData())
			}
			if o.GetExited() {
				return out.Bytes(), int(o.GetExitCode()), nil
			}
		}
	}
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}
