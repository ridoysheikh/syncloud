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
)

// CopyProgress describes a copy between two registries (Phase 19).
type CopyProgress struct {
	Repositories     int    `json:"repositories"`
	RepositoriesDone int    `json:"repositoriesDone"`
	Images           int    `json:"images"`
	ImagesDone       int    `json:"imagesDone"`
	Blobs            int    `json:"blobs"`   // blobs uploaded or mounted
	Skipped          int    `json:"skipped"` // blobs the target had already
	Bytes            int64  `json:"bytes"`   // bytes uploaded
	Current          string `json:"current,omitempty"`
}

// copyManifest is what a copy needs from a manifest or index.
type copyManifest struct {
	Config    *Descriptor  `json:"config"`
	Layers    []Descriptor `json:"layers"`
	Manifests []Descriptor `json:"manifests"`
}

// Copy copies every tagged image of src into dst: manifests byte for byte
// (digests stay the same), index children before their index, blobs
// streamed. Blobs dst has are skipped and blobs already copied to another
// repository are cross-mounted, so a failed copy resumes when retried.
// Untagged manifests are not copied. progress is called after each image.
func Copy(ctx context.Context, src, dst *Browser, progress func(CopyProgress)) (CopyProgress, error) {
	var p CopyProgress
	repos, err := src.Repositories(ctx)
	if err != nil {
		return p, fmt.Errorf("list repositories: %w", err)
	}
	tags := map[string][]string{}
	for _, r := range repos {
		ts, err := src.tags(ctx, r.Name)
		if err != nil {
			return p, fmt.Errorf("%s: list tags: %w", r.Name, err)
		}
		sort.Strings(ts)
		tags[r.Name] = ts
		p.Images += len(ts)
	}
	p.Repositories = len(repos)
	if progress != nil {
		progress(p)
	}
	c := copier{src: src, dst: dst, p: &p, mounted: map[string]string{}}
	for _, r := range repos {
		done := map[string]bool{} // manifests copied into this repository
		for _, tag := range tags[r.Name] {
			p.Current = r.Name + ":" + tag
			if err := c.manifest(ctx, r.Name, tag, done, 0); err != nil {
				return p, fmt.Errorf("%s:%s: %w", r.Name, tag, err)
			}
			p.ImagesDone++
			if progress != nil {
				progress(p)
			}
		}
		p.RepositoriesDone++
	}
	p.Current = ""
	if progress != nil {
		progress(p)
	}
	return p, nil
}

// VerifyCopy checks that dst lists every repository of src with at least
// as many tags. dst's catalog is read the way the registry walks its
// storage, so an S3 provider that lists objects wrongly is caught here,
// before the registry switches to it.
func VerifyCopy(ctx context.Context, src, dst *Browser) error {
	want, err := src.Repositories(ctx)
	if err != nil {
		return fmt.Errorf("list source repositories: %w", err)
	}
	return Verify(ctx, dst, want)
}

// Verify checks that b lists every repository of want with at least as
// many tags.
func Verify(ctx context.Context, b *Browser, want []Repository) error {
	got, err := b.Repositories(ctx)
	if err != nil {
		return fmt.Errorf("list copied repositories: %w", err)
	}
	have := map[string]int{}
	for _, r := range got {
		have[r.Name] = r.Tags
	}
	for _, r := range want {
		if have[r.Name] < r.Tags {
			return fmt.Errorf("the new storage lists %d of %d tags of %s after the copy: the S3 provider may not list objects correctly (try a bucket without a prefix)", have[r.Name], r.Tags, r.Name)
		}
	}
	return nil
}

type copier struct {
	src, dst *Browser
	p        *CopyProgress
	// mounted maps a blob digest to a dst repository that has it.
	mounted map[string]string
}

func (c *copier) manifest(ctx context.Context, repo, ref string, done map[string]bool, depth int) error {
	if depth > 4 {
		return errors.New("manifests nest too deeply")
	}
	if done[ref] {
		return nil // a child shared by two indexes, or a digest two tags point at
	}
	resp, err := c.src.do(ctx, http.MethodGet, "/v2/"+repo+"/manifests/"+ref, manifestAccept+", application/vnd.oci.artifact.manifest.v1+json", repoAccess(repo, "pull"))
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	if err != nil {
		return err
	}
	mt, digest := resp.Header.Get("Content-Type"), resp.Header.Get("Docker-Content-Digest")
	var m copyManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("manifest %s: %w", ref, err)
	}
	for _, child := range m.Manifests {
		if err := c.manifest(ctx, repo, child.Digest, done, depth+1); err != nil {
			return err
		}
	}
	blobs := m.Layers
	if m.Config != nil && m.Config.Digest != "" {
		blobs = append([]Descriptor{*m.Config}, blobs...)
	}
	for _, d := range blobs {
		if d.Digest == "" || len(d.URLs) > 0 {
			continue // foreign layers are not stored in registries
		}
		if err := c.blob(ctx, repo, d); err != nil {
			return fmt.Errorf("blob %s: %w", d.Digest, err)
		}
	}
	put, err := c.dst.send(ctx, http.MethodPut, "/v2/"+repo+"/manifests/"+ref, mt, bytes.NewReader(raw), int64(len(raw)), repo)
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	put.Body.Close()
	if got := put.Header.Get("Docker-Content-Digest"); digest != "" && got != "" && got != digest {
		return fmt.Errorf("the copy of %s has digest %s instead of %s", ref, got, digest)
	}
	done[digest] = true
	return nil
}

func (c *copier) blob(ctx context.Context, repo string, d Descriptor) error {
	if resp, err := c.dst.send(ctx, http.MethodHead, "/v2/"+repo+"/blobs/"+d.Digest, "", nil, 0, repo); err == nil {
		resp.Body.Close()
		c.p.Skipped++
		c.mounted[d.Digest] = repo
		return nil
	}
	var resp *http.Response
	var err error
	if from, ok := c.mounted[d.Digest]; ok && from != repo {
		q := url.Values{"mount": {d.Digest}, "from": {from}}
		resp, err = c.dst.sendAs(ctx, http.MethodPost, "/v2/"+repo+"/blobs/uploads/?"+q.Encode(), "", nil, 0,
			append(repoAccess(repo, "pull", "push"), repoAccess(from, "pull")...))
		if err == nil && resp.StatusCode == http.StatusCreated {
			resp.Body.Close()
			c.p.Blobs++
			c.mounted[d.Digest] = repo
			return nil
		}
		// 202: the registry started a normal upload instead.
	} else {
		resp, err = c.dst.send(ctx, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", "", nil, 0, repo)
	}
	if err != nil {
		return err
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.Header.Get("Location") == "" {
		return errors.New("the registry gave no upload location")
	}
	q := loc.Query()
	q.Set("digest", d.Digest)
	in, err := c.src.send(ctx, http.MethodGet, "/v2/"+repo+"/blobs/"+d.Digest, "", nil, 0, repo)
	if err != nil {
		return err
	}
	defer in.Body.Close()
	size := d.Size
	if in.ContentLength >= 0 {
		size = in.ContentLength
	}
	// The upload URL is the registry's own; keep only its path and query.
	out, err := c.dst.send(ctx, http.MethodPut, loc.Path+"?"+q.Encode(), "application/octet-stream", in.Body, size, repo)
	if err != nil {
		return err
	}
	out.Body.Close()
	c.p.Blobs++
	c.p.Bytes += size
	c.mounted[d.Digest] = repo
	return nil
}
