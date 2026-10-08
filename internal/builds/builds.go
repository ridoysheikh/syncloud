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
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/events"
	"syncloud/internal/gitprovider"
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
	// BuildChecking: built, and the after-build checks run in the image.
	BuildChecking = "checking"
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
	// SelfSignedCA returns the certificate of a platform host (the
	// registry, the built-in Git server) while it is self-signed, else "".
	SelfSignedCA func(host string) string
}

// Connections resolves Git provider connections (§5.8).
type Connections interface {
	Provider(ctx context.Context, idOrName string) (store.GitConnection, gitprovider.Provider, error)
}

type Manager struct {
	// Connections lets sources name a repository of a connected provider
	// (nil: sources need a URL).
	Connections Connections
	// DashboardURL is where Git hosts send webhooks and commit statuses link.
	DashboardURL func() string
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

	statusOnce sync.Once
	statuses   chan statusReport
}

type statusReport struct {
	b     store.Build
	state string
	desc  string
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
	URL        string   `json:"url"`
	Branch     string   `json:"branch"`
	Tags       string   `json:"tags"`
	Paths      []string `json:"paths"`
	Builder    string   `json:"builder"`
	Dockerfile string   `json:"dockerfile"`
	Context    string   `json:"context"`
	Token      string   `json:"token,omitempty"` // write-only
	HasToken   bool     `json:"hasToken"`
	// Connection and Repo pick a repository of a connected provider instead
	// of a URL; SynCloud then creates the webhook and reports statuses.
	Connection string `json:"connection,omitempty"`
	Repo       string `json:"repo,omitempty"`
	// Webhook is how pushes arrive: "created" (SynCloud added it to the
	// repository), "app" (the GitHub App's) or "manual" (add it yourself).
	Webhook     string     `json:"webhook"`
	HookError   string     `json:"hookError,omitempty"`
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
	v := Source{URL: g.URL, Branch: g.Branch, Tags: g.Tags, Paths: paths, Builder: g.Builder, Dockerfile: g.Dockerfile, Context: g.Context,
		HasToken: len(g.TokenEnc) > 0, AutoDeploy: &auto, PollSeconds: g.PollSeconds, WebhookPath: "/api/v1/hooks/git/" + g.ID,
		Secret: g.WebhookSecret, LastSHA: g.LastSHA, LastChecked: g.LastCheckedAt, LastError: g.LastError, Refs: refs, LastWebhook: g.LastWebhookAt,
		Repo: g.Repo, Webhook: "manual", HookError: g.HookError}
	if g.ConnectionID != "" {
		v.Connection = g.ConnectionID
		if m.Connections != nil {
			if c, _, err := m.Connections.Provider(context.Background(), g.ConnectionID); err == nil {
				v.Connection = c.Name
				if c.Kind == gitprovider.KindGitHubApp {
					v.Webhook = "app"
				}
			}
		}
		if g.HookID != "" {
			v.Webhook = "created"
		}
	}
	return v
}

// ErrInvalid wraps validation errors.
type ErrInvalid struct{ Err error }

func (e ErrInvalid) Error() string { return e.Err.Error() }

// SetSource connects a service to a repository.
func (m *Manager) SetSource(ctx context.Context, sv store.Service, in Source) (Source, error) {
	var conn store.GitConnection
	var prov gitprovider.Provider
	token := in.Token
	if in.Connection != "" {
		if m.Connections == nil {
			return Source{}, ErrInvalid{errors.New("Git connections are not enabled")}
		}
		var err error
		if conn, prov, err = m.Connections.Provider(ctx, in.Connection); errors.Is(err, store.ErrNotFound) {
			return Source{}, ErrInvalid{fmt.Errorf("no Git connection %s", in.Connection)}
		} else if err != nil {
			return Source{}, err
		}
		if !gitprovider.ValidRepoName(in.Repo) {
			return Source{}, ErrInvalid{errors.New("repo must be owner/name")}
		}
		repo, err := prov.Repo(ctx, in.Repo)
		if err != nil {
			return Source{}, ErrInvalid{fmt.Errorf("cannot read %s through %s: %w", in.Repo, conn.Name, err)}
		}
		if token, err = prov.Token(ctx, repo.FullName); err != nil {
			return Source{}, ErrInvalid{err}
		}
		in.URL, in.Repo, in.Token = repo.CloneURL, repo.FullName, ""
		if in.Branch == "" {
			in.Branch = repo.DefaultBranch
		}
	} else if in.Repo != "" {
		return Source{}, ErrInvalid{errors.New("repo needs a connection")}
	}
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
		Tags: in.Tags, Paths: in.Paths, Builder: in.Builder, ConnectionID: conn.ID, Repo: in.Repo}
	if in.Token != "" {
		g.TokenEnc = m.box.Seal([]byte(in.Token), []byte("git:"+sv.ID))
	}
	// Fail early on a wrong URL, branch or token.
	refs, err := gitremote.LsRemote(ctx, g.URL, token)
	if err != nil {
		return Source{}, ErrInvalid{fmt.Errorf("cannot read the repository (check the URL and token): %w", err)}
	}
	if err := branchFound(refs, g); err != nil {
		return Source{}, ErrInvalid{err}
	}
	prev, prevErr := m.st.GitSourceByService(ctx, sv.ID)
	if err := m.st.PutGitSource(ctx, g); err != nil {
		return Source{}, err
	}
	saved, err := m.st.GitSourceByService(ctx, sv.ID)
	if err != nil {
		return Source{}, err
	}
	if prevErr == nil && (prev.ConnectionID != saved.ConnectionID || !strings.EqualFold(prev.Repo, saved.Repo)) {
		m.removeHook(ctx, prev) // the old repository no longer builds this service
		saved.HookID, saved.HookError = "", ""
		_ = m.st.SetGitHook(ctx, saved.ID, "", "")
	}
	if prov != nil && saved.HookID == "" {
		saved.HookID, saved.HookError = m.createHook(ctx, prov, saved)
		_ = m.st.SetGitHook(ctx, saved.ID, saved.HookID, saved.HookError)
	}
	m.check(saved.ID, "poll")
	return m.view(saved), nil
}

// createHook adds the push webhook to the repository; failures (e.g. a
// dashboard the Git host cannot reach) leave polling in charge.
func (m *Manager) createHook(ctx context.Context, prov gitprovider.Provider, g store.GitSource) (id, problem string) {
	dash := ""
	if m.DashboardURL != nil {
		dash = strings.TrimRight(m.DashboardURL(), "/")
	}
	if dash == "" {
		return "", "the dashboard has no URL yet"
	}
	id, err := prov.CreateHook(ctx, g.Repo, dash+"/api/v1/hooks/git/"+g.ID, g.WebhookSecret)
	if err != nil {
		m.log.Warn("create webhook", "repo", g.Repo, "err", err)
		return "", "could not create the webhook (polling continues): " + err.Error()
	}
	return id, ""
}

func (m *Manager) removeHook(ctx context.Context, g store.GitSource) {
	if g.HookID == "" || g.ConnectionID == "" || m.Connections == nil {
		return
	}
	if _, prov, err := m.Connections.Provider(ctx, g.ConnectionID); err == nil {
		if err := prov.DeleteHook(ctx, g.Repo, g.HookID); err != nil {
			m.log.Warn("delete webhook", "repo", g.Repo, "err", err)
		}
	}
}

// DeleteSource disconnects a service from Git and removes the webhook
// SynCloud created.
func (m *Manager) DeleteSource(ctx context.Context, serviceID string) error {
	g, err := m.st.GitSourceByService(ctx, serviceID)
	if err != nil {
		return err
	}
	m.removeHook(ctx, g)
	return m.st.DeleteGitSource(ctx, serviceID)
}

// CheckRepo polls every source building a connection's repository (a
// GitHub App push webhook).
func (m *Manager) CheckRepo(ctx context.Context, connectionID, repo string) int {
	gs, err := m.st.GitSourcesByRepo(ctx, connectionID, repo)
	if err != nil {
		return 0
	}
	for _, g := range gs {
		m.Check(g.ID)
	}
	return len(gs)
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
		refs, err := gitremote.LsRemote(cctx, g.URL, m.token(cctx, g))
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

func (m *Manager) token(ctx context.Context, g store.GitSource) string {
	if g.ConnectionID != "" && m.Connections != nil {
		_, prov, err := m.Connections.Provider(ctx, g.ConnectionID)
		if err == nil {
			var tok string
			if tok, err = prov.Token(ctx, g.Repo); err == nil {
				return tok
			}
		}
		m.log.Warn("git connection token", "repo", g.Repo, "err", err)
		return ""
	}
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
	m.report(b, gitprovider.StatePending, "Queued ("+trigger+")")
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
		refs, err := gitremote.LsRemote(ctx, g.URL, m.token(ctx, g))
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
		m.report(b, gitprovider.StateFailure, "Build failed: "+msg)
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
	spec, err := m.buildSpec(ctx, sv, g, b)
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
	b.Message = run.Message // e.g. "waiting for a node: …" until one has room
	_ = m.st.UpdateBuild(ctx, b)
	m.bus.Publish(TopicBuild, b)
	m.log.Info("build started", "service", sv.Project+"/"+sv.Name, "sha", b.SHA[:12], "run", run.ID)
	m.report(b, gitprovider.StateRunning, "Building")
}

// buildSpec is the BuildKit task: fetch the commit from Git, build the
// Dockerfile, push to the private registry.
func (m *Manager) buildSpec(ctx context.Context, sv store.Service, g store.GitSource, b store.Build) (workload.Spec, error) {
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
	m.settingsEnv(g, env)
	if m.cfg.SelfSignedCA != nil {
		// Private networks: trust the platform's self-signed certificates.
		regHost, _, _ := strings.Cut(host, ":")
		if ca := m.cfg.SelfSignedCA(regHost); ca != "" {
			env["REGISTRY_HOST"], env["REGISTRY_CA"] = host, ca
		}
		if u, err := url.Parse(g.URL); err == nil && u.Scheme == "https" {
			if ca := m.cfg.SelfSignedCA(u.Hostname()); ca != "" {
				env["GIT_CA"] = ca
			}
		}
	}
	if t := m.token(ctx, g); t != "" {
		env["GIT_TOKEN"] = t
	}
	spec := workload.Spec{
		Image:      BuildKitImage,
		Entrypoint: []string{"sh", "-c", buildScript, "build"}, // the image's entrypoint is buildkitd
		Env:        env,
		// A modest reservation so small nodes can build (CPU is not limited;
		// memory may grow to the limit).
		Resources: workload.Resources{CPU: 0.25, Memory: 512, MemoryLimit: 8192},
		Placement: workload.Placement{Node: m.cfg.Node},
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

// onRunFinished completes a build (or its after-build checks) and deploys
// it when auto-deploy is on.
func (m *Manager) onRunFinished(ctx context.Context, run store.JobRun) {
	switch run.Trigger {
	case jobs.TriggerBuild:
		m.buildFinished(ctx, run)
	case jobs.TriggerCheck:
		m.checkFinished(ctx, run)
	}
}

func (m *Manager) buildFinished(ctx context.Context, run store.JobRun) {
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
		m.report(b, gitprovider.StateSuccess, "Skipped: no watched path changed")
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
		m.report(b, gitprovider.StateFailure, b.Message)
		return
	}
	if g, err := m.st.GitSourceByService(ctx, b.ServiceID); err == nil {
		if checks := parseStored(g).PostBuild; len(checks) > 0 {
			m.startCheck(ctx, b, checks)
			return
		}
	}
	b.Status = BuildSucceeded
	m.built(ctx, b)
}

// startCheck runs a build's after-build checks in its image.
func (m *Manager) startCheck(ctx context.Context, b store.Build, checks []string) {
	fail := func(msg string) {
		b.Status, b.Message = BuildFailed, msg
		_ = m.st.UpdateBuild(ctx, b)
		m.bus.Publish(TopicBuild, b)
		m.report(b, gitprovider.StateFailure, msg)
		m.kickQueue()
	}
	sv, err := m.st.ServiceByID(ctx, b.ServiceID)
	if err != nil {
		fail("service not found")
		return
	}
	spec, err := m.wl.SpecFor(ctx, sv.ID, sv.Revision)
	if err != nil {
		fail(err.Error())
		return
	}
	spec.Image = b.Image
	spec.Entrypoint, spec.Command = []string{"sh", "-c"}, []string{checkScript(checks)}
	run, err := m.jobs.StartCheck(ctx, sv, spec)
	if err != nil {
		fail("after-build checks could not start: " + err.Error())
		return
	}
	b.Status, b.CheckRunID, b.Message = BuildChecking, run.ID, "running after-build checks"
	_ = m.st.UpdateBuild(ctx, b)
	m.bus.Publish(TopicBuild, b)
	m.report(b, gitprovider.StateRunning, "Built; running after-build checks")
	m.kickQueue() // the builder is free while the checks run
}

// checkFinished deploys a build whose after-build checks passed.
func (m *Manager) checkFinished(ctx context.Context, run store.JobRun) {
	b, err := m.st.BuildByCheckRun(ctx, run.ID)
	if err != nil || b.Status != BuildChecking {
		return
	}
	now := m.now().UTC()
	b.FinishedAt = &now
	if run.Status != store.RunSucceeded {
		b.Status, b.Message = BuildFailed, "after-build check "+strings.ReplaceAll(run.Status, "_", " ")
		if run.ExitCode != nil {
			b.Message += fmt.Sprintf(" (exit %d)", *run.ExitCode)
		}
		if run.Message != "" {
			b.Message += ": " + run.Message
		}
		_ = m.st.UpdateBuild(ctx, b)
		m.bus.Publish(TopicBuild, b)
		m.log.Warn("after-build checks failed", "build", b.ID, "reason", b.Message)
		m.report(b, gitprovider.StateFailure, b.Message)
		return
	}
	b.Status, b.Message = BuildSucceeded, ""
	m.built(ctx, b)
}

func (m *Manager) kickQueue() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// built deploys a succeeded build when auto-deploy is on, and reports it.
func (m *Manager) built(ctx context.Context, b store.Build) {
	state, desc := gitprovider.StateSuccess, "Built; deploy it from the dashboard"
	if g, err := m.st.GitSourceByService(ctx, b.ServiceID); err == nil && g.AutoDeploy && m.newerDeployed(ctx, b) {
		// Two pushes built at once: never roll back to the older commit
		// because its build happened to finish last.
		b.Message = "not deployed: a newer commit of " + gitremote.ShortRef(b.Ref) + " is already deployed"
		desc = "Built; a newer commit is already deployed"
	} else if err == nil && g.AutoDeploy {
		if err := m.Deploy(ctx, b); err != nil {
			b.Message = "deploy failed: " + err.Error()
			state, desc = gitprovider.StateFailure, "Built, but the "+b.Message
		} else {
			b.Deployed = true
			desc = "Built and deployed"
		}
	}
	m.report(b, state, desc)
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

// report sets the commit status on the Git host for sources from a
// connection. Reports go out one at a time, in order, in the background.
func (m *Manager) report(b store.Build, state, desc string) {
	if m.Connections == nil || len(b.SHA) != 40 {
		return
	}
	m.statusOnce.Do(func() {
		m.statuses = make(chan statusReport, 256)
		go func() {
			for r := range m.statuses {
				m.sendStatus(r)
			}
		}()
	})
	select {
	case m.statuses <- statusReport{b, state, desc}:
	default:
		m.log.Warn("commit status dropped: queue full", "build", b.ID)
	}
}

func (m *Manager) sendStatus(r statusReport) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	g, err := m.st.GitSourceByService(ctx, r.b.ServiceID)
	if err != nil || g.ConnectionID == "" {
		return
	}
	sv, err := m.st.ServiceByID(ctx, r.b.ServiceID)
	if err != nil {
		return
	}
	_, prov, err := m.Connections.Provider(ctx, g.ConnectionID)
	if err != nil {
		return
	}
	target := ""
	if m.DashboardURL != nil {
		if dash := strings.TrimRight(m.DashboardURL(), "/"); dash != "" {
			target = dash + "/projects/" + sv.Project + "/" + sv.Environment + "/services/" + sv.Name + "?tab=builds"
		}
	}
	st := gitprovider.Status{State: r.state, Context: "syncloud/" + sv.Project + "/" + sv.Environment + "/" + sv.Name, Description: r.desc, TargetURL: target}
	if err := prov.SetStatus(ctx, g.Repo, r.b.SHA, st); err != nil {
		m.log.Warn("set commit status", "repo", g.Repo, "sha", r.b.SHA[:12], "err", err)
	}
}

// newerDeployed reports whether a build of the same ref queued after b is
// already deployed.
func (m *Manager) newerDeployed(ctx context.Context, b store.Build) bool {
	recent, err := m.st.ServiceBuilds(ctx, b.ServiceID, 50, "")
	if err != nil {
		return false
	}
	for _, o := range recent {
		if o.ID != b.ID && o.Ref == b.Ref && o.Deployed && o.CreatedAt.After(b.CreatedAt) {
			return true
		}
	}
	return false
}
