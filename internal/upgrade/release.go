package upgrade

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Release layout (the same as install.sh downloads):
//
//	<base>/channels/<channel>       a version, e.g. "1.4.2"
//	<base>/<version>/SHA256SUMS
//	<base>/<version>/syncloud-controller-linux-<arch>
//	<base>/<version>/syncloud-agent-linux-<arch>
//	<base>/<version>/synctl-linux-<arch>
//
// base is https://…, http://… or file:///dir (air-gapped installs and tests).
type Source struct {
	Base string
	HTTP *http.Client
}

var versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$`)

// ValidVersion reports whether v is usable as a path segment.
func ValidVersion(v string) bool { return versionRe.MatchString(v) && !strings.Contains(v, "..") }

func (s Source) get(ctx context.Context, rel string, limit int64) ([]byte, error) {
	u, err := url.Parse(strings.TrimRight(s.Base, "/") + "/" + rel)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "file" {
		f, err := os.Open(u.Path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, limit))
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("release URL must be https://, http:// or file://")
	}
	c := s.HTTP
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", u.Redacted(), resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// Latest is the version a channel points at.
func (s Source) Latest(ctx context.Context, channel string) (string, error) {
	if channel == "" {
		channel = "stable"
	}
	if !ValidVersion(channel) {
		return "", fmt.Errorf("invalid channel %q", channel)
	}
	b, err := s.get(ctx, "channels/"+channel, 256)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if !ValidVersion(v) {
		return "", fmt.Errorf("channel %s: invalid version %q", channel, v)
	}
	return v, nil
}

// Files of a release fetched by Fetch, by base name.
type Files map[string]string

// Fetch downloads a release into dir and checks every file against
// SHA256SUMS. The controller for this machine's architecture is required;
// agents and synctl for other architectures are taken when present.
func (s Source) Fetch(ctx context.Context, version, dir string) (Files, error) {
	if !ValidVersion(version) {
		return nil, fmt.Errorf("invalid version %q", version)
	}
	sums, err := s.get(ctx, version+"/SHA256SUMS", 1<<20)
	if err != nil {
		return nil, fmt.Errorf("release %s: %w", version, err)
	}
	want := parseSums(sums)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	arch := runtime.GOARCH
	files := Files{}
	for _, name := range []string{"syncloud-controller-linux-" + arch, "syncloud-agent-linux-amd64", "syncloud-agent-linux-arm64", "synctl-linux-amd64", "synctl-linux-arm64"} {
		sum, listed := want[name]
		if !listed {
			if strings.HasPrefix(name, "syncloud-controller") || name == "syncloud-agent-linux-"+arch {
				return nil, fmt.Errorf("release %s has no %s", version, name)
			}
			continue
		}
		b, err := s.get(ctx, version+"/"+name, 512<<20)
		if err != nil {
			return nil, err
		}
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != sum {
			return nil, fmt.Errorf("%s: checksum mismatch", name)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o755); err != nil {
			return nil, err
		}
		files[name] = p
	}
	return files, nil
}

func parseSums(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 {
			out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
		}
	}
	return out
}

// FileSHA256 is the hex SHA-256 of a file.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
