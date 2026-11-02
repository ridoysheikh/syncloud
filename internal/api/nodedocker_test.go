package api

import (
	"context"
	"testing"

	"github.com/ridoysheikh/syncloud/internal/dockerusage"
)

func TestDockerView(t *testing.T) {
	u := dockerusage.Usage{
		LayersBytes: 1000, BuildCacheBytes: 50, BuildCacheCount: 1,
		Disk: dockerusage.Disk{Path: "/var/lib/docker", TotalBytes: 5000, UsedBytes: 2000, FreeBytes: 3000},
		Images: []dockerusage.Image{
			{ID: "i1", Tags: []string{"registry:3"}, Bytes: 600, SharedBytes: 100, Containers: 1},
			{ID: "i2", Bytes: 400, SharedBytes: 100, Containers: 0, Dangling: true},
			{ID: "i3", Tags: []string{"old:1"}, Bytes: 300, Containers: 0},
		},
		Containers: []dockerusage.Container{
			{ID: "c1", Name: "sys-registry", ImageID: "i1", State: "running", WritableBytes: 10, LogBytes: 90, System: true, Managed: true, Volumes: []string{"syncloud-registry"}},
			{ID: "c2", Name: "scratch", ImageID: "i3", State: "exited", WritableBytes: 20, LogBytes: 5},
		},
		Volumes: []dockerusage.Volume{
			{Name: "syncloud-registry", Bytes: 700, RefCount: 1},
			{Name: "orphan", Bytes: 40, RefCount: 0},
			{Name: "ab12", Bytes: 9, RefCount: 0, Anonymous: true},
		},
		BuildCache: []dockerusage.CacheRecord{{ID: "k", Bytes: 50}},
	}
	v := (&Server{}).dockerView(context.Background(), "nod_1", "n1", u)
	tt := v.Totals
	// Docker's footprint adds up; the rest of the used disk is not Docker's.
	if tt.DockerBytes != 1000+30+95+749+50 || tt.OtherBytes != 2000-tt.DockerBytes && tt.OtherBytes != 0 {
		t.Errorf("totals: %+v", tt)
	}
	r := v.Reclaimable
	// Untagged unused image: its own part only. Unused images include the tagged one.
	if r.DanglingImages != 300 || r.UnusedImages != 300+300 {
		t.Errorf("images reclaimable: %+v", r)
	}
	// Only the volume with a random name is bulk-prunable; the named orphan needs a deliberate delete.
	if r.AnonymousVolumes != 9 || r.UnusedVolumes != 49 {
		t.Errorf("volumes reclaimable: %+v", r)
	}
	if r.Containers != 25 || r.StoppedManaged != 0 || r.BuildCache != 50 {
		t.Errorf("containers/cache reclaimable: %+v", r)
	}
	var plat, other *dockerApp
	for i := range v.Apps {
		switch v.Apps[i].Kind {
		case "platform":
			plat = &v.Apps[i]
		case "other":
			other = &v.Apps[i]
		}
	}
	if plat == nil || plat.VolumeBytes != 700 || plat.ImageBytes != 600 || plat.ContainerBytes != 100 || plat.Running != 1 {
		t.Errorf("platform app: %+v", plat)
	}
	if other == nil || other.Name != "scratch" || other.Running != 0 {
		t.Errorf("other app: %+v", other)
	}
	if v.Volumes[0].Name != "syncloud-registry" || len(v.Volumes[0].Apps) != 1 || v.Volumes[0].Apps[0] != "sys-registry" {
		t.Errorf("volume owner: %+v", v.Volumes[0])
	}
}
