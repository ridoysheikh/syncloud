// Package gitremote reads a remote repository's refs over HTTPS (§5.8
// polling): the equivalent of `git ls-remote`, without a git binary or a
// clone. It speaks the smart HTTP protocol and falls back to dumb HTTP.
package gitremote

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

var client = &http.Client{Timeout: 20 * time.Second}

// SelfSignedCA returns the certificate of a platform host (the built-in Git
// server) while it is self-signed, so polling it works on private networks;
// nil or "" = system roots only.
var SelfSignedCA func(host string) string

func clientFor(u *url.URL) *http.Client {
	if SelfSignedCA == nil || u.Scheme != "https" {
		return client
	}
	ca := SelfSignedCA(u.Hostname())
	if ca == "" {
		return client
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	pool.AppendCertsFromPEM([]byte(ca))
	return &http.Client{Timeout: client.Timeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, Proxy: http.ProxyFromEnvironment}}
}

// ValidateURL accepts http(s) repository URLs without embedded credentials.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("repository URL must be https://host/owner/repo(.git)")
	}
	if u.User != nil {
		return errors.New("put credentials in the token field, not the URL")
	}
	return nil
}

// LsRemote returns ref name -> commit SHA.
func LsRemote(ctx context.Context, repoURL, token string) (map[string]string, error) {
	base := strings.TrimSuffix(repoURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/info/refs?service=git-upload-pack", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "git/2.45 syncloud")
	if token != "" {
		req.SetBasicAuth("syncloud", token) // GitHub, GitLab and Gitea accept a token as the password
	}
	resp, err := clientFor(req.URL).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return nil, fmt.Errorf("repository not found or not accessible (HTTP %d): check the URL and token", resp.StatusCode)
	default:
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, req.URL.Host)
	}
	r := bufio.NewReaderSize(io.LimitReader(resp.Body, 32<<20), 64<<10)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/x-git-upload-pack-advertisement") {
		return parseSmart(r)
	}
	return parseDumb(r)
}

// parseSmart reads pkt-lines: "# service=…", flush, then "<sha> <ref>[\0caps]".
func parseSmart(r *bufio.Reader) (map[string]string, error) {
	refs := map[string]string{}
	for {
		line, flush, err := readPkt(r)
		if errors.Is(err, io.EOF) {
			return refs, nil
		} else if err != nil {
			return nil, err
		}
		if flush || strings.HasPrefix(line, "#") {
			continue
		}
		line, _, _ = strings.Cut(line, "\x00")
		sha, ref, ok := strings.Cut(strings.TrimRight(line, "\n"), " ")
		if ok && len(sha) == 40 {
			refs[ref] = sha
		}
	}
}

func readPkt(r *bufio.Reader) (string, bool, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", false, err
	}
	n, err := strconv.ParseUint(string(hdr[:]), 16, 16)
	if err != nil {
		return "", false, fmt.Errorf("bad pkt-line length %q", hdr[:])
	}
	if n == 0 {
		return "", true, nil
	}
	if n < 4 {
		return "", false, fmt.Errorf("bad pkt-line length %d", n)
	}
	buf := make([]byte, n-4)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", false, err
	}
	return string(buf), false, nil
}

// parseDumb reads "<sha>\t<ref>" lines (a static info/refs file).
func parseDumb(r *bufio.Reader) (map[string]string, error) {
	refs := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		sha, ref, ok := strings.Cut(sc.Text(), "\t")
		if ok && len(sha) == 40 {
			refs[ref] = sha
		}
	}
	return refs, sc.Err()
}

// BranchSHA returns the commit of a branch.
func BranchSHA(refs map[string]string, branch string) (string, bool) {
	sha, ok := refs["refs/heads/"+branch]
	return sha, ok
}

// ValidatePattern checks a branch or tag pattern: a name, optionally with
// globs ("release/*", "v*").
func ValidatePattern(p string) error {
	if p == "" || strings.ContainsAny(p, " \\'\"`$;&|\n#:~^") || strings.Contains(p, "..") || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return fmt.Errorf("invalid branch or tag pattern %q", p)
	}
	if _, err := path.Match(p, ""); err != nil {
		return fmt.Errorf("invalid pattern %q: %w", p, err)
	}
	return nil
}

// IsPattern reports whether p has glob characters.
func IsPattern(p string) bool { return strings.ContainsAny(p, "*?[") }

// Match returns the refs whose branch matches branches or whose tag matches
// tags ("" = none), as full ref name -> commit. Annotated tags resolve to
// the commit they point at.
func Match(refs map[string]string, branches, tags string) map[string]string {
	out := map[string]string{}
	for ref, sha := range refs {
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok && branches != "" {
			if m, _ := path.Match(branches, name); m {
				out[ref] = sha
			}
		}
		if name, ok := strings.CutPrefix(ref, "refs/tags/"); ok && tags != "" && !strings.HasSuffix(name, "^{}") {
			if m, _ := path.Match(tags, name); m {
				if peeled, ok := refs[ref+"^{}"]; ok {
					sha = peeled
				}
				out[ref] = sha
			}
		}
	}
	return out
}

// ShortRef turns refs/heads/main into main and refs/tags/v1 into v1.
func ShortRef(ref string) string {
	if s, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return s
	}
	return strings.TrimPrefix(ref, "refs/tags/")
}
