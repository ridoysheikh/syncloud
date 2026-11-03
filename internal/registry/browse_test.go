package registry

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUsageUniqueBytesAndRetag(t *testing.T) {
	is, err := LoadOrCreateIssuer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := newFake()
	var manifests []int // size of every manifest document, to total the registry by hand
	image := func(repo, tag string, layers ...string) (digest string) {
		cfg := f.putBlob(repo, []byte(`{"created":"2026-10-13T00:00:00Z","os":"linux","architecture":"amd64","history":[{"created_by":"/bin/sh -c #(nop) ADD base"},{"created_by":"RUN /bin/sh -c make # buildkit"},{"created_by":"ENV A=1","empty_layer":true}]}`+tag))
		m := ociManifest{SchemaVersion: 2, MediaType: mediaManifest, Config: cfg}
		for _, l := range layers {
			m.Layers = append(m.Layers, f.putBlob(repo, []byte(l)))
		}
		raw, _ := json.Marshal(m)
		manifests = append(manifests, len(raw))
		return f.putManifest(repo, tag, mediaManifest, raw)
	}
	image("shop/api", "aaaaaaaaaaaa", strings.Repeat("b", 100), strings.Repeat("1", 10))
	d2 := image("shop/api", "bbbbbbbbbbbb", strings.Repeat("b", 100), strings.Repeat("2", 20))
	f.tags["shop/api"]["latest-main"] = d2 // the branch tag points at the newest build
	image("shop/web", "latest", strings.Repeat("b", 100), strings.Repeat("w", 7))
	// The builders' cache: an index of layers, one shared with an image.
	cacheIdx, _ := json.Marshal(ociIndex{SchemaVersion: 2, MediaType: mediaIndex, Manifests: []Descriptor{
		f.putBlob("shop/api", []byte(strings.Repeat("c", 1000))),
		f.putBlob("shop/api", []byte(strings.Repeat("2", 20))),
	}})
	f.putManifest("shop/api", CacheTag, mediaIndex, cacheIdx)

	srv := httptest.NewServer(f)
	defer srv.Close()
	b := &Browser{URL: srv.URL, Issuer: is}
	ctx := context.Background()

	imgs, err := b.Images(ctx, "shop/api")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Image{}
	for _, i := range imgs {
		by[i.Tag] = i
	}
	if !by[CacheTag].Cache || by[CacheTag].SizeBytes != 1020+int64(len(cacheIdx)) {
		t.Errorf("cache: %+v", by[CacheTag])
	}
	// aaaa owns its config and its 10-byte layer; the 100-byte base layer is
	// shared with bbbb, so deleting aaaa frees only what it alone uses.
	cfgSize := func(tag string) int64 {
		return int64(len(`{"created":"2026-10-13T00:00:00Z","os":"linux","architecture":"amd64","history":[{"created_by":"/bin/sh -c #(nop) ADD base"},{"created_by":"RUN /bin/sh -c make # buildkit"},{"created_by":"ENV A=1","empty_layer":true}]}` + tag))
	}
	if want := cfgSize("aaaaaaaaaaaa") + 10; by["aaaaaaaaaaaa"].UniqueBytes != want {
		t.Errorf("aaaa unique = %d, want %d", by["aaaaaaaaaaaa"].UniqueBytes, want)
	}
	// Two tags of one digest are one image: nothing is "shared" between them.
	// Its 20-byte layer is also in the build cache, so deleting the image
	// alone would not free it.
	if by["bbbbbbbbbbbb"].UniqueBytes != by["latest-main"].UniqueBytes || by["latest-main"].UniqueBytes != cfgSize("bbbbbbbbbbbb") {
		t.Errorf("bbbb/latest-main unique = %d/%d", by["bbbbbbbbbbbb"].UniqueBytes, by["latest-main"].UniqueBytes)
	}

	u, err := b.Usage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rep := map[string]RepoUsage{}
	for _, r := range u.Repositories {
		rep[r.Name] = r
	}
	api := rep["shop/api"]
	if api.Images != 2 || api.Tags != 4 {
		t.Errorf("shop/api images=%d tags=%d", api.Images, api.Tags)
	}
	// The base layer is held by both repositories: shared, stored once.
	if api.SharedBytes != 100 || rep["shop/web"].SharedBytes != 100 {
		t.Errorf("shared = %d/%d, want 100", api.SharedBytes, rep["shop/web"].SharedBytes)
	}
	// Only the cache holds the 1000-byte layer and the cache index itself.
	if api.CacheBytes < 1000 || api.CacheBytes >= 1000+int64(len(cacheIdx))+1 {
		t.Errorf("cache bytes = %d", api.CacheBytes)
	}
	var sum int64
	for _, r := range u.Repositories {
		sum += r.UniqueBytes
	}
	if u.ReferencedBytes != sum+100 { // the shared base layer counts once
		t.Errorf("referenced = %d, unique sum + shared = %d", u.ReferencedBytes, sum+100)
	}

	// Retag: the same manifest under a new tag, then both resolve alike.
	if err := b.Retag(ctx, "shop/api", "bbbbbbbbbbbb", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if f.tags["shop/api"]["v1.0.0"] != d2 {
		t.Errorf("v1.0.0 -> %s, want %s", f.tags["shop/api"]["v1.0.0"], d2)
	}

	in, err := b.Inspect(ctx, "shop/api", "bbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Layers) != 2 || in.Layers[0].Command != "ADD base" || in.Layers[1].Command != "RUN make" {
		t.Errorf("layer steps: %+v", in.Layers)
	}
	if in.Platform != "linux/amd64" || in.TotalBytes == 0 {
		t.Errorf("inspect: %+v", in)
	}
}

func TestDeleteRepository(t *testing.T) {
	is, err := LoadOrCreateIssuer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := newFake()
	image := func(repo, tag, layer string) string {
		cfg := f.putBlob(repo, []byte(`{"os":"linux","architecture":"amd64"}`+tag))
		m := ociManifest{SchemaVersion: 2, MediaType: mediaManifest, Config: cfg, Layers: []Descriptor{f.putBlob(repo, []byte(layer))}}
		raw, _ := json.Marshal(m)
		return f.putManifest(repo, tag, mediaManifest, raw)
	}
	image("shop/api", "aaaaaaaaaaaa", "one")
	d2 := image("shop/api", "bbbbbbbbbbbb", "two")
	f.tags["shop/api"]["latest-main"] = d2 // two tags, one digest
	image("shop/web", "latest", "web")

	srv := httptest.NewServer(f)
	defer srv.Close()
	b := &Browser{URL: srv.URL, Issuer: is}
	ctx := context.Background()

	n, err := b.DeleteRepository(ctx, "shop/api")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("removed %d tags, want 3", n)
	}
	repos, err := b.Repositories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Name != "shop/web" {
		t.Errorf("repositories after delete: %+v", repos)
	}
}
