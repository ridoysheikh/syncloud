package registry

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OCI image layouts (https://github.com/opencontainers/image-spec/blob/main/image-layout.md)
// carry platform images inside a release: `docker save` writes one, the
// release tool compresses its layers (CompressLayout), and the controller
// pushes it into its own registry (PushLayout), where nodes pull it.

const (
	mediaManifest  = "application/vnd.oci.image.manifest.v1+json"
	mediaIndex     = "application/vnd.oci.image.index.v1+json"
	mediaLayerTar  = "application/vnd.oci.image.layer.v1.tar"
	mediaLayerGzip = "application/vnd.oci.image.layer.v1.tar+gzip"
	dockerLayerTar = "application/vnd.docker.image.rootfs.diff.tar"
)

// SystemProject holds the platform's own images in the built-in registry
// (the release's PostgreSQL images). No project may take the name, and its
// images are neither expired nor deleted by hand.
const SystemProject = "syncloud-system"

// IsSystemRepo reports a repository of the platform's own images.
func IsSystemRepo(repo string) bool { return strings.HasPrefix(repo, SystemProject+"/") }

// Descriptor points at a blob of a layout.
type Descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Platform    map[string]any    `json:"platform,omitempty"`
}

type ociManifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        Descriptor   `json:"config"`
	Layers        []Descriptor `json:"layers"`
}

type ociIndex struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType,omitempty"`
	Manifests     []Descriptor `json:"manifests"`
}

func blobPath(dir, digest string) (string, error) {
	algo, hexsum, ok := strings.Cut(digest, ":")
	if !ok || algo != "sha256" || len(hexsum) != 64 || strings.ContainsAny(hexsum, "./\\") {
		return "", fmt.Errorf("unsupported digest %q", digest)
	}
	return filepath.Join(dir, "blobs", "sha256", hexsum), nil
}

// layoutManifest reads the single image manifest of a layout.
func layoutManifest(dir string) (Descriptor, ociManifest, error) {
	var idx ociIndex
	b, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return Descriptor{}, ociManifest{}, err
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		return Descriptor{}, ociManifest{}, fmt.Errorf("index.json: %w", err)
	}
	if len(idx.Manifests) != 1 || idx.Manifests[0].MediaType != mediaManifest {
		return Descriptor{}, ociManifest{}, errors.New("the layout must hold exactly one image manifest")
	}
	desc := idx.Manifests[0]
	p, err := blobPath(dir, desc.Digest)
	if err != nil {
		return Descriptor{}, ociManifest{}, err
	}
	mb, err := os.ReadFile(p)
	if err != nil {
		return Descriptor{}, ociManifest{}, err
	}
	var man ociManifest
	if err := json.Unmarshal(mb, &man); err != nil {
		return Descriptor{}, ociManifest{}, fmt.Errorf("manifest: %w", err)
	}
	return desc, man, nil
}

// writeBlob stores b in the layout and describes it.
func writeBlob(dir, mediaType string, b []byte) (Descriptor, error) {
	sum := sha256.Sum256(b)
	d := Descriptor{MediaType: mediaType, Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(b))}
	p, _ := blobPath(dir, d.Digest)
	return d, os.WriteFile(p, b, 0o644)
}

// CompressLayout rewrites a layout from `docker save` so its layers are
// gzip-compressed (registries and nodes then move a third of the bytes),
// and names the image ref. The config, and so the image ID, is unchanged.
func CompressLayout(dir, ref string) error {
	_, man, err := layoutManifest(dir)
	if err != nil {
		return err
	}
	for i, l := range man.Layers {
		if l.MediaType != mediaLayerTar && l.MediaType != dockerLayerTar {
			continue // already compressed
		}
		src, err := blobPath(dir, l.Digest)
		if err != nil {
			return err
		}
		d, err := gzipBlob(dir, src)
		if err != nil {
			return err
		}
		if err := os.Remove(src); err != nil {
			return err
		}
		man.Layers[i] = d
	}
	man.MediaType = mediaManifest
	mb, err := json.Marshal(man)
	if err != nil {
		return err
	}
	desc, err := writeBlob(dir, mediaManifest, mb)
	if err != nil {
		return err
	}
	desc.Annotations = map[string]string{"org.opencontainers.image.ref.name": ref}
	ib, err := json.Marshal(ociIndex{SchemaVersion: 2, MediaType: mediaIndex, Manifests: []Descriptor{desc}})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), ib, 0o644); err != nil {
		return err
	}
	// Docker's own files are not part of the layout and name old blobs.
	_ = os.Remove(filepath.Join(dir, "manifest.json"))
	_ = os.Remove(filepath.Join(dir, "repositories"))
	return pruneBlobs(dir, man, desc)
}

func gzipBlob(dir, src string) (Descriptor, error) {
	in, err := os.Open(src)
	if err != nil {
		return Descriptor{}, err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Join(dir, "blobs", "sha256"), ".gz-*")
	if err != nil {
		return Descriptor{}, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	zw, _ := gzip.NewWriterLevel(io.MultiWriter(tmp, h), gzip.BestCompression)
	if _, err := io.Copy(zw, in); err != nil {
		tmp.Close()
		return Descriptor{}, err
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		return Descriptor{}, err
	}
	st, err := tmp.Stat()
	if err != nil {
		tmp.Close()
		return Descriptor{}, err
	}
	if err := tmp.Close(); err != nil {
		return Descriptor{}, err
	}
	d := Descriptor{MediaType: mediaLayerGzip, Digest: "sha256:" + hex.EncodeToString(h.Sum(nil)), Size: st.Size()}
	p, _ := blobPath(dir, d.Digest)
	return d, os.Rename(tmp.Name(), p)
}

// pruneBlobs removes blobs the manifest no longer names.
func pruneBlobs(dir string, man ociManifest, desc Descriptor) error {
	keep := map[string]bool{desc.Digest: true, man.Config.Digest: true}
	for _, l := range man.Layers {
		keep[l.Digest] = true
	}
	entries, err := os.ReadDir(filepath.Join(dir, "blobs", "sha256"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !keep["sha256:"+e.Name()] {
			if err := os.Remove(filepath.Join(dir, "blobs", "sha256", e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// PushLayout uploads a layout's image to repo:tag. Blobs the registry has
// already are skipped, so pushing again is cheap.
func (b *Browser) PushLayout(ctx context.Context, repo, tag, dir string) error {
	_, man, err := layoutManifest(dir)
	if err != nil {
		return err
	}
	for _, d := range append([]Descriptor{man.Config}, man.Layers...) {
		if err := b.pushBlob(ctx, repo, dir, d); err != nil {
			return fmt.Errorf("blob %s: %w", d.Digest, err)
		}
	}
	mb, err := json.Marshal(man)
	if err != nil {
		return err
	}
	resp, err := b.send(ctx, http.MethodPut, "/v2/"+repo+"/manifests/"+tag, mediaManifest, bytes.NewReader(mb), int64(len(mb)), repo)
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	resp.Body.Close()
	return nil
}

func (b *Browser) pushBlob(ctx context.Context, repo, dir string, d Descriptor) error {
	if resp, err := b.send(ctx, http.MethodHead, "/v2/"+repo+"/blobs/"+d.Digest, "", nil, 0, repo); err == nil {
		resp.Body.Close()
		return nil // already there
	}
	resp, err := b.send(ctx, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", "", nil, 0, repo)
	if err != nil {
		return err
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.Header.Get("Location") == "" {
		return errors.New("the registry gave no upload location")
	}
	// The upload URL is the registry's own; keep only its path and query.
	q := loc.Query()
	q.Set("digest", d.Digest)
	path := loc.Path + "?" + q.Encode()
	p, err := blobPath(dir, d.Digest)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	resp, err = b.send(ctx, http.MethodPut, path, "application/octet-stream", f, d.Size, repo)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// send is a push-scoped request with a body (no client timeout: layers are
// hundreds of megabytes; ctx bounds it).
func (b *Browser) send(ctx context.Context, method, path, contentType string, body io.Reader, size int64, repo string) (*http.Response, error) {
	tok, err := b.Issuer.Issue(BrowserSubject, repoAccess(repo, "pull", "push"), time.Now())
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, b.URL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if body != nil {
		req.ContentLength = size
	}
	c := &http.Client{Transport: http.DefaultTransport}
	if b.HTTP != nil && b.HTTP.Transport != nil {
		c.Transport = b.HTTP.Transport
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("registry: %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}
