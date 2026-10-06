package registry

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"syncloud/internal/store"
)

// EventsRetention is how long individual registry events are kept.
const EventsRetention = 30 * 24 * time.Hour

// Events receives the registry's notification webhooks (§5.9) and records
// manifest pushes, pulls and deletes, which distribution does not track
// itself: they give last-pulled times, pull counts and an activity feed.
type Events struct {
	Store       *store.Store
	Token       string
	TokenHeader string
	Log         *slog.Logger
	// OnEvent is called after new events were stored (dashboard refresh).
	OnEvent func(repos []string)
}

type envelope struct {
	Events []struct {
		ID        string    `json:"id"`
		Timestamp time.Time `json:"timestamp"`
		Action    string    `json:"action"`
		Target    struct {
			MediaType  string `json:"mediaType"`
			Digest     string `json:"digest"`
			Repository string `json:"repository"`
			Tag        string `json:"tag"`
		} `json:"target"`
		Request struct {
			Addr      string `json:"addr"`
			Method    string `json:"method"`
			UserAgent string `json:"useragent"`
		} `json:"request"`
		Actor struct {
			Name string `json:"name"`
		} `json:"actor"`
	} `json:"events"`
}

// isManifest tells manifest and index events apart from layer and config
// blobs, which would only add noise.
func isManifest(mediaType string) bool {
	return strings.Contains(mediaType, "manifest") || strings.Contains(mediaType, "image.index")
}

// Parse turns a notification envelope into the events worth keeping.
func Parse(body []byte) ([]store.RegistryEvent, error) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	var out []store.RegistryEvent
	for _, e := range env.Events {
		switch e.Action {
		case "push", "pull":
			// HEAD counts: Docker resolves a tag with HEAD, which is often the
			// only manifest request when the node already has the content.
			if !isManifest(e.Target.MediaType) {
				continue
			}
			if e.Action == "pull" && e.Actor.Name == BrowserSubject {
				continue // the dashboard reading manifests is not a pull
			}
		case "delete":
		default:
			continue // mounts and anything newer
		}
		if e.ID == "" || e.Target.Repository == "" {
			continue
		}
		at := e.Timestamp
		if at.IsZero() {
			at = time.Now()
		}
		addr := e.Request.Addr
		if i := strings.LastIndexByte(addr, ':'); i > 0 && !strings.HasSuffix(addr, "]") {
			addr = strings.Trim(addr[:i], "[]")
		}
		out = append(out, store.RegistryEvent{
			EventID: e.ID, At: at.UTC(), Action: e.Action, Repository: e.Target.Repository,
			Tag: e.Target.Tag, Digest: e.Target.Digest, Actor: e.Actor.Name, Addr: addr,
			UserAgent: truncate(e.Request.UserAgent, 200),
		})
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (h *Events) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(h.TokenHeader)), []byte(h.Token)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	evs, err := Parse(body)
	if err != nil {
		// A malformed envelope will never parse: accept it so the registry
		// does not retry it forever.
		h.Log.Warn("registry notification", "err", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	n, err := h.Store.RecordRegistryEvents(r.Context(), evs)
	if err != nil {
		h.Log.Error("record registry events", "err", err)
		http.Error(w, "store", http.StatusInternalServerError) // retried
		return
	}
	if n > 0 && h.OnEvent != nil {
		seen := map[string]bool{}
		var repos []string
		for _, e := range evs {
			if !seen[e.Repository] {
				seen[e.Repository] = true
				repos = append(repos, e.Repository)
			}
		}
		h.OnEvent(repos)
	}
	w.WriteHeader(http.StatusNoContent)
}

// Prune drops old events once an hour.
func (h *Events) Prune(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := h.Store.PruneRegistryEvents(ctx, time.Now().Add(-EventsRetention)); err != nil && ctx.Err() == nil {
			h.Log.Warn("prune registry events", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
