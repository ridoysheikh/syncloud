package sysimage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/registry"
)

// fakeRegistry is just enough of the registry API for a push.
type fakeRegistry struct {
	mu        sync.Mutex
	blobs     map[string][]byte
	manifests map[string][]byte
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case r.Method == http.MethodHead && strings.Contains(p, "/blobs/"):
		if _, ok := f.blobs[p[strings.LastIndex(p, "/")+1:]]; !ok {
			http.NotFound(w, r)
		}
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/blobs/uploads/"):
		w.Header().Set("Location", "http://registry.invalid"+p+"u1?_state=x")
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodPut && strings.Contains(p, "/blobs/uploads/"):
		b, _ := io.ReadAll(r.Body)
		d := r.URL.Query().Get("digest")
		sum := sha256.Sum256(b)
		if d != "sha256:"+hex.EncodeToString(sum[:]) || r.URL.Query().Get("_state") != "x" {
			http.Error(w, "digest mismatch", http.StatusBadRequest)
			return
		}
		f.blobs[d] = b
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPut && strings.Contains(p, "/manifests/"):
		b, _ := io.ReadAll(r.Body)
		var m struct {
			Layers []struct{ Digest string } `json:"layers"`
		}
		_ = json.Unmarshal(b, &m)
		for _, l := range m.Layers {
			if _, ok := f.blobs[l.Digest]; !ok {
				http.Error(w, "blob unknown", http.StatusBadRequest)
				return
			}
		}
		f.manifests[p] = b
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodHead && strings.Contains(p, "/manifests/"):
		if _, ok := f.manifests[p]; !ok {
			http.NotFound(w, r)
		}
	default:
		http.Error(w, "unexpected "+r.Method+" "+p, http.StatusBadRequest)
	}
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func digest(b []byte) string { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }

// dockerSaveLayout writes what `docker save` produces: an OCI layout with
// an uncompressed layer, plus Docker's own manifest.json.
func dockerSaveLayout(t *testing.T, dir string) {
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	body := bytes.Repeat([]byte("postgres "), 4096)
	_ = tw.WriteHeader(&tar.Header{Name: "usr/bin/postgres", Mode: 0o755, Size: int64(len(body))})
	_, _ = tw.Write(body)
	_ = tw.Close()
	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":["` + digest(layer.Bytes()) + `"]}}`)
	man := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + digest(config) + `","size":` + itoa(len(config)) + `},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"` + digest(layer.Bytes()) + `","size":` + itoa(layer.Len()) + `}]}`)
	for _, b := range [][]byte{layer.Bytes(), config, man} {
		writeFile(t, filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(digest(b), "sha256:")), b)
	}
	writeFile(t, filepath.Join(dir, "index.json"), []byte(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"`+digest(man)+`","size":`+itoa(len(man))+`}]}`))
	writeFile(t, filepath.Join(dir, "manifest.json"), []byte(`[]`))
	writeFile(t, filepath.Join(dir, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`))
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// tarGz packs dir like the release tool does.
func tarGz(t *testing.T, dir, file string) {
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		rel, _ := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		h, _ := tar.FileInfoHeader(info, "")
		h.Name = filepath.ToSlash(rel)
		_ = tw.WriteHeader(h)
		if info.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			_, _ = tw.Write(b)
		}
		return nil
	})
	_ = tw.Close()
	_ = zw.Close()
	_ = f.Close()
}

func TestPackPushAndSeed(t *testing.T) {
	layout := t.TempDir()
	dockerSaveLayout(t, layout)
	if err := registry.CompressLayout(layout, "18-r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(layout, "manifest.json")); err == nil {
		t.Fatal("Docker's manifest.json was kept")
	}
	idx, _ := os.ReadFile(filepath.Join(layout, "index.json"))
	if !strings.Contains(string(idx), `"18-r1"`) {
		t.Fatalf("index does not name the tag: %s", idx)
	}
	blobs, _ := os.ReadDir(filepath.Join(layout, "blobs", "sha256"))
	if len(blobs) != 3 {
		t.Fatalf("%d blobs after compression, want config, gzip layer and manifest", len(blobs))
	}

	dir := t.TempDir()
	tarGz(t, layout, filepath.Join(dir, Archive("syncloud-system/postgres", "18-r1", "amd64")))
	fake := &fakeRegistry{blobs: map[string][]byte{}, manifests: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	iss, err := registry.LoadOrCreateIssuer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Seeder{Registry: &registry.Browser{URL: srv.URL, Issuer: iss}, Version: "0.1.0", Dir: dir, Arch: "amd64"}
	ctx := context.Background()

	if err := s.Ensure(ctx, "postgres:18"); err != nil {
		t.Fatalf("an outside image is not ready: %v", err)
	}
	var p *Preparing
	if err := s.Ensure(ctx, "@registry/syncloud-system/postgres:18-r1"); !errors.As(err, &p) {
		t.Fatalf("first Ensure = %v, want *Preparing", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := s.Ensure(ctx, "@registry/syncloud-system/postgres:18-r1")
		if err == nil {
			break
		}
		if !errors.As(err, &p) || time.Now().After(deadline) {
			t.Fatalf("Ensure = %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(fake.manifests) != 1 || len(fake.blobs) != 2 {
		t.Fatalf("registry has %d manifests and %d blobs", len(fake.manifests), len(fake.blobs))
	}
	if _, err := os.Stat(filepath.Join(dir, Archive("syncloud-system/postgres", "18-r1", "amd64"))); err != nil {
		t.Fatal("an archive placed by hand was removed")
	}

	// A development build has no release to download from.
	dev := &Seeder{Registry: &registry.Browser{URL: srv.URL, Issuer: iss}, Version: "0.0.0-dev", Dir: t.TempDir(), Arch: "amd64"}
	_ = dev.Ensure(ctx, "@registry/syncloud-system/postgres:17-r2")
	for {
		err := dev.Ensure(ctx, "@registry/syncloud-system/postgres:17-r2")
		if err != nil && strings.Contains(err.Error(), "development build") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dev Ensure = %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
