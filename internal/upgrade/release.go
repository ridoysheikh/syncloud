package upgrade

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	"github.com/ridoysheikh/syncloud/internal/version"
)

// DefaultReleaseURL is where releases are published: the project's GitHub
// releases.
const DefaultReleaseURL = version.Repository

// Release layout (the same as install.sh downloads):
//
//	<base>/channels/<channel>       a version, e.g. "1.4.2"
//	<base>/<version>/SHA256SUMS
//	<base>/<version>/syncloud-controller-linux-<arch>
//	<base>/<version>/syncloud-agent-linux-<arch>
//	<base>/<version>/synctl-linux-<arch>
//
// base is https://…, http://… or file:///dir (mirrors, air-gapped installs
// and tests). A GitHub repository base (https://github.com/<owner>/<repo>)
// uses GitHub's release layout instead: files are release assets of the tag
// v<version>, "stable" is the latest release and "beta" the newest one
// including prereleases.
type Source struct {
	Base string
	HTTP *http.Client
	// API is GitHub's REST API (default https://api.github.com).
	API string
}

// githubRepo is the "<owner>/<repo>" of a GitHub repository base.
func (s Source) githubRepo() (string, bool) {
	u, err := url.Parse(strings.TrimRight(s.Base, "/"))
	if err != nil || u.Scheme != "https" || (u.Host != "github.com" && u.Host != "www.github.com") {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

// tag is the Git tag of a version ("1.4.2" and "v1.4.2" are both v1.4.2).
func tag(version string) string { return "v" + strings.TrimPrefix(version, "v") }

func (s Source) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

// githubLatest resolves a channel of a GitHub repository: "stable" through
// the releases/latest redirect (no API quota), anything else as the newest
// non-draft release including prereleases.
func (s Source) githubLatest(ctx context.Context, repo, channel string) (string, error) {
	if channel == "stable" {
		c := *s.client()
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://github.com/"+repo+"/releases/latest", nil)
		if err != nil {
			return "", err
		}
		resp, err := c.Do(req)
		if err != nil {
			return "", err
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		i := strings.LastIndex(loc, "/releases/tag/")
		if resp.StatusCode/100 != 3 || i < 0 {
			return "", fmt.Errorf("%s has no release yet (HTTP %d)", repo, resp.StatusCode)
		}
		t, err := url.PathUnescape(loc[i+len("/releases/tag/"):])
		if err != nil {
			return "", err
		}
		return strings.TrimPrefix(t, "v"), nil
	}
	api := strings.TrimRight(s.API, "/")
	if api == "" {
		api = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/repos/"+repo+"/releases?per_page=30", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := s.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub releases of %s: HTTP %d", repo, resp.StatusCode)
	}
	var rels []struct {
		Tag   string `json:"tag_name"`
		Draft bool   `json:"draft"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rels); err != nil {
		return "", err
	}
	for _, r := range rels {
		if !r.Draft {
			return strings.TrimPrefix(r.Tag, "v"), nil
		}
	}
	return "", fmt.Errorf("%s has no release yet", repo)
}

var versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$`)

// ValidVersion reports whether v is usable as a path segment.
func ValidVersion(v string) bool { return versionRe.MatchString(v) && !strings.Contains(v, "..") }

// open streams a release file.
func (s Source) open(ctx context.Context, rel string) (io.ReadCloser, error) {
	raw := strings.TrimRight(s.Base, "/") + "/" + rel
	if repo, ok := s.githubRepo(); ok {
		// <version>/<file> is the asset <file> of the release v<version>.
		version, file, _ := strings.Cut(rel, "/")
		raw = "https://github.com/" + repo + "/releases/download/" + tag(version) + "/" + file
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "file" {
		return os.Open(u.Path)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("release URL must be https://, http:// or file://")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", u.Redacted(), resp.StatusCode)
	}
	return resp.Body, nil
}

func (s Source) get(ctx context.Context, rel string, limit int64) ([]byte, error) {
	r, err := s.open(ctx, rel)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, limit))
}

// FetchFile downloads one file of a release to dst, checked against the
// release's SHA256SUMS (for large files such as image archives).
func (s Source) FetchFile(ctx context.Context, version, name, dst string) error {
	version = strings.TrimPrefix(version, "v")
	if !ValidVersion(version) || !ValidVersion(name) {
		return fmt.Errorf("invalid release file %q of %q", name, version)
	}
	sums, err := s.get(ctx, version+"/SHA256SUMS", 1<<20)
	if err != nil {
		return fmt.Errorf("release %s: %w", version, err)
	}
	want, ok := parseSums(sums)[name]
	if !ok {
		return fmt.Errorf("release %s has no %s", version, name)
	}
	r, err := s.open(ctx, version+"/"+name)
	if err != nil {
		return err
	}
	defer r.Close()
	f, err := os.CreateTemp(filepath.Dir(dst), ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return fmt.Errorf("%s: checksum mismatch", name)
	}
	return os.Rename(f.Name(), dst)
}

// Latest is the version a channel points at.
func (s Source) Latest(ctx context.Context, channel string) (string, error) {
	if channel == "" {
		channel = "stable"
	}
	if !ValidVersion(channel) {
		return "", fmt.Errorf("invalid channel %q", channel)
	}
	var v string
	if repo, ok := s.githubRepo(); ok {
		var err error
		if v, err = s.githubLatest(ctx, repo, channel); err != nil {
			return "", err
		}
	} else {
		b, err := s.get(ctx, "channels/"+channel, 256)
		if err != nil {
			return "", err
		}
		v = strings.TrimSpace(string(b))
	}
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
	version = strings.TrimPrefix(version, "v")
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
