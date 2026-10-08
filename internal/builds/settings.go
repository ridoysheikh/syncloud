package builds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"syncloud/internal/store"
)

// Settings are a Git source's build settings (Phase 15b): Nixpacks command
// overrides, build-time variables and after-build checks.
type Settings struct {
	// InstallCommand, BuildCommand and StartCommand override what Nixpacks
	// detects (NIXPACKS_INSTALL_CMD, …_BUILD_CMD, …_START_CMD).
	InstallCommand string `json:"installCommand"`
	BuildCommand   string `json:"buildCommand"`
	StartCommand   string `json:"startCommand"`
	// PostBuild commands run in order in the new image, with the service's
	// variables, before it is deployed; a failure stops the deployment.
	PostBuild []string `json:"postBuild"`
	// Variables are build-time variables: Dockerfile build arguments and
	// Nixpacks environment. Only their names are shown.
	Variables []string `json:"variables"`
}

// SettingsInput changes build settings. A variable set to null keeps its
// stored value; variables left out are removed.
type SettingsInput struct {
	InstallCommand string             `json:"installCommand"`
	BuildCommand   string             `json:"buildCommand"`
	StartCommand   string             `json:"startCommand"`
	PostBuild      []string           `json:"postBuild"`
	Variables      map[string]*string `json:"variables"`
}

// stored is what the source's build_settings column holds.
type stored struct {
	InstallCommand string   `json:"installCommand,omitempty"`
	BuildCommand   string   `json:"buildCommand,omitempty"`
	StartCommand   string   `json:"startCommand,omitempty"`
	PostBuild      []string `json:"postBuild,omitempty"`
}

var buildVarRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func parseStored(g store.GitSource) stored {
	var s stored
	_ = json.Unmarshal([]byte(g.BuildSettings), &s)
	return s
}

// buildVars decrypts a source's build-time variables.
func (m *Manager) buildVars(g store.GitSource) map[string]string {
	out := map[string]string{}
	if len(g.BuildVarsEnc) == 0 {
		return out
	}
	b, err := m.box.Open(g.BuildVarsEnc, []byte("buildvars:"+g.ServiceID))
	if err != nil {
		m.log.Warn("build variables", "service", g.ServiceID, "err", err)
		return out
	}
	_ = json.Unmarshal(b, &out)
	return out
}

func settingsView(s stored, vars map[string]string) Settings {
	v := Settings{InstallCommand: s.InstallCommand, BuildCommand: s.BuildCommand, StartCommand: s.StartCommand,
		PostBuild: s.PostBuild, Variables: slices.Sorted(maps.Keys(vars))}
	if v.PostBuild == nil {
		v.PostBuild = []string{}
	}
	return v
}

// GetSettings returns a service's build settings.
func (m *Manager) GetSettings(ctx context.Context, sv store.Service) (Settings, error) {
	g, err := m.st.GitSourceByService(ctx, sv.ID)
	if err != nil {
		return Settings{}, err
	}
	return settingsView(parseStored(g), m.buildVars(g)), nil
}

// SetSettings validates and stores a service's build settings.
func (m *Manager) SetSettings(ctx context.Context, sv store.Service, in SettingsInput) (Settings, error) {
	g, err := m.st.GitSourceByService(ctx, sv.ID)
	if err != nil {
		return Settings{}, err
	}
	s := stored{InstallCommand: strings.TrimSpace(in.InstallCommand), BuildCommand: strings.TrimSpace(in.BuildCommand),
		StartCommand: strings.TrimSpace(in.StartCommand)}
	for name, c := range map[string]string{"install": s.InstallCommand, "build": s.BuildCommand, "start": s.StartCommand} {
		if strings.ContainsAny(c, "\n\r") || len(c) > 1000 {
			return Settings{}, ErrInvalid{fmt.Errorf("the %s command must be one line of at most 1000 characters", name)}
		}
	}
	for _, c := range in.PostBuild {
		if c = strings.TrimSpace(c); c != "" {
			s.PostBuild = append(s.PostBuild, c)
		}
	}
	if len(s.PostBuild) > 10 {
		return Settings{}, ErrInvalid{errors.New("at most 10 after-build checks")}
	}
	for i, c := range s.PostBuild {
		if len(c) > 4000 {
			return Settings{}, ErrInvalid{fmt.Errorf("after-build check %d is longer than 4000 characters", i+1)}
		}
	}
	old := m.buildVars(g)
	vars := map[string]string{}
	if len(in.Variables) > 50 {
		return Settings{}, ErrInvalid{errors.New("at most 50 build variables")}
	}
	for k, v := range in.Variables {
		if !buildVarRE.MatchString(k) {
			return Settings{}, ErrInvalid{fmt.Errorf("%q is not a variable name", k)}
		}
		switch {
		case v != nil:
			if strings.ContainsAny(*v, "\n\r") || len(*v) > 4000 {
				return Settings{}, ErrInvalid{fmt.Errorf("variable %s must be one line of at most 4000 characters", k)}
			}
			vars[k] = *v
		default:
			prev, ok := old[k]
			if !ok {
				return Settings{}, ErrInvalid{fmt.Errorf("variable %s has no stored value to keep", k)}
			}
			vars[k] = prev
		}
	}
	raw, _ := json.Marshal(s)
	var enc []byte
	if len(vars) > 0 {
		b, _ := json.Marshal(vars)
		enc = m.box.Seal(b, []byte("buildvars:"+sv.ID))
	}
	if err := m.st.SetGitBuildSettings(ctx, sv.ID, string(raw), enc); err != nil {
		return Settings{}, err
	}
	return settingsView(s, vars), nil
}

// settingsEnv is the build task environment for a source's settings.
func (m *Manager) settingsEnv(g store.GitSource, env map[string]string) {
	s := parseStored(g)
	for k, v := range map[string]string{"NIXPACKS_INSTALL_CMD": s.InstallCommand, "NIXPACKS_BUILD_CMD": s.BuildCommand, "NIXPACKS_START_CMD": s.StartCommand} {
		if v != "" {
			env[k] = v
		}
	}
	vars := m.buildVars(g)
	lines := make([]string, 0, len(vars))
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		lines = append(lines, k+"="+vars[k])
	}
	if len(lines) > 0 {
		env["BUILD_VARS"] = strings.Join(lines, "\n")
	}
}

// checkScript runs after-build checks in order, stopping at the first
// failure.
func checkScript(checks []string) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	for i, c := range checks {
		fmt.Fprintf(&b, "echo '==> after-build check %d'\n%s\n", i+1, c)
	}
	return b.String()
}
