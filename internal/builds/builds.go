// Package builds turns Git commits into images and deployments (§5.8): it
// polls Git sources (and reacts to webhooks), builds each new commit once
// with BuildKit as a privileged job run, pushes the image to the private
// registry and, with auto-deploy, rolls it out as a new revision.
package builds

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/events"
	"syncloud/internal/gitremote"
	"syncloud/internal/jobs"
	"syncloud/internal/registry"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
	"syncloud/internal/workload"
)

const (
	// BuildKitImage is pinned per release (§5.0 release manifest).
	BuildKitImage = "moby/buildkit:v0.25.1"
	// TopicBuild carries a store.Build whenever a build changes.
	TopicBuild = "build.updated"

	BuildQueued    = "queued"
	BuildBuilding  = "building"
	BuildSucceeded = "succeeded"
	BuildFailed    = "failed"

	maxConcurrent = 2
	minPoll       = 15
)

type Config struct {
	// RegistryHost is where builds push ("" = the registry host of the base domain).
	RegistryHost func() string
	// RegistryInsecure pushes over plain HTTP (development).
	RegistryInsecure bool
	// Node pins builds to one node ("" = any schedulable node).
	Node string
}

type Manager struct {
	st     *store.Store
	box    *secrets.Box
	jobs   *jobs.Manager
	wl     *workload.Manager
	issuer *registry.Issuer
	bus    *events.Bus
	log    *slog.Logger
	cfg    Config
	now    func() time.Time

	mu   sync.Mutex
	kick chan struct{}
	due  map[string]string // sources to check now -> build trigger
}

func New(st *store.Store, box *secrets.Box, jm *jobs.Manager, wl *workload.Manager, issuer *registry.Issuer, bus *events.Bus, log *slog.Logger, cfg Config) *Manager {
	m := &Manager{st: st, box: box, jobs: jm, wl: wl, issuer: issuer, bus: bus, log: log, cfg: cfg, now: time.Now,
		kick: make(chan struct{}, 1), due: map[string]string{}}
	jm.OnFinished = m.onRunFinished
	return m
}

// Source is a Git source as the API shows and accepts it.
type Source struct {
	URL         string     `json:"url"`
	Branch      string     `json:"branch"`
	Dockerfile  string     `json:"dockerfile"`
	Context     string     `json:"context"`
	Token       string     `json:"token,omitempty"` // write-only
	HasToken    bool       `json:"hasToken"`
	AutoDeploy  *bool      `json:"autoDeploy,omitempty"`
	PollSeconds int        `json:"pollSeconds"`
	WebhookPath string     `json:"webhookPath"` // relative to the dashboard URL
	Secret      string     `json:"webhookSecret"`
	LastSHA     string     `json:"lastSha"`
	LastChecked *time.Time `json:"lastCheckedAt"`
	LastError   string     `json:"lastError"`
}

func (m *Manager) view(g store.GitSource) Source {
	auto := g.AutoDeploy
	return Source{URL: g.URL, Branch: g.Branch, Dockerfile: g.Dockerfile, Context: g.Context, HasToken: len(g.TokenEnc) > 0, AutoDeploy: &auto,
		PollSeconds: g.PollSeconds, WebhookPath: "/api/v1/hooks/git/" + g.ID, Secret: g.WebhookSecret, LastSHA: g.LastSHA,
		LastChecked: g.LastCheckedAt, LastError: g.LastError}
}

// ErrInvalid wraps validation errors.
type ErrInvalid struct{ Err error }

func (e ErrInvalid) Error() string { return e.Err.Error() }

// SetSource connects a service to a repository.
func (m *Manager) SetSource(ctx context.Context, sv store.Service, in Source) (Source, error) {
	if err := gitremote.ValidateURL(in.URL); err != nil {
		return Source{}, ErrInvalid{err}
	}
	if in.Branch == "" {
		in.Branch = "main"
	}
	if in.Dockerfile == "" {
		in.Dockerfile = "Dockerfile"
	}
	in.Context = strings.Trim(in.Context, "/")
	for _, p := range []string{in.Branch, in.Dockerfile, in.Context} {
		if strings.ContainsAny(p, " \\'\"`$;&|\n#:") || strings.Contains(p, "..") {
			return Source{}, ErrInvalid{fmt.Errorf("invalid branch or path %q", p)}
		}
	}
	if in.PollSeconds == 0 {
		in.PollSeconds = 60
	}
	if in.PollSeconds < minPoll {
		return Source{}, ErrInvalid{fmt.Errorf("pollSeconds must be at least %d", minPoll)}
	}
	auto := in.AutoDeploy == nil || *in.AutoDeploy
	secret := make([]byte, 20)
	_, _ = rand.Read(secret)
	g := store.GitSource{ID: auth.NewID("git_"), ServiceID: sv.ID, URL: in.URL, Branch: in.Branch, Dockerfile: in.Dockerfile, Context: in.Context,
		AutoDeploy: auto, PollSeconds: in.PollSeconds, WebhookSecret: hex.EncodeToString(secret), CreatedAt: m.now().UTC()}
	if in.Token != "" {
		g.TokenEnc = m.box.Seal([]byte(in.Token), []byte("git:"+sv.ID))
	}
	// Fail early on a wrong URL, branch or token.
	refs, err := gitremote.LsRemote(ctx, g.URL, in.Token)
	if err != nil {
		return Source{}, ErrInvalid{err}
	}
	if _, ok := gitremote.BranchSHA(refs, g.Branch); !ok {
		return Source{}, ErrInvalid{fmt.Errorf("branch %s not found in the repository", g.Branch)}
	}
	if err := m.st.PutGitSource(ctx, g); err != nil {
		return Source{}, err
	}
	saved, err := m.st.GitSourceByService(ctx, sv.ID)
	if err != nil {
		return Source{}, err
	}
	m.check(saved.ID, "poll")
	return m.view(saved), nil
}

func (m *Manager) GetSource(ctx context.Context, serviceID string) (Source, error) {
	g, err := m.st.GitSourceByService(ctx, serviceID)
	if err != nil {
		return Source{}, err
	}
	return m.view(g), nil
}

// Check asks for an immediate poll of a source after a push webhook.
func (m *Manager) Check(sourceID string) { m.check(sourceID, "webhook") }

func (m *Manager) check(sourceID, trigger string) {
	m.mu.Lock()
	m.due[sourceID] = trigger
	m.mu.Unlock()
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// WebhookSecret returns a source's secret for signature checks.
func (m *Manager) WebhookSecret(ctx context.Context, sourceID string) (string, error) {
	g, err := m.st.GitSourceByID(ctx, sourceID)
	return g.WebhookSecret, err
}

// Run polls sources and starts queued builds until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.kick:
		}
		m.poll(ctx)
		m.startQueued(ctx)
	}
}

func (m *Manager) poll(ctx context.Context) {
	sources, err := m.st.ListGitSources(ctx)
	if err != nil {
		return
	}
	now := m.now()
	for _, g := range sources {
		m.mu.Lock()
		forcedBy, forced := m.due[g.ID]
		delete(m.due, g.ID)
		m.mu.Unlock()
		interval := time.Duration(g.PollSeconds) * time.Second
		if g.Failures > 0 { // back off on errors, up to an hour
			interval = min(interval<<min(g.Failures, 6), time.Hour)
		}
		if !forced && g.LastCheckedAt != nil && now.Sub(*g.LastCheckedAt) < interval {
			continue
		}
		token := m.token(g)
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		refs, err := gitremote.LsRemote(cctx, g.URL, token)
		cancel()
		sha, ok := "", false
		if err == nil {
			if sha, ok = gitremote.BranchSHA(refs, g.Branch); !ok {
				err = fmt.Errorf("branch %s not found", g.Branch)
			}
		}
		if err != nil {
			_ = m.st.RecordGitCheck(ctx, g.ID, "", err.Error(), now)
			m.log.Warn("git poll failed", "source", g.URL, "err", err)
			continue
		}
		_ = m.st.RecordGitCheck(ctx, g.ID, sha, "", now)
		trigger := "poll"
		if forced {
			trigger = forcedBy
		}
		if sha != g.LastSHA || forced {
			m.enqueue(ctx, g, sha, trigger)
		}
	}
}

func (m *Manager) token(g store.GitSource) string {
	if len(g.TokenEnc) == 0 {
		return ""
	}
	b, err := m.box.Open(g.TokenEnc, []byte("git:"+g.ServiceID))
	if err != nil {
		return ""
	}
	return string(b)
}

// enqueue records a build of sha; a commit is never built twice.
func (m *Manager) enqueue(ctx context.Context, g store.GitSource, sha, trigger string) (store.Build, error) {
	sv, err := m.st.ServiceByID(ctx, g.ServiceID)
	if err != nil {
		return store.Build{}, err
	}
	b := store.Build{ID: auth.NewID("bld_"), ServiceID: sv.ID, SHA: sha, Ref: "refs/heads/" + g.Branch, Trigger: trigger, Status: BuildQueued,
		Image: "@registry/" + Repository(sv) + ":" + sha[:12], CreatedAt: m.now().UTC().Truncate(time.Second)}
	if err := m.st.CreateBuild(ctx, b); errors.Is(err, store.ErrNameTaken) {
		return store.Build{}, err
	} else if err != nil {
		return store.Build{}, err
	}
	m.log.Info("build queued", "service", sv.Project+"/"+sv.Name, "sha", sha[:12], "trigger", trigger)
	m.bus.Publish(TopicBuild, b)
	select {
	case m.kick <- struct{}{}:
	default:
	}
	return b, nil
}

// BuildNow queues the branch head (or a given commit) for a service.
func (m *Manager) BuildNow(ctx context.Context, sv store.Service, sha string) (store.Build, error) {
	g, err := m.st.GitSourceByService(ctx, sv.ID)
	if err != nil {
		return store.Build{}, ErrInvalid{errors.New("the service has no Git source")}
	}
	if sha == "" {
		refs, err := gitremote.LsRemote(ctx, g.URL, m.token(g))
		if err != nil {
			return store.Build{}, ErrInvalid{err}
		}
		var ok bool
		if sha, ok = gitremote.BranchSHA(refs, g.Branch); !ok {
			return store.Build{}, ErrInvalid{fmt.Errorf("branch %s not found", g.Branch)}
		}
	}
	if len(sha) != 40 || strings.Trim(sha, "0123456789abcdef") != "" {
		return store.Build{}, ErrInvalid{errors.New("sha must be a full 40-character commit hash")}
	}
	b, err := m.enqueue(ctx, g, sha, "manual")
	if errors.Is(err, store.ErrNameTaken) {
		return store.Build{}, ErrInvalid{fmt.Errorf("commit %s was already built; redeploy that build instead", sha[:12])}
	}
	return b, err
}

// Repository is the registry path for a service's images.
func Repository(sv store.Service) string { return sv.Project + "/" + sv.Name }

func (m *Manager) startQueued(ctx context.Context) {
	building, err := m.st.BuildsByStatus(ctx, BuildBuilding)
	if err != nil {
		return
	}
	queued, err := m.st.BuildsByStatus(ctx, BuildQueued)
	if err != nil {
		return
	}
	for i := 0; i < len(queued) && len(building)+i < maxConcurrent; i++ {
		m.start(ctx, queued[i])
	}
}

func (m *Manager) start(ctx context.Context, b store.Build) {
	fail := func(msg string) {
		now := m.now().UTC()
		b.Status, b.Message, b.FinishedAt = BuildFailed, msg, &now
		_ = m.st.UpdateBuild(ctx, b)
		m.bus.Publish(TopicBuild, b)
	}
	sv, err := m.st.ServiceByID(ctx, b.ServiceID)
	if err != nil {
		fail("service not found")
		return
	}
	g, err := m.st.GitSourceByService(ctx, sv.ID)
	if err != nil {
		fail("the service no longer has a Git source")
		return
	}
	spec, err := m.buildSpec(sv, g, b)
	if err != nil {
		fail(err.Error())
		return
	}
	run, err := m.jobs.StartBuild(ctx, sv, spec)
	if err != nil {
		fail(err.Error())
		return
	}
	now := m.now().UTC()
	b.Status, b.RunID, b.StartedAt = BuildBuilding, run.ID, &now
	_ = m.st.UpdateBuild(ctx, b)
	m.bus.Publish(TopicBuild, b)
	m.log.Info("build started", "service", sv.Project+"/"+sv.Name, "sha", b.SHA[:12], "run", run.ID)
}

// buildSpec is the BuildKit task: fetch the commit from Git, build the
// Dockerfile, push to the private registry.
func (m *Manager) buildSpec(sv store.Service, g store.GitSource, b store.Build) (workload.Spec, error) {
	host := m.cfg.RegistryHost()
	if host == "" {
		return workload.Spec{}, errors.New("no registry host: set a base domain first")
	}
	repo := Repository(sv)
	tok, err := m.issuer.IssueTTL("build", []registry.Access{{Type: "repository", Name: repo, Actions: []string{"pull", "push"}}}, m.now(), 2*time.Hour)
	if err != nil {
		return workload.Spec{}, err
	}
	dockerCfg, _ := json.Marshal(map[string]any{"auths": map[string]any{host: map[string]string{"registrytoken": tok}}})
	gitContext := g.URL + "#" + b.SHA
	if g.Context != "" {
		gitContext += ":" + g.Context
	}
	// --output is CSV: the field holding two image names must be quoted.
	output := `type=image,"name=` + host + "/" + repo + ":" + b.SHA[:12] + "," + host + "/" + repo + ":latest-" + strings.ReplaceAll(g.Branch, "/", "-") + `",push=true`
	if m.cfg.RegistryInsecure {
		output += ",registry.insecure=true"
	}
	args := []string{
		"buildctl-daemonless.sh", "build", "--progress=plain", "--frontend", "dockerfile.v0",
		"--opt", "context=" + gitContext, "--opt", "filename=" + g.Dockerfile, "--output", output,
	}
	env := map[string]string{"BUILD_REGISTRY_AUTH": string(dockerCfg), "DOCKER_CONFIG": "/tmp/syncloud-docker"}
	if t := m.token(g); t != "" {
		u, _ := url.Parse(g.URL)
		env["GIT_TOKEN"] = t
		args = append(args, "--secret", "id=GIT_AUTH_TOKEN."+u.Hostname()+",env=GIT_TOKEN")
	}
	script := `mkdir -p "$DOCKER_CONFIG" && printf '%s' "$BUILD_REGISTRY_AUTH" > "$DOCKER_CONFIG/config.json" && exec "$@"`
	spec := workload.Spec{
		Image:      BuildKitImage,
		Entrypoint: []string{"sh", "-c", script, "build"}, // the image's entrypoint is buildkitd
		Command:    args,
		Env:        env,
		Resources:  workload.Resources{CPU: 1, Memory: 1024, MemoryLimit: 8192},
		Placement:  workload.Placement{Node: m.cfg.Node},
	}
	if err := spec.Normalize(); err != nil {
		return workload.Spec{}, err
	}
	return spec, nil
}

// onRunFinished completes a build and deploys it when auto-deploy is on.
func (m *Manager) onRunFinished(ctx context.Context, run store.JobRun) {
	if run.Trigger != jobs.TriggerBuild {
		return
	}
	b, err := m.st.BuildByRun(ctx, run.ID)
	if err != nil {
		return
	}
	now := m.now().UTC()
	b.FinishedAt = &now
	if run.Status != store.RunSucceeded {
		b.Status, b.Message = BuildFailed, "build "+strings.ReplaceAll(run.Status, "_", " ")
		if run.Message != "" {
			b.Message += ": " + run.Message
		}
		_ = m.st.UpdateBuild(ctx, b)
		m.bus.Publish(TopicBuild, b)
		m.log.Warn("build failed", "build", b.ID, "reason", b.Message)
		return
	}
	b.Status = BuildSucceeded
	if g, err := m.st.GitSourceByService(ctx, b.ServiceID); err == nil && g.AutoDeploy {
		if err := m.Deploy(ctx, b); err != nil {
			b.Message = "deploy failed: " + err.Error()
		} else {
			b.Deployed = true
		}
	}
	_ = m.st.UpdateBuild(ctx, b)
	m.bus.Publish(TopicBuild, b)
	select {
	case m.kick <- struct{}{}: // start the next queued build
	default:
	}
}

// Deploy rolls out a successful build's image as a new revision.
func (m *Manager) Deploy(ctx context.Context, b store.Build) error {
	if b.Status != BuildSucceeded {
		return ErrInvalid{errors.New("only succeeded builds can be deployed")}
	}
	sv, err := m.st.ServiceByID(ctx, b.ServiceID)
	if err != nil {
		return err
	}
	spec, err := m.wl.SpecFor(ctx, sv.ID, sv.Revision)
	if err != nil {
		return err
	}
	spec.Image = b.Image
	_, _, err = m.wl.Apply(ctx, store.Environment{ID: sv.EnvironmentID}, sv.Name, spec, -1, "build:"+b.ID)
	if err == nil {
		m.log.Info("build deployed", "service", sv.Project+"/"+sv.Name, "image", b.Image)
	}
	return err
}
