package workload

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/store"
)

/*
 * Auto-deploy for services that run a ready-made image. A new push of the
 * image's tag (a new digest) becomes a new revision pinned to it. Pushes are
 * found by watching the registry — the built-in one at once, others every
 * pollSeconds — or reported by CI through the service's deploy webhook.
 */

// Image watch polling bounds (seconds).
const (
	MinImagePoll     = 60
	MaxImagePoll     = 24 * 3600
	DefaultImagePoll = 300
	imageWatchTick   = 15 * time.Second
)

// ImageUpdate is the outcome of a check for a newer image.
type ImageUpdate struct {
	Deployed bool   `json:"deployed"`
	Digest   string `json:"digest"`
	Revision int    `json:"revision"`
	Message  string `json:"message"`
}

// ErrNotWatchable means the service cannot follow its image's tag.
type ErrNotWatchable struct{ Reason string }

func (e ErrNotWatchable) Error() string { return e.Reason }

// ErrDeploysByHand means the environment takes no automatic deploys.
type ErrDeploysByHand struct{ Environment string }

func (e ErrDeploysByHand) Error() string {
	return e.Environment + " deploys by hand: redeploy the service to take the new image"
}

// Watchable reports whether a service can auto-deploy pushes of its image:
// not one built from Git (its builds deploy it), and not one whose image
// names a fixed digest.
func (m *Manager) Watchable(ctx context.Context, sv store.Service) error {
	if _, err := m.st.GitSourceByService(ctx, sv.ID); err == nil {
		return ErrNotWatchable{"the service is built from Git: its builds deploy it"}
	}
	spec, err := m.SpecFor(ctx, sv.ID, sv.Revision)
	if err != nil {
		return err
	}
	switch {
	case spec.Image == AwaitingBuild:
		return ErrNotWatchable{"the service has no image yet"}
	case HasDigest(spec.Image):
		return ErrNotWatchable{"the image names a fixed digest: there is nothing newer to follow"}
	}
	return nil
}

// DeployNewImage deploys the image the service's tag points to now, when it
// differs from the running revision's. actor names who asked (the watcher,
// the webhook). The environment's policy applies: a locked environment, or
// one that deploys by hand, is not deployed to.
func (m *Manager) DeployNewImage(ctx context.Context, serviceID, actor string) (ImageUpdate, error) {
	var out ImageUpdate
	sv, err := m.st.ServiceByID(ctx, serviceID)
	if err != nil {
		return out, err
	}
	if sv.Deleting {
		return out, ErrInvalid{errors.New("the service is being deleted")}
	}
	if err := m.Watchable(ctx, sv); err != nil {
		return out, err
	}
	spec, err := m.SpecFor(ctx, sv.ID, sv.Revision)
	if err != nil {
		return out, err
	}
	if m.ResolveDigest == nil {
		return out, errors.New("image digests cannot be resolved")
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	d, err := m.ResolveDigest(rctx, spec.Image)
	cancel()
	if err != nil {
		return out, fmt.Errorf("check %s: %w", spec.Image, err)
	}
	out.Digest, out.Revision = d, sv.Revision
	if d == spec.ImageDigest {
		out.Message = "already running the newest push of " + spec.Image
		return out, nil
	}
	e, err := m.st.EnvironmentByID(ctx, sv.EnvironmentID)
	if err != nil {
		return out, err
	}
	switch {
	case e.Lock != nil:
		return out, ErrLocked{Lock: *e.Lock}
	case !e.AutoDeploy:
		return out, ErrDeploysByHand{Environment: e.Name}
	}
	ctx = WithImageRefresh(WithCause(ctx, Cause{Trigger: store.TriggerImage}))
	v, _, err := m.Apply(ctx, store.Environment{ID: sv.EnvironmentID}, sv.Name, spec, -1, actor)
	if err != nil {
		return out, err
	}
	// With release commands the new revision becomes current only after
	// they pass: the deployment has started either way.
	out.Deployed, out.Revision = true, v.Revision
	out.Message = fmt.Sprintf("deploying %s (%s)", spec.Image, shortDigest(d))
	m.log.Info("new image pushed; deploying", "service", sv.Name, "image", spec.Image, "digest", d, "revision", v.Revision, "by", actor)
	return out, nil
}

// KickImageWatch checks the watched images of the built-in registry now
// (after a push to it).
func (m *Manager) KickImageWatch() {
	select {
	case m.imageKick <- struct{}{}:
	default:
	}
}

// RunImageWatch checks watched images until ctx ends: those in the
// built-in registry at every tick (and on a kick), others every pollSeconds.
func (m *Manager) RunImageWatch(ctx context.Context) {
	t := time.NewTicker(imageWatchTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.imageKick:
		}
		m.checkImages(ctx)
	}
}

func (m *Manager) checkImages(ctx context.Context) {
	watches, err := m.st.AutoDeployWatches(ctx)
	if err != nil {
		m.log.Warn("list image watches", "err", err)
		return
	}
	now := m.now()
	for _, w := range watches {
		sv, err := m.st.ServiceByID(ctx, w.ServiceID)
		if err != nil {
			continue
		}
		spec, err := m.SpecFor(ctx, sv.ID, sv.Revision)
		if err != nil {
			continue
		}
		local := strings.HasPrefix(spec.Image, "@registry/")
		if !local && w.CheckedAt != nil && now.Sub(*w.CheckedAt) < time.Duration(max(w.PollSeconds, MinImagePoll))*time.Second {
			continue
		}
		up, err := m.DeployNewImage(ctx, sv.ID, "image-watch")
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if err := m.st.RecordImageCheck(ctx, sv.ID, up.Digest, msg, up.Deployed, now.UTC()); err != nil {
			m.log.Warn("record image check", "service", sv.Name, "err", err)
		}
	}
}
