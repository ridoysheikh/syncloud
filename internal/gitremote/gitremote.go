// Package gitremote reads a remote repository's refs over HTTPS (§5.8
// polling): the equivalent of `git ls-remote`, without a git binary or a
// clone. It speaks the smart HTTP protocol and falls back to dumb HTTP.
package gitremote

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var client = &http.Client{Timeout: 20 * time.Second}

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
	resp, err := client.Do(req)
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
