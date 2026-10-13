package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeRegistry is an in-memory registry speaking the parts of the
// distribution API Copy uses.
type fakeRegistry struct {
	mu        sync.Mutex
	blobs     map[string]map[string][]byte // repo -> digest -> data
	manifests map[string]map[string][]byte // repo -> digest -> raw
	types     map[string]string            // digest -> media type
	tags      map[string]map[string]string // repo -> tag -> digest
	uploads   int
	mounts    int
}

func newFake() *fakeRegistry {
	return &fakeRegistry{blobs: map[string]map[string][]byte{}, manifests: map[string]map[string][]byte{},
		types: map[string]string{}, tags: map[string]map[string]string{}}
}

func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

func (f *fakeRegistry) putBlob(repo string, b []byte) Descriptor {
	if f.blobs[repo] == nil {
		f.blobs[repo] = map[string][]byte{}
	}
	d := digestOf(b)
	f.blobs[repo][d] = b
	return Descriptor{MediaType: mediaLayerGzip, Digest: d, Size: int64(len(b))}
}

func (f *fakeRegistry) putManifest(repo, tag, mt string, raw []byte) string {
	if f.manifests[repo] == nil {
		f.manifests[repo], f.tags[repo] = map[string][]byte{}, map[string]string{}
	}
	d := digestOf(raw)
	f.manifests[repo][d], f.types[d] = raw, mt
	if tag != "" {
		f.tags[repo][tag] = d
	}
	return d
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/v2/")
	if p == "_catalog" {
		var names []string
		for n := range f.tags {
			names = append(names, n)
		}
		sort.Strings(names)
		_ = json.NewEncoder(w).Encode(map[string]any{"repositories": names})
		return
	}
	for _, kind := range []string{"/tags/list", "/manifests/", "/blobs/uploads/", "/blobs/"} {
		i := strings.Index(p, kind)
		if i < 0 {
			continue
		}
		repo, ref := p[:i], p[i+len(kind):]
		switch kind {
		case "/tags/list":
			tags := []string{}
			for t := range f.tags[repo] {
				tags = append(tags, t)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})
		case "/manifests/":
			if r.Method == http.MethodPut {
				raw, _ := io.ReadAll(r.Body)
				tag := ref
				if strings.HasPrefix(ref, "sha256:") {
					tag = ""
				}
				w.Header().Set("Docker-Content-Digest", f.putManifest(repo, tag, r.Header.Get("Content-Type"), raw))
				w.WriteHeader(http.StatusCreated)
				return
			}
			d := ref
			if t, ok := f.tags[repo][ref]; ok {
				d = t
			}
			raw, ok := f.manifests[repo][d]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", f.types[d])
			w.Header().Set("Docker-Content-Digest", d)
			_, _ = w.Write(raw)
		case "/blobs/uploads/":
			if r.Method == http.MethodPost {
				if m, from := r.URL.Query().Get("mount"), r.URL.Query().Get("from"); m != "" {
					if b, ok := f.blobs[from][m]; ok {
						f.putBlob(repo, b)
						f.mounts++
						w.WriteHeader(http.StatusCreated)
						return
					}
				}
				w.Header().Set("Location", "/v2/"+repo+"/blobs/uploads/u1?state=x")
				w.WriteHeader(http.StatusAccepted)
				return
			}
			b, _ := io.ReadAll(r.Body)
			if digestOf(b) != r.URL.Query().Get("digest") {
				http.Error(w, "digest mismatch", http.StatusBadRequest)
				return
			}
			f.putBlob(repo, b)
			f.uploads++
			w.WriteHeader(http.StatusCreated)
		case "/blobs/":
			b, ok := f.blobs[repo][ref]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(b)))
			if r.Method == http.MethodGet {
				_, _ = w.Write(b)
			}
		}
		return
	}
	http.NotFound(w, r)
}

func TestCopy(t *testing.T) {
	is, err := LoadOrCreateIssuer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src, dst := newFake(), newFake()
	image := func(repo, tag string, layers ...string) (string, int) {
		cfg := src.putBlob(repo, []byte(`{"created":"2026-10-13T00:00:00Z","os":"linux","architecture":"amd64"}`+repo))
		m := ociManifest{SchemaVersion: 2, MediaType: mediaManifest, Config: cfg}
		for _, l := range layers {
			m.Layers = append(m.Layers, src.putBlob(repo, []byte(l)))
		}
		raw, _ := json.Marshal(m)
		return src.putManifest(repo, tag, mediaManifest, raw), len(raw)
	}
	image("shop/api", "v1", "base", "app-v1")
	image("shop/api", "v2", "base", "app-v2")
	image("shop/web", "latest", "base", "web")
	// An index whose child is untagged.
	child, size := image("multi/x", "", "base", "arm")
	idx, _ := json.Marshal(ociIndex{SchemaVersion: 2, MediaType: mediaIndex, Manifests: []Descriptor{{MediaType: mediaManifest, Digest: child, Size: int64(size)}}})
	idxDigest := src.putManifest("multi/x", "latest", mediaIndex, idx)

	s, d := httptest.NewServer(src), httptest.NewServer(dst)
	defer s.Close()
	defer d.Close()
	sb, db := &Browser{URL: s.URL, Issuer: is}, &Browser{URL: d.URL, Issuer: is}
	ctx := context.Background()
	p, err := Copy(ctx, sb, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Repositories != 3 || p.Images != 4 || p.ImagesDone != 4 {
		t.Fatalf("progress %+v", p)
	}
	if err := VerifyCopy(ctx, sb, db); err != nil {
		t.Fatal(err)
	}
	for repo, tags := range src.tags {
		for tag, digest := range tags {
			if dst.tags[repo][tag] != digest {
				t.Errorf("%s:%s copied as %s, want %s", repo, tag, dst.tags[repo][tag], digest)
			}
		}
	}
	if dst.tags["multi/x"]["latest"] != idxDigest || dst.manifests["multi/x"][child] == nil {
		t.Fatal("the index or its untagged child is missing")
	}
	// "base" is uploaded once and mounted into the other repositories.
	if dst.mounts != 2 {
		t.Errorf("mounts = %d, want 2", dst.mounts)
	}
	// A second copy uploads nothing.
	uploads := dst.uploads
	p, err = Copy(ctx, sb, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dst.uploads != uploads || p.Bytes != 0 {
		t.Fatalf("a repeated copy uploaded again: %+v", p)
	}
	// A target missing a tag fails verification.
	delete(dst.tags["shop/api"], "v2")
	if err := VerifyCopy(ctx, sb, db); err == nil {
		t.Fatal("verification passed with a missing tag")
	}
}
