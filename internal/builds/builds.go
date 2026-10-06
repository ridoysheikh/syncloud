// Package builds turns Git commits into images and deployments (§5.8): it
// polls Git sources (and reacts to webhooks), builds each new commit once
// with BuildKit as a privileged job run, pushes the image to the private
// registry and, with auto-deploy, rolls it out as a new revision.
package builds

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
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
	// StaticImage serves static sites (index.html, no Dockerfile).
	StaticImage = "nginx:1.29-alpine"
	// Nixpacks builds apps without a Dockerfile; the download is checked
	// against these digests.
	NixpacksVersion       = "1.41.0"
	nixpacksSHA256X86_64  = "0f55de7874507b9cf7502113120bd96f2ab6979f78d10eaf2eb2ade9207b3af6"
	nixpacksSHA256Aarch64 = "912bd02dd2bb6f9c3a9ed965fe8a68b4aa318dc7a2546e2eca6f2806a894ba39"
	// TopicBuild carries a store.Build whenever a build changes.
	TopicBuild = "build.updated"

	BuildQueued    = "queued"
	BuildBuilding  = "building"
	BuildSucceeded = "succeeded"
	BuildFailed    = "failed"
	// BuildSkipped means no changed file matched the path filters.
	BuildSkipped = "skipped"

	// exitSkipped is build.sh's exit code for a skipped commit.
	exitSkipped = 78
	// webhookSafetyNet is the poll interval while webhooks arrive.
	webhookSafetyNet = 10 * time.Minute

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
	// UpstreamAuths are docker config.json "auths" for third-party
	// registries (private FROM images); may be nil.
	UpstreamAuths func() map[string]any
}

type Manager struct {
	// BuildSlots returns a service's project and its concurrent-builds
	// quota (0: none).
	BuildSlots func(ctx context.Context, serviceID string) (projectID string, limit int)

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

//go:embed build.sh
var buildScript string

// Builders: auto tries a Dockerfile, then Nixpacks, then a static site.
var builders = map[string]bool{"auto": true, "dockerfile": true, "nixpacks": true, "static": true}

var pathRE = regexp.MustCompile(`^!?[A-Za-z0-9._/*?\[\]-]+$`)

// Source is a Git source as the API shows and accepts it.
type Source struct {
	URL         string     `json:"url"`
	Branch      string     `json:"branch"`
	Tags        string     `json:"tags"`
	Paths       []string   `json:"paths"`
	Builder     string     `json:"builder"`
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
	// Refs are the watched branches and tags with their last seen commit.
	Refs        map[string]string `json:"refs"`
	LastWebhook *time.Time        `json:"lastWebhookAt"`
}

func (m *Manager) view(g store.GitSource) Source {
	auto := g.AutoDeploy
	refs := map[string]string{}
	for ref, sha := range g.RefSHAs {
		refs[gitremote.ShortRef(ref)] = sha
	}
	paths := g.Paths
	if paths == nil {
		paths = []string{}
	}
	return Source{URL: g.URL, Branch: g.Branch, Tags: g.Tags, Paths: paths, Builder: g.Builder, Dockerfile: g.Dockerfile, Context: g.Context,
		HasToken: len(g.TokenEnc) > 0, AutoDeploy: &auto, PollSeconds: g.PollSeconds, WebhookPath: "/api/v1/hooks/git/" + g.ID,
		Secret: g.WebhookSecret, LastSHA: g.LastSHA, LastChecked: g.LastCheckedAt, LastError: g.LastError, Refs: refs, LastWebhook: g.LastWebhookAt}
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
	for _, p := range []string{in.Dockerfile, in.Context} {
		if strings.ContainsAny(p, " \\'\"`$;&|\n#:*?[") || strings.Contains(p, "..") {
			return Source{}, ErrInvalid{fmt.Errorf("invalid path %q", p)}
		}
	}
	if err := gitremote.ValidatePattern(in.Branch); err != nil {
		return Source{}, ErrInvalid{err}
	}
	if in.Tags != "" {
		if err := gitremote.ValidatePattern(in.Tags); err != nil {
			return Source{}, ErrInvalid{err}
		}
	}
	if len(in.Paths) > 20 {
		return Source{}, ErrInvalid{errors.New("at most 20 path filters")}
	}
	for _, p := range in.Paths {
		if !pathRE.MatchString(p) || strings.Contains(p, "..") || strings.HasPrefix(strings.TrimPrefix(p, "!"), "/") {
			return Source{}, ErrInvalid{fmt.Errorf("invalid path filter %q: use repository paths like services/api/** or !docs/**", p)}
		}
	}
	if in.Builder == "" {
		in.Builder = "auto"
	}
	if !builders[in.Builder] {
		return Source{}, ErrInvalid{errors.New("builder must be auto, dockerfile, nixpacks or static")}
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
		AutoDeploy: auto, PollSeconds: in.PollSeconds, WebhookSecret: hex.EncodeToString(secret), CreatedAt: m.now().UTC(),
		Tags: in.Tags, Paths: in.Paths, Builder: in.Builder}
	if in.Token != "" {
		g.TokenEnc = m.box.Seal([]byte(in.Token), []byte("git:"+sv.ID))
	}
	// Fail early on a wrong URL, branch or token.
	refs, err := gitremote.LsRemote(ctx, g.URL, in.Token)
	if err != nil {
		return Source{}, ErrInvalid{fmt.Errorf("cannot read the repository (check the URL and token): %w", err)}
	}
	if err := branchFound(refs, g); err != nil {
		return Source{}, ErrInvalid{err}
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

// SourceSummary is a Git source with the service it builds.
type SourceSummary struct {
	Source
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
}

// ListSources returns every Git source with its service.
func (m *Manager) ListSources(ctx context.Context) ([]SourceSummary, error) {
	gs, err := m.st.ListGitSources(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SourceSummary, 0, len(gs))
	for _, g := range gs {
		sv, err := m.st.ServiceByID(ctx, g.ServiceID)
		if err != nil {
			continue
		}
		v := m.view(g)
		v.Secret = "" // shown on the service page only
		out = append(out, SourceSummary{Source: v, Project: sv.Project, Environment: sv.Environment, Service: sv.Name})
	}
	return out, nil
}

func (m *Manager) GetSource(ctx context.Context, serviceID string) (Source, error) {
	g, err := m.st.GitSourceByService(ctx, serviceID)
	if err != nil {
		return Source{}, err
	}
	return m.view(g), nil
}

// branchFound fails when no branch matches the source's branch pattern.
func branchFound(refs map[string]string, g store.GitSource) error {
	if len(gitremote.Match(refs, g.Branch, "")) > 0 {
		return nil
	}
	if gitremote.IsPattern(g.Branch) {
		return fmt.Errorf("no branch matches %s", g.Branch)
	}
	return fmt.Errorf("branch %s not found in the repository", g.Branch)
}

// Check asks for an immediate poll of a source after a push webhook.
func (m *Manager) Check(sourceID string) {
	_ = m.st.RecordGitWebhook(context.Background(), sourceID, m.now())
	m.check(sourceID, "webhook")
}

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
		if g.LastWebhookAt != nil && now.Sub(*g.LastWebhookAt) < 24*time.Hour {
			interval = max(interval, webhookSafetyNet) // webhooks work: polling only catches missed ones
		}
		if g.Failures > 0 { // back off on errors, up to an hour
			interval = min(interval<<min(g.Failures, 6), time.Hour)
		}
		if !forced && g.LastCheckedAt != nil && now.Sub(*g.LastCheckedAt) < interval {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		refs, err := gitremote.LsRemote(cctx, g.URL, m.token(g))
		cancel()
		if err == nil {
			err = branchFound(refs, g)
		}
		if err != nil {
			_ = m.st.RecordGitCheck(ctx, g.ID, "", err.Error(), now)
			m.log.Warn("git poll failed", "source", g.URL, "err", err)
			continue
		}
		trigger := "poll"
		if forced {
			trigger = forcedBy
		}
		last := m.watch(ctx, g, refs, forced, trigger)
		_ = m.st.RecordGitCheck(ctx, g.ID, last, "", now)
	}
}

// candidate is a commit to build; base is the commit path filters compare
// with ("" = build in any case).
type candidate struct{ ref, sha, base string }

// decide compares the matching refs with the last seen commits: a changed
// ref is built against its previous commit (path filters), a new branch or
// tag is built in full. The first check only builds the branch, and only
// when it is not a pattern.
func decide(g store.GitSource, matched map[string]string, forced bool) []candidate {
	branchRef := "refs/heads/" + g.Branch
	literal := !gitremote.IsPattern(g.Branch)
	prev := g.RefSHAs
	first := len(prev) == 0
	if first && g.LastSHA != "" && literal { // sources from before watch rules
		prev, first = map[string]string{branchRef: g.LastSHA}, false
	}
	names := make([]string, 0, len(matched))
	for ref := range matched {
		names = append(names, ref)
	}
	sort.Strings(names)
	var out []candidate
	for _, ref := range names {
		sha := matched[ref]
		old, seen := prev[ref]
		switch {
		case first:
			if literal && ref == branchRef {
				out = append(out, candidate{ref, sha, ""})
			}
		case !seen:
			out = append(out, candidate{ref, sha, ""})
		case old != sha:
			out = append(out, candidate{ref, sha, old})
		case forced && literal && ref == branchRef:
			// A webhook for a commit not built yet; built commits are skipped.
			out = append(out, candidate{ref, sha, ""})
		}
	}
	return out
}

// watch queues the builds decide picks and remembers the refs. It returns
// the newest queued (or the branch's) commit.
func (m *Manager) watch(ctx context.Context, g store.GitSource, refs map[string]string, forced bool, trigger string) string {
	matched := gitremote.Match(refs, g.Branch, g.Tags)
	last := g.LastSHA
	if sha, ok := matched["refs/heads/"+g.Branch]; ok {
		last = sha
	}
	for _, c := range decide(g, matched, forced) {
		if _, err := m.enqueue(ctx, g, c.ref, c.sha, c.base, trigger); err == nil {
			last = c.sha
		}
	}
	if err := m.st.SetGitRefs(ctx, g.ID, matched); err != nil {
		m.log.Warn("save watched refs", "source", g.ID, "err", err)
	}
	return last
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
func (m *Manager) enqueue(ctx context.Context, g store.GitSource, ref, sha, base, trigger string) (store.Build, error) {
	sv, err := m.st.ServiceByID(ctx, g.ServiceID)
	if err != nil {
		return store.Build{}, err
	}
	b := store.Build{ID: auth.NewID("bld_"), ServiceID: sv.ID, SHA: sha, Ref: ref, BaseSHA: base, Trigger: trigger, Status: BuildQueued,
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

// BuildNow queues a commit: the head of ref (a branch or tag name; ""
// means the source's branch) or the given sha. Path filters do not apply.
func (m *Manager) BuildNow(ctx context.Context, sv store.Service, ref, sha string) (store.Build, error) {
	g, err := m.st.GitSourceByService(ctx, sv.ID)
	if err != nil {
		return store.Build{}, ErrInvalid{errors.New("the service has no Git source")}
	}
	if ref == "" {
		if gitremote.IsPattern(g.Branch) {
			return store.Build{}, ErrInvalid{fmt.Errorf("the source watches %s: choose a branch or tag to build", g.Branch)}
		}
		ref = g.Branch
	}
	if err := gitremote.ValidatePattern(ref); err != nil || gitremote.IsPattern(ref) {
		return store.Build{}, ErrInvalid{fmt.Errorf("invalid branch or tag %q", ref)}
	}
	fullRef := "refs/heads/" + ref
	if sha == "" {
		refs, err := gitremote.LsRemote(ctx, g.URL, m.token(g))
		if err != nil {
			return store.Build{}, ErrInvalid{err}
		}
		var ok bool
		if sha, ok = refs[fullRef]; !ok {
			fullRef = "refs/tags/" + ref
			if sha, ok = gitremote.Match(refs, "", ref)[fullRef]; !ok {
				return store.Build{}, ErrInvalid{fmt.Errorf("no branch or tag %s in the repository", ref)}
			}
		}
	}
	if len(sha) != 40 || strings.Trim(sha, "0123456789abcdef") != "" {
		return store.Build{}, ErrInvalid{errors.New("sha must be a full 40-character commit hash")}
	}
	b, err := m.enqueue(ctx, g, fullRef, sha, "", "manual")
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
	running := len(building)
	perProject := map[string]int{}
	if m.BuildSlots != nil {
		for _, b := range building {
			if p, _ := m.BuildSlots(ctx, b.ServiceID); p != "" {
				perProject[p]++
			}
		}
	}
	for _, b := range queued {
		if running >= maxConcurrent {
			break
		}
		if m.BuildSlots != nil {
			// A project at its concurrent-builds quota waits in the queue (§7.2).
			if p, limit := m.BuildSlots(ctx, b.ServiceID); limit > 0 {
				if perProject[p] >= limit {
					continue
				}
				perProject[p]++
			}
		}
		m.start(ctx, b)
		running++
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
	auths := map[string]any{}
	if m.cfg.UpstreamAuths != nil {
		for k, v := range m.cfg.UpstreamAuths() {
			auths[k] = v
		}
	}
	auths[host] = map[string]string{"registrytoken": tok}
	dockerCfg, _ := json.Marshal(map[string]any{"auths": auths})
	image := host + "/" + repo
	names := image + ":" + b.SHA[:12]
	if name, ok := strings.CutPrefix(b.Ref, "refs/tags/"); ok {
		names += "," + image + ":" + dockerTag(name)
	} else {
		names += "," + image + ":latest-" + dockerTag(gitremote.ShortRef(b.Ref))
	}
	// --output is CSV: the field holding two image names must be quoted.
	output := `type=image,"name=` + names + `",push=true`
	cache := "type=registry,ref=" + image + ":buildcache"
	if m.cfg.RegistryInsecure {
		output += ",registry.insecure=true"
		cache += ",registry.insecure=true"
	}
	env := map[string]string{
		"BUILD_REGISTRY_AUTH": string(dockerCfg), "DOCKER_CONFIG": "/tmp/syncloud-docker",
		"GIT_URL": g.URL, "GIT_SHA": b.SHA, "GIT_REF": b.Ref, "BASE_SHA": b.BaseSHA,
		"WATCH_PATHS": strings.Join(g.Paths, "\n"), "CONTEXT_DIR": g.Context, "DOCKERFILE": g.Dockerfile, "BUILDER": g.Builder,
		"OUTPUT": output, "CACHE": cache, "STATIC_IMAGE": StaticImage,
		"NIXPACKS_VERSION": NixpacksVersion, "NIXPACKS_SHA256_X86_64": nixpacksSHA256X86_64, "NIXPACKS_SHA256_AARCH64": nixpacksSHA256Aarch64,
	}
	if env["BUILDER"] == "" {
		env["BUILDER"] = "auto"
	}
	if t := m.token(g); t != "" {
		env["GIT_TOKEN"] = t
	}
	spec := workload.Spec{
		Image:      BuildKitImage,
		Entrypoint: []string{"sh", "-c", buildScript, "build"}, // the image's entrypoint is buildkitd
		Env:        env,
		Resources:  workload.Resources{CPU: 1, Memory: 1024, MemoryLimit: 8192},
		Placement:  workload.Placement{Node: m.cfg.Node},
	}
	if err := spec.Normalize(); err != nil {
		return workload.Spec{}, err
	}
	return spec, nil
}

var tagChars = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// dockerTag makes a branch or tag name usable as an image tag.
func dockerTag(name string) string {
	t := strings.TrimLeft(tagChars.ReplaceAllString(name, "-"), ".-")
	if len(t) > 100 {
		t = t[:100]
	}
	if t == "" {
		t = "ref"
	}
	return t
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
	if run.ExitCode != nil && *run.ExitCode == exitSkipped {
		b.Status, b.Message = BuildSkipped, "no changed file matches the watch paths"
		_ = m.st.UpdateBuild(ctx, b)
		m.bus.Publish(TopicBuild, b)
		m.log.Info("build skipped", "build", b.ID, "sha", b.SHA[:12])
		select {
		case m.kick <- struct{}{}:
		default:
		}
		return
	}
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
