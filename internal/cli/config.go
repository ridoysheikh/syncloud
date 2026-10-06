package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Profile holds one named set of connection settings in ~/.syncloud/credentials
// (like `aws configure`, §7.1).
type Profile struct {
	Endpoint        string `json:"endpoint"`
	AccessKeyID     string `json:"accessKeyId,omitempty"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`
	Token           string `json:"token,omitempty"`
	// SessionToken comes with temporary credentials (synctl login, sts).
	SessionToken string     `json:"sessionToken,omitempty"`
	Expiration   *time.Time `json:"expiration,omitempty"`
}

type credentialsFile struct {
	Profiles map[string]Profile `json:"profiles"`
}

// Environment variables override the profile (useful in CI).
const (
	EnvProfile  = "SYNCLOUD_PROFILE"
	EnvEndpoint = "SYNCLOUD_ENDPOINT"
	EnvKeyID    = "SYNCLOUD_ACCESS_KEY_ID"
	EnvSecret   = "SYNCLOUD_SECRET_ACCESS_KEY"
	EnvToken    = "SYNCLOUD_TOKEN"
	// EnvSessionToken goes with temporary credentials (Cloud Shell, sts).
	EnvSessionToken = "SYNCLOUD_SESSION_TOKEN"
	EnvConfigDir    = "SYNCLOUD_CONFIG_DIR"
)

func configDir() (string, error) {
	if d := os.Getenv(EnvConfigDir); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".syncloud"), nil
}

func credentialsPath() (string, error) {
	d, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "credentials"), nil
}

func loadCredentials() (credentialsFile, error) {
	f := credentialsFile{Profiles: map[string]Profile{}}
	p, err := credentialsPath()
	if err != nil {
		return f, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	} else if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("%s: %w", p, err)
	}
	if f.Profiles == nil {
		f.Profiles = map[string]Profile{}
	}
	return f, nil
}

func saveCredentials(f credentialsFile) (string, error) {
	p, err := credentialsPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", err
	}
	// Write then rename, so a crash never leaves a truncated file.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return "", err
	}
	return p, os.Rename(tmp, p)
}

// resolveProfile merges, in increasing priority: the profile file, environment
// variables, and the --endpoint flag.
func resolveProfile(name, endpointFlag string) (Profile, error) {
	if name == "" {
		name = os.Getenv(EnvProfile)
	}
	if name == "" {
		name = "default"
	}
	f, err := loadCredentials()
	if err != nil {
		return Profile{}, err
	}
	p := f.Profiles[name]
	if v := os.Getenv(EnvEndpoint); v != "" {
		p.Endpoint = v
	}
	if v := os.Getenv(EnvKeyID); v != "" {
		p.AccessKeyID, p.SecretAccessKey, p.SessionToken = v, os.Getenv(EnvSecret), os.Getenv(EnvSessionToken)
	}
	if v := os.Getenv(EnvToken); v != "" {
		p.Token = v
	}
	if endpointFlag != "" {
		p.Endpoint = endpointFlag
	}
	if p.Endpoint == "" {
		return p, fmt.Errorf("no endpoint configured for profile %q: run `synctl configure` or set %s", name, EnvEndpoint)
	}
	return p, nil
}
