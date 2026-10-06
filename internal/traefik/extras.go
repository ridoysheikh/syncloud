package traefik

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"

	yaml "go.yaml.in/yaml/v3"

	"syncloud/internal/store"
)

// SettingCustom holds the custom configuration as YAML.
const SettingCustom = "traefik.custom"

// Extras caches what the database adds to the generated configuration:
// middleware presets with their attachments, and the custom configuration.
type Extras struct {
	st  *store.Store
	log *slog.Logger

	mu       sync.RWMutex
	defs     map[string]Middleware // by Traefik name
	chains   map[string][]string   // service ID -> middleware names, in order
	ownRetry map[string]bool
	custom   map[string]any
	text     string
	settings Settings
}

func NewExtras(st *store.Store, log *slog.Logger) *Extras {
	return &Extras{st: st, log: log, defs: map[string]Middleware{}, chains: map[string][]string{}, ownRetry: map[string]bool{}, custom: map[string]any{}, settings: DefaultSettings()}
}

// MiddlewareName is a preset's Traefik name.
func MiddlewareName(id string) string { return "mw-" + strings.TrimPrefix(id, "mw_") }

// Reload reads presets and the custom configuration again.
func (e *Extras) Reload(ctx context.Context) error {
	list, err := e.st.ListMiddlewares(ctx)
	if err != nil {
		return err
	}
	defs := map[string]Middleware{}
	type att struct {
		name  string
		order int
		retry bool
	}
	by := map[string][]att{}
	for _, m := range list {
		mw, err := RenderPreset(m.Type, json.RawMessage(m.Config))
		if err != nil {
			e.log.Warn("skip middleware", "middleware", m.Name, "err", err)
			continue
		}
		name := MiddlewareName(m.ID)
		defs[name] = mw
		for _, sv := range m.ServiceIDs {
			by[sv] = append(by[sv], att{name, PresetOrder(m.Type), m.Type == "retry"})
		}
	}
	chains, own := map[string][]string{}, map[string]bool{}
	for sv, as := range by {
		sort.SliceStable(as, func(i, j int) bool { return as[i].order < as[j].order })
		for _, a := range as {
			chains[sv] = append(chains[sv], a.name)
			own[sv] = own[sv] || a.retry
		}
	}
	text, _, err := e.st.GetSetting(ctx, SettingCustom)
	if err != nil {
		return err
	}
	custom := map[string]any{}
	if text != "" {
		// Validated when saved. A reference that broke since (a deleted
		// service) only disables that router in Traefik.
		if err := yaml.Unmarshal([]byte(text), &custom); err != nil {
			e.log.Warn("custom Traefik configuration skipped", "err", err)
			custom = map[string]any{}
		}
	}
	global, _, err := e.st.GetSetting(ctx, SettingGlobal)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.defs, e.chains, e.ownRetry, e.custom, e.text = defs, chains, own, custom, text
	e.settings = ParseSettings(global)
	e.mu.Unlock()
	return nil
}

// Definitions returns the preset middlewares.
func (e *Extras) Definitions() map[string]Middleware {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.defs
}

// Chain returns a service's middlewares and whether one replaces the
// default retry.
func (e *Extras) Chain(serviceID string) ([]string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.chains[serviceID], e.ownRetry[serviceID]
}

// Custom returns the custom configuration to merge.
func (e *Extras) Custom() map[string]any {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.custom
}

// CustomText returns the stored YAML.
func (e *Extras) CustomText() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.text
}

// Settings returns the global settings.
func (e *Extras) Settings() Settings {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.settings
}
