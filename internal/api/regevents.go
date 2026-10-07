package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"syncloud/internal/registry"
	"syncloud/internal/store"
)

// serviceRef names a service that runs an image.
type serviceRef struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
}

type imageView struct {
	registry.Image
	Pushes       int          `json:"pushes"`
	Pulls        int          `json:"pulls"`
	LastPushedAt *time.Time   `json:"lastPushedAt"`
	LastPulledAt *time.Time   `json:"lastPulledAt"`
	InUseBy      []serviceRef `json:"inUseBy"`
}

// imageViews adds push and pull counters from registry events and the
// services whose current revision runs each image.
func (s *Server) imageViews(ctx context.Context, repo string, imgs []registry.Image) ([]imageView, error) {
	stats, err := s.store.ImageStatsFor(ctx, repo)
	if err != nil {
		return nil, err
	}
	byDigest := map[string]store.ImageStats{}
	for _, st := range stats {
		byDigest[st.Digest] = st
	}
	users, err := s.imageUsers(ctx, repo, imgs)
	if err != nil {
		return nil, err
	}
	out := make([]imageView, 0, len(imgs))
	for _, img := range imgs {
		st := byDigest[img.Digest]
		v := imageView{Image: img, Pushes: st.Pushes, Pulls: st.Pulls, LastPushedAt: st.LastPushedAt, LastPulledAt: st.LastPulledAt}
		v.InUseBy = users[img.Digest]
		if v.InUseBy == nil {
			v.InUseBy = []serviceRef{}
		}
		out = append(out, v)
	}
	return out, nil
}

// imageUsers maps digests of repo to the services running them.
func (s *Server) imageUsers(ctx context.Context, repo string, imgs []registry.Image) (map[string][]serviceRef, error) {
	out := map[string][]serviceRef{}
	if s.workloads == nil {
		return out, nil
	}
	var hosts []string
	if s.registryHosts != nil {
		hosts = s.registryHosts()
	}
	tagDigest := map[string]string{}
	for _, img := range imgs {
		tagDigest[img.Tag] = img.Digest
	}
	services, err := s.store.ListServices(ctx)
	if err != nil {
		return nil, err
	}
	for _, sv := range services {
		if sv.Revision < 1 || sv.Deleting {
			continue
		}
		spec, err := s.workloads.SpecFor(ctx, sv.ID, sv.Revision)
		if err != nil {
			continue
		}
		r, tag, digest, ok := registry.ParseRef(spec.Image, hosts)
		if !ok || r != repo {
			continue
		}
		if digest == "" {
			digest = tagDigest[tag]
		}
		if digest != "" {
			out[digest] = append(out[digest], serviceRef{Project: sv.Project, Environment: sv.Environment, Service: sv.Name})
		}
	}
	return out, nil
}

func latest(a, b *time.Time) *time.Time {
	if a == nil || (b != nil && b.After(*a)) {
		return b
	}
	return a
}

// handleRegistryEvents lists recent pushes, pulls and deletes (§5.9).
func (s *Server) handleRegistryEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo := q.Get("repository")
	if repo != "" && !repoNameRE.MatchString(repo) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "repository must be a registry path like shop/api")
		return
	}
	limit, before, ok := pageParams(w, r, 100, 1000)
	if !ok {
		return
	}
	evs, err := s.store.ListRegistryEvents(r.Context(), repo, limit, before)
	if err != nil {
		s.internalError(w, "list registry events", err)
		return
	}
	// Tokens name users by ID; show their email instead.
	names := map[string]string{"node": "node", "build": "build", registry.BrowserSubject: "controller"}
	for i, e := range evs {
		name, ok := names[e.Actor]
		if !ok {
			name = e.Actor
			if u, err := s.store.UserByID(r.Context(), e.Actor); err == nil {
				name = u.Email
			}
			names[e.Actor] = name
		}
		evs[i].Actor = name
	}
	writePage(w, evs, limit, func(e store.RegistryEvent) string { return strconv.FormatInt(e.ID, 10) })
}
