package docker

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ridoysheikh/syncloud/internal/dockerusage"
)

func fakeDaemon(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return New(sock)
}

func TestUsage(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "c1-json.log")
	if err := os.WriteFile(logFile, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	anon := strings.Repeat("ab", 32)
	c := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/"+APIVersion) {
		case "/system/df":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"LayersSize": 900,
				"Images": []map[string]any{
					{"Id": "sha256:a", "RepoTags": []string{"app:1"}, "Size": 600, "SharedSize": 100, "Containers": 1},
					{"Id": "sha256:b", "RepoTags": []string{"<none>:<none>"}, "Size": 400, "SharedSize": 100, "Containers": 0},
				},
				"Containers": []map[string]any{
					{"Id": "c1", "Names": []string{"/web"}, "ImageID": "sha256:a", "State": "running", "SizeRw": 50,
						"Labels": map[string]string{"syncloud.task_id": "tsk_1", "syncloud.managed": "true"},
						"Mounts": []map[string]any{{"Type": "volume", "Name": "data"}, {"Type": "bind", "Name": ""}}},
				},
				"Volumes": []map[string]any{
					{"Name": "data", "Driver": "local", "UsageData": map[string]any{"Size": 300, "RefCount": 1}},
					{"Name": anon, "Driver": "local", "UsageData": map[string]any{"Size": -1, "RefCount": 0}},
				},
				"BuildCache": []map[string]any{{"ID": "k1", "Size": 70, "InUse": false}, {"ID": "k2", "Size": 30, "InUse": true}},
			})
		case "/info":
			_ = json.NewEncoder(w).Encode(map[string]any{"DockerRootDir": t.TempDir(), "ServerVersion": "27.0"})
		case "/containers/c1/json":
			_ = json.NewEncoder(w).Encode(map[string]any{"LogPath": logFile})
		default:
			http.NotFound(w, r)
		}
	})
	u, err := c.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Images) != 2 || !u.Images[1].Dangling || u.Images[0].Dangling || u.LayersBytes != 900 {
		t.Errorf("images: %+v", u.Images)
	}
	ct := u.Containers[0]
	if ct.Name != "web" || ct.TaskID != "tsk_1" || !ct.Managed || ct.WritableBytes != 50 || ct.LogBytes != 4096 || len(ct.Volumes) != 1 || ct.Volumes[0] != "data" {
		t.Errorf("container: %+v", ct)
	}
	if !u.Volumes[1].Anonymous || u.Volumes[1].Bytes != 0 || u.Volumes[0].Anonymous || u.Volumes[0].RefCount != 1 {
		t.Errorf("volumes: %+v", u.Volumes)
	}
	if u.BuildCacheBytes != 100 || u.BuildCacheCount != 2 || u.BuildCache[0].ID != "k1" {
		t.Errorf("cache: %+v", u.BuildCache)
	}
	if u.Disk.TotalBytes == 0 || u.DockerVersion != "27.0" {
		t.Errorf("disk/version: %+v %q", u.Disk, u.DockerVersion)
	}
}

func TestClean(t *testing.T) {
	var calls []string
	c := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/"+APIVersion)
		calls = append(calls, r.Method+" "+path+" "+r.URL.Query().Get("filters"))
		switch {
		case path == "/containers/prune":
			_ = json.NewEncoder(w).Encode(map[string]any{"ContainersDeleted": []string{"x"}, "SpaceReclaimed": 11})
		case path == "/images/prune":
			_ = json.NewEncoder(w).Encode(map[string]any{"ImagesDeleted": []map[string]string{{"Deleted": "sha256:z"}, {"Untagged": "t"}}, "SpaceReclaimed": 22})
		case path == "/volumes/prune":
			_ = json.NewEncoder(w).Encode(map[string]any{"VolumesDeleted": []string{"v"}, "SpaceReclaimed": 33})
		case r.Method == http.MethodDelete && path == "/images/busy":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"image is being used by running container c1"}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	ctx := context.Background()

	r, err := c.Clean(ctx, dockerusage.CleanRequest{Kind: dockerusage.KindContainers})
	if err != nil || r.Removed != 1 || r.ReclaimedBytes != 11 {
		t.Fatalf("containers: %+v %v", r, err)
	}
	// SynCloud's own containers are spared by the bulk prune.
	if !strings.Contains(calls[0], "syncloud.managed=true") || !strings.Contains(calls[0], `"label!"`) {
		t.Errorf("container prune filters: %s", calls[0])
	}
	r, _ = c.Clean(ctx, dockerusage.CleanRequest{Kind: dockerusage.KindImages})
	if !strings.Contains(calls[1], `"dangling":["true"]`) || r.Removed != 1 {
		t.Errorf("untagged images: %s %+v", calls[1], r)
	}
	c.Clean(ctx, dockerusage.CleanRequest{Kind: dockerusage.KindImages, All: true})
	if !strings.Contains(calls[2], `"dangling":["false"]`) {
		t.Errorf("all images: %s", calls[2])
	}
	// Volumes: the plain prune only, which Docker limits to anonymous ones.
	r, _ = c.Clean(ctx, dockerusage.CleanRequest{Kind: dockerusage.KindVolumes, All: true})
	if r.Removed != 1 || strings.Contains(calls[3], "all") {
		t.Errorf("volumes: %s %+v", calls[3], r)
	}
	// Named removals report what Docker refused, without failing the rest.
	r, err = c.Clean(ctx, dockerusage.CleanRequest{Kind: dockerusage.KindImages, IDs: []string{"busy", "free"}})
	if err != nil || r.Removed != 1 || len(r.Errors) != 1 || !strings.Contains(r.Errors[0], "being used") {
		t.Errorf("named: %+v %v", r, err)
	}
	if _, err := c.Clean(ctx, dockerusage.CleanRequest{Kind: "networks"}); err == nil {
		t.Error("unknown kind accepted")
	}
}
