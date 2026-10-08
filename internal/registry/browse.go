package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Browser reads the registry's contents for the dashboard (§5.10), with
// tokens the controller issues to itself.
type Browser struct {
	URL    string // e.g. http://127.0.0.1:5000
	Issuer *Issuer
	HTTP   *http.Client
}

// Repository is one image repository.
type Repository struct {
	Name string `json:"name"`
	Tags int    `json:"tags"`
}

// Image is one tag with its manifest details.
type Image struct {
	Tag       string     `json:"tag"`
	Digest    string     `json:"digest"`
	MediaType string     `json:"mediaType"`
	SizeBytes int64      `json:"sizeBytes"` // compressed layers + config of the first platform
	Platforms []string   `json:"platforms"`
	Created   *time.Time `json:"created"`
}

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// BrowserSubject is the token subject the controller reads the registry as.
const BrowserSubject = "syncloud-controller"

// ErrUnavailable means the registry could not be reached.
var ErrUnavailable = errors.New("registry unavailable")

func (b *Browser) do(ctx context.Context, method, path, accept string, access []Access) (*http.Response, error) {
	tok, err := b.Issuer.Issue(BrowserSubject, access, time.Now())
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, b.URL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	c := b.HTTP
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrManifestNotFound, strings.TrimSpace(string(msg)))
		}
		return nil, fmt.Errorf("registry: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

func repoAccess(repo string, actions ...string) []Access {
	return []Access{{Type: "repository", Name: repo, Actions: actions}}
}

// Repositories lists repositories with their tag counts.
func (b *Browser) Repositories(ctx context.Context) ([]Repository, error) {
	var names []string
	next := "/v2/_catalog?n=1000"
	for page := 0; next != "" && page < 100; page++ {
		resp, err := b.do(ctx, http.MethodGet, next, "", []Access{{Type: "registry", Name: "catalog", Actions: []string{"*"}}})
		if err != nil {
			return nil, err
		}
		var cat struct {
			Repositories []string `json:"repositories"`
		}
		err = json.NewDecoder(resp.Body).Decode(&cat)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		names = append(names, cat.Repositories...)
		next = nextLink(resp.Header.Get("Link"))
	}
	out := make([]Repository, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		out[i].Name = name
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tags, err := b.tags(ctx, name); err == nil {
				out[i].Tags = len(tags)
			}
		}()
	}
	wg.Wait()
	// Repositories whose tags were all deleted still show in the catalog.
	kept := out[:0]
	for _, r := range out {
		if r.Tags > 0 {
			kept = append(kept, r)
		}
	}
	return kept, nil
}

func (b *Browser) tags(ctx context.Context, repo string) ([]string, error) {
	resp, err := b.do(ctx, http.MethodGet, "/v2/"+repo+"/tags/list", "", repoAccess(repo, "pull"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var t struct {
		Tags []string `json:"tags"`
	}
	err = json.NewDecoder(resp.Body).Decode(&t)
	return t.Tags, err
}

type manifest struct {
	MediaType string `json:"mediaType"`
	Config    struct {
		Size   int64  `json:"size"`
		Digest string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		Size int64 `json:"size"`
	} `json:"layers"`
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`
}

func (b *Browser) manifest(ctx context.Context, repo, ref string) (manifest, string, string, error) {
	resp, err := b.do(ctx, http.MethodGet, "/v2/"+repo+"/manifests/"+ref, manifestAccept, repoAccess(repo, "pull"))
	if err != nil {
		return manifest{}, "", "", err
	}
	defer resp.Body.Close()
	var m manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&m); err != nil {
		return m, "", "", err
	}
	mt := resp.Header.Get("Content-Type")
	if m.MediaType == "" {
		m.MediaType = mt
	}
	return m, resp.Header.Get("Docker-Content-Digest"), mt, nil
}

// ErrManifestNotFound means the registry has no such repository, tag or blob.
var ErrManifestNotFound = errors.New("not found")

// HasImage reports whether repo has a manifest for ref (a tag or digest).
func (b *Browser) HasImage(ctx context.Context, repo, ref string) (bool, error) {
	resp, err := b.do(ctx, http.MethodHead, "/v2/"+repo+"/manifests/"+ref, manifestAccept, repoAccess(repo, "pull"))
	if errors.Is(err, ErrManifestNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	resp.Body.Close()
	return true, nil
}

// Images lists a repository's tags, newest first.
func (b *Browser) Images(ctx context.Context, repo string) ([]Image, error) {
	tags, err := b.tags(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := make([]Image, len(tags))
	var wg sync.WaitGroup
	for i, tag := range tags {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = b.image(ctx, repo, tag)
		}()
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool {
		a, c := out[i].Created, out[j].Created
		switch {
		case a != nil && c != nil && !a.Equal(*c):
			return a.After(*c)
		case (a != nil) != (c != nil):
			return a != nil
		}
		return out[i].Tag > out[j].Tag
	})
	return out, nil
}

func (b *Browser) image(ctx context.Context, repo, tag string) Image {
	img := Image{Tag: tag, Platforms: []string{}}
	m, digest, mt, err := b.manifest(ctx, repo, tag)
	if err != nil {
		return img
	}
	img.Digest, img.MediaType = digest, mt
	if len(m.Manifests) > 0 { // multi-platform index: describe the first real platform
		for _, c := range m.Manifests {
			if c.Platform.OS != "unknown" && c.Platform.OS != "" {
				img.Platforms = append(img.Platforms, c.Platform.OS+"/"+c.Platform.Architecture)
			}
		}
		for _, c := range m.Manifests {
			if c.Platform.OS == "linux" {
				if child, _, _, err := b.manifest(ctx, repo, c.Digest); err == nil {
					m = child
				}
				break
			}
		}
	}
	img.SizeBytes = m.Config.Size
	for _, l := range m.Layers {
		img.SizeBytes += l.Size
	}
	if m.Config.Digest != "" {
		if resp, err := b.do(ctx, http.MethodGet, "/v2/"+repo+"/blobs/"+m.Config.Digest, "", repoAccess(repo, "pull")); err == nil {
			var cfg struct {
				Created      *time.Time `json:"created"`
				OS           string     `json:"os"`
				Architecture string     `json:"architecture"`
			}
			if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&cfg) == nil {
				img.Created = cfg.Created
				if len(img.Platforms) == 0 && cfg.OS != "" {
					img.Platforms = append(img.Platforms, cfg.OS+"/"+cfg.Architecture)
				}
			}
			resp.Body.Close()
		}
	}
	return img
}

// DeleteTag removes a tag's manifest. Other tags pointing at the same digest
// go with it; disk space is reclaimed by registry garbage collection.
func (b *Browser) DeleteTag(ctx context.Context, repo, tag string) error {
	resp, err := b.do(ctx, http.MethodHead, "/v2/"+repo+"/manifests/"+tag, manifestAccept, repoAccess(repo, "pull"))
	if err != nil {
		return err
	}
	resp.Body.Close()
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return errors.New("registry did not return the manifest digest")
	}
	resp, err = b.do(ctx, http.MethodDelete, "/v2/"+repo+"/manifests/"+url.PathEscape(digest), "", repoAccess(repo, "delete"))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// nextLink extracts the path of a `Link: <…>; rel="next"` header.
func nextLink(h string) string {
	start, end := strings.IndexByte(h, '<'), strings.IndexByte(h, '>')
	if start < 0 || end < start || !strings.Contains(h, `rel="next"`) {
		return ""
	}
	return h[start+1 : end]
}
