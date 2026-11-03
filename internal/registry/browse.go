package registry

import (
	"bytes"
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
	// PushedAt is when the registry last received the image (set by callers
	// from the push events; nil when unknown).
	PushedAt *time.Time `json:"pushedAt"`
	// UniqueBytes is what deleting the tag would free within the repository:
	// layers no other image of it uses.
	UniqueBytes int64 `json:"uniqueBytes"`
	// Cache marks the builders' layer cache, not a runnable image.
	Cache bool `json:"cache"`

	blobs []Blob // config and layers of the described platform
}

// Blob is a content-addressed object of the registry.
type Blob struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// concurrency bounds the registry requests one listing makes at a time.
const concurrency = 12

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
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	} `json:"layers"`
	Manifests []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
		Platform  struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`

	size int64 // the manifest document's own size
}

func (b *Browser) manifest(ctx context.Context, repo, ref string) (manifest, string, string, error) {
	resp, err := b.do(ctx, http.MethodGet, "/v2/"+repo+"/manifests/"+ref, manifestAccept, repoAccess(repo, "pull"))
	if err != nil {
		return manifest{}, "", "", err
	}
	defer resp.Body.Close()
	var m manifest
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return m, "", "", err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, "", "", err
	}
	m.size = int64(len(raw))
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

// Digest is the manifest digest repo's ref (a tag) points to now.
func (b *Browser) Digest(ctx context.Context, repo, ref string) (string, error) {
	resp, err := b.do(ctx, http.MethodHead, "/v2/"+repo+"/manifests/"+ref, manifestAccept, repoAccess(repo, "pull"))
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	d := resp.Header.Get("Docker-Content-Digest")
	if d == "" {
		return "", fmt.Errorf("registry: no digest for %s:%s", repo, ref)
	}
	return d, nil
}

// Images lists a repository's tags, newest first.
func (b *Browser) Images(ctx context.Context, repo string) ([]Image, error) {
	tags, err := b.tags(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := make([]Image, len(tags))
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for i, tag := range tags {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = b.image(ctx, repo, tag)
		}()
	}
	wg.Wait()
	markUnique(out)
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

// markUnique sets each image's UniqueBytes: layers used by it and by no
// other digest of the repository (tags of one digest are one image).
func markUnique(imgs []Image) {
	owners := map[string]map[string]bool{} // blob → digests using it
	for _, img := range imgs {
		for _, bl := range img.blobs {
			if owners[bl.Digest] == nil {
				owners[bl.Digest] = map[string]bool{}
			}
			owners[bl.Digest][img.Digest] = true
		}
	}
	for i := range imgs {
		for _, bl := range imgs[i].blobs {
			if len(owners[bl.Digest]) == 1 {
				imgs[i].UniqueBytes += bl.Size
			}
		}
	}
}

func (b *Browser) image(ctx context.Context, repo, tag string) Image {
	img := Image{Tag: tag, Platforms: []string{}, Cache: IsCacheTag(tag)}
	m, digest, mt, err := b.manifest(ctx, repo, tag)
	if err != nil {
		return img
	}
	img.Digest, img.MediaType = digest, mt
	if img.Cache { // an index of cache layers, no platforms: its size is what the cache holds
		if blobs, err := b.footprint(ctx, repo, tag, 0); err == nil {
			for _, bl := range blobs {
				img.SizeBytes += bl.Size
			}
			img.blobs = blobs
		}
		return img
	}
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
	if m.Config.Digest != "" {
		img.blobs = append(img.blobs, Blob{m.Config.Digest, m.Config.Size})
	}
	for _, l := range m.Layers {
		img.SizeBytes += l.Size
		img.blobs = append(img.blobs, Blob{l.Digest, l.Size})
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
// go with it; disk space is reclaimed by registry garbage collection. The
// delete is checked: the image must be gone afterwards.
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
	switch {
	case err == nil:
		resp.Body.Close()
	case strings.Contains(err.Error(), "HTTP 405"):
		return errors.New("the registry refuses deletes: deletion is switched off in its storage configuration")
	case strings.Contains(err.Error(), "HTTP 401"), strings.Contains(err.Error(), "HTTP 403"):
		return fmt.Errorf("the registry denied the delete: %w", err)
	default:
		return err
	}
	if still, err := b.HasImage(ctx, repo, digest); err == nil && still {
		return errors.New("the registry accepted the delete but still serves the image")
	}
	return nil
}

// DeleteRepository removes every tag of a repository, the build cache
// included, and returns how many tags it removed. Tags sharing a digest go
// with the first one deleted, so a tag already gone is not an error. Layers
// stay on disk until registry garbage collection. A tag that cannot be
// deleted stops the run: the repository is only reported empty when it is.
func (b *Browser) DeleteRepository(ctx context.Context, repo string) (int, error) {
	tags, err := b.tags(ctx, repo)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, tag := range tags {
		err := b.DeleteTag(ctx, repo, tag)
		switch {
		case err == nil:
			removed++
		case errors.Is(err, ErrManifestNotFound):
			// Deleted along with another tag of the same digest.
			removed++
		default:
			return removed, fmt.Errorf("delete %s:%s: %w", repo, tag, err)
		}
	}
	if left, err := b.tags(ctx, repo); err == nil && len(left) > 0 {
		return removed, fmt.Errorf("the registry still lists %d tags of %s", len(left), repo)
	}
	return removed, nil
}

// Retag points tag at the manifest from (a tag or digest) names: the same
// image under another name, nothing copied.
func (b *Browser) Retag(ctx context.Context, repo, from, tag string) error {
	resp, err := b.do(ctx, http.MethodGet, "/v2/"+repo+"/manifests/"+url.PathEscape(from), manifestAccept, repoAccess(repo, "pull", "push"))
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	if err != nil {
		return err
	}
	resp, err = b.send(ctx, http.MethodPut, "/v2/"+repo+"/manifests/"+url.PathEscape(tag), resp.Header.Get("Content-Type"), bytes.NewReader(raw), int64(len(raw)), repo)
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

func isManifestType(mt string) bool {
	return strings.Contains(mt, "manifest") || strings.Contains(mt, "image.index")
}

// footprint lists the blobs ref (a tag or digest) holds in the registry,
// the manifest documents included and the first entry being ref's own
// manifest. Indexes are followed down to their platforms' manifests and
// attestations, so the sum is what deleting ref could free.
func (b *Browser) footprint(ctx context.Context, repo, ref string, depth int) ([]Blob, error) {
	m, digest, _, err := b.manifest(ctx, repo, ref)
	if err != nil {
		return nil, err
	}
	if digest == "" && strings.HasPrefix(ref, "sha256:") {
		digest = ref
	}
	out := []Blob{{digest, m.size}}
	if m.Config.Digest != "" {
		out = append(out, Blob{m.Config.Digest, m.Config.Size})
	}
	for _, l := range m.Layers {
		out = append(out, Blob{l.Digest, l.Size})
	}
	for _, c := range m.Manifests {
		if depth < 3 && isManifestType(c.MediaType) {
			if sub, err := b.footprint(ctx, repo, c.Digest, depth+1); err == nil {
				out = append(out, sub...)
				continue
			}
		}
		out = append(out, Blob{c.Digest, c.Size})
	}
	seen := map[string]bool{}
	uniq := out[:0]
	for _, bl := range out {
		if bl.Digest != "" && !seen[bl.Digest] {
			seen[bl.Digest] = true
			uniq = append(uniq, bl)
		}
	}
	return uniq, nil
}

// RepoUsage is what one repository holds in the registry's storage.
type RepoUsage struct {
	Name   string `json:"name"`
	System bool   `json:"system"` // the platform's own images
	Images int    `json:"images"` // distinct manifests behind the tags
	Tags   int    `json:"tags"`
	// Bytes counts each layer once, however many images use it.
	Bytes int64 `json:"bytes"`
	// UniqueBytes are layers no other repository uses; SharedBytes the rest.
	UniqueBytes int64 `json:"uniqueBytes"`
	SharedBytes int64 `json:"sharedBytes"`
	// CacheBytes is the part of Bytes only the builders' layer cache holds.
	CacheBytes int64 `json:"cacheBytes"`
}

// Usage is the registry's content measured through its API.
type Usage struct {
	Repositories []RepoUsage `json:"repositories"`
	// ReferencedBytes counts every layer reachable from a tag once; the
	// registry deduplicates layers across repositories.
	ReferencedBytes int64 `json:"referencedBytes"`
	CacheBytes      int64 `json:"cacheBytes"`
}

// Usage walks every tag of every repository.
func (b *Browser) Usage(ctx context.Context) (Usage, error) {
	repos, err := b.Repositories(ctx)
	if err != nil {
		return Usage{}, err
	}
	type repoAcc struct {
		RepoUsage
		digests map[string]bool
		blobs   map[string]bool // blob → held by a non-cache tag
	}
	accs := make([]*repoAcc, len(repos))
	sizes := map[string]int64{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for i, rp := range repos {
		acc := &repoAcc{RepoUsage: RepoUsage{Name: rp.Name, System: IsSystemRepo(rp.Name)}, digests: map[string]bool{}, blobs: map[string]bool{}}
		accs[i] = acc
		tags, err := b.tags(ctx, rp.Name)
		if err != nil {
			continue
		}
		acc.Tags = len(tags)
		for _, tag := range tags {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				blobs, err := b.footprint(ctx, rp.Name, tag, 0)
				if err != nil || len(blobs) == 0 {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				cache := IsCacheTag(tag)
				if !cache {
					acc.digests[blobs[0].Digest] = true
				}
				for _, bl := range blobs {
					sizes[bl.Digest] = bl.Size
					acc.blobs[bl.Digest] = acc.blobs[bl.Digest] || !cache
				}
			}()
		}
	}
	wg.Wait()
	holders := map[string]int{}
	global := map[string]bool{}
	for _, acc := range accs {
		for d, real := range acc.blobs {
			holders[d]++
			global[d] = global[d] || real
		}
	}
	var u Usage
	for d, real := range global {
		u.ReferencedBytes += sizes[d]
		if !real {
			u.CacheBytes += sizes[d]
		}
	}
	for _, acc := range accs {
		acc.Images = len(acc.digests)
		for d, real := range acc.blobs {
			acc.Bytes += sizes[d]
			if holders[d] == 1 {
				acc.UniqueBytes += sizes[d]
			}
			if !real {
				acc.CacheBytes += sizes[d]
			}
		}
		acc.SharedBytes = acc.Bytes - acc.UniqueBytes
		u.Repositories = append(u.Repositories, acc.RepoUsage)
	}
	sort.Slice(u.Repositories, func(i, j int) bool { return u.Repositories[i].Bytes > u.Repositories[j].Bytes })
	return u, nil
}

// Layer is one layer of an image, with the Dockerfile step that made it
// when the image's history records it.
type Layer struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
	Command   string `json:"command,omitempty"`
}

// PlatformImage is one platform of a multi-platform image.
type PlatformImage struct {
	Platform string `json:"platform"`
	Digest   string `json:"digest"`
	Size     int64  `json:"sizeBytes"`
}

// Inspection describes an image in depth.
type Inspection struct {
	Image
	Layers     []Layer           `json:"layers"`
	Platform   string            `json:"platform"` // the one Layers and the settings below describe
	Variants   []PlatformImage   `json:"variants"`
	Entrypoint []string          `json:"entrypoint"`
	Cmd        []string          `json:"cmd"`
	WorkingDir string            `json:"workingDir"`
	User       string            `json:"user"`
	Ports      []string          `json:"ports"`
	EnvNames   []string          `json:"envNames"` // names only: values may hold secrets
	Labels     map[string]string `json:"labels"`
	// TotalBytes covers every platform and attestation behind the tag.
	TotalBytes int64 `json:"totalBytes"`
}

var historyNoise = strings.NewReplacer("/bin/sh -c ", "", "#(nop) ", "", "# buildkit", "")

// Inspect reads a tag's manifest, layers and image settings.
func (b *Browser) Inspect(ctx context.Context, repo, tag string) (Inspection, error) {
	in := Inspection{Layers: []Layer{}, Variants: []PlatformImage{}, Ports: []string{}, EnvNames: []string{}, Labels: map[string]string{}}
	m, digest, mt, err := b.manifest(ctx, repo, tag)
	if err != nil {
		return in, err
	}
	in.Image = Image{Tag: tag, Digest: digest, MediaType: mt, Platforms: []string{}, Cache: IsCacheTag(tag)}
	if foot, err := b.footprint(ctx, repo, tag, 0); err == nil {
		for _, bl := range foot {
			in.TotalBytes += bl.Size
		}
	}
	if in.Cache {
		return in, nil
	}
	for _, c := range m.Manifests {
		if c.Platform.OS != "" && c.Platform.OS != "unknown" {
			p := c.Platform.OS + "/" + c.Platform.Architecture
			in.Platforms = append(in.Platforms, p)
			in.Variants = append(in.Variants, PlatformImage{Platform: p, Digest: c.Digest, Size: c.Size})
		}
	}
	for _, c := range m.Manifests {
		if c.Platform.OS == "linux" {
			if child, _, _, err := b.manifest(ctx, repo, c.Digest); err == nil {
				m = child
				in.Platform = "linux/" + c.Platform.Architecture
			}
			break
		}
	}
	in.SizeBytes = m.Config.Size
	for _, l := range m.Layers {
		in.SizeBytes += l.Size
		in.Layers = append(in.Layers, Layer{Digest: l.Digest, Size: l.Size, MediaType: l.MediaType})
	}
	if m.Config.Digest == "" {
		return in, nil
	}
	resp, err := b.do(ctx, http.MethodGet, "/v2/"+repo+"/blobs/"+m.Config.Digest, "", repoAccess(repo, "pull"))
	if err != nil {
		return in, nil
	}
	defer resp.Body.Close()
	var cfg struct {
		Created      *time.Time `json:"created"`
		OS           string     `json:"os"`
		Architecture string     `json:"architecture"`
		Config       struct {
			Entrypoint   []string            `json:"Entrypoint"`
			Cmd          []string            `json:"Cmd"`
			WorkingDir   string              `json:"WorkingDir"`
			User         string              `json:"User"`
			Env          []string            `json:"Env"`
			Labels       map[string]string   `json:"Labels"`
			ExposedPorts map[string]struct{} `json:"ExposedPorts"`
		} `json:"config"`
		History []struct {
			CreatedBy  string `json:"created_by"`
			EmptyLayer bool   `json:"empty_layer"`
		} `json:"history"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&cfg) != nil {
		return in, nil
	}
	in.Created, in.Entrypoint, in.Cmd = cfg.Created, cfg.Config.Entrypoint, cfg.Config.Cmd
	in.WorkingDir, in.User = cfg.Config.WorkingDir, cfg.Config.User
	if in.Platform == "" && cfg.OS != "" {
		in.Platform = cfg.OS + "/" + cfg.Architecture
	}
	for p := range cfg.Config.ExposedPorts {
		in.Ports = append(in.Ports, p)
	}
	sort.Strings(in.Ports)
	for _, e := range cfg.Config.Env {
		name, _, _ := strings.Cut(e, "=")
		in.EnvNames = append(in.EnvNames, name)
	}
	if cfg.Config.Labels != nil {
		in.Labels = cfg.Config.Labels
	}
	// History lists every Dockerfile step; those that are not empty_layer
	// made the layers, in order.
	var cmds []string
	for _, h := range cfg.History {
		if !h.EmptyLayer {
			cmds = append(cmds, strings.TrimSpace(historyNoise.Replace(h.CreatedBy)))
		}
	}
	if len(cmds) == len(in.Layers) {
		for i := range in.Layers {
			in.Layers[i].Command = cmds[i]
		}
	}
	return in, nil
}
