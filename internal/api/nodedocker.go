package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/ridoysheikh/syncloud/internal/dockerusage"
	"github.com/ridoysheikh/syncloud/internal/nodedocker"
)

// dockerApp is one consumer of a node's Docker disk: a service, a platform
// component, or something running outside SynCloud.
type dockerApp struct {
	Kind        string `json:"kind"` // service | platform | managed | other
	Name        string `json:"name"`
	Project     string `json:"project,omitempty"`
	Environment string `json:"environment,omitempty"`
	Service     string `json:"service,omitempty"`
	Containers  int    `json:"containers"`
	Running     int    `json:"running"`
	// ContainerBytes are the containers' own layers and log files; Volumes
	// the volumes they mount; ImageBytes the images they run (shared layers
	// counted in full, so apps sharing a base image each show it).
	ContainerBytes int64 `json:"containerBytes"`
	VolumeBytes    int64 `json:"volumeBytes"`
	ImageBytes     int64 `json:"imageBytes"`
	Bytes          int64 `json:"bytes"`
}

type dockerContainer struct {
	dockerusage.Container
	App dockerApp `json:"app"`
}

type dockerVolume struct {
	dockerusage.Volume
	// UsedBy names the containers mounting it, Apps their owners.
	UsedBy []string `json:"usedBy"`
	Apps   []string `json:"apps"`
}

type dockerImage struct {
	dockerusage.Image
	Apps []string `json:"apps"`
}

type dockerTotals struct {
	// Docker's footprint, by kind.
	ImageBytes      int64 `json:"imageBytes"`
	ContainerBytes  int64 `json:"containerBytes"`
	LogBytes        int64 `json:"logBytes"`
	VolumeBytes     int64 `json:"volumeBytes"`
	BuildCacheBytes int64 `json:"buildCacheBytes"`
	DockerBytes     int64 `json:"dockerBytes"`
	// The disk Docker lives on; OtherBytes is used space Docker does not hold.
	DiskTotalBytes int64 `json:"diskTotalBytes"`
	DiskUsedBytes  int64 `json:"diskUsedBytes"`
	DiskFreeBytes  int64 `json:"diskFreeBytes"`
	OtherBytes     int64 `json:"otherBytes"`
}

// dockerReclaimable is what each bulk clean would free.
type dockerReclaimable struct {
	DanglingImages   int64 `json:"danglingImages"`   // untagged, unused
	UnusedImages     int64 `json:"unusedImages"`     // every image no container uses (includes dangling)
	Containers       int64 `json:"containers"`       // stopped containers outside SynCloud, with their logs
	StoppedManaged   int64 `json:"stoppedManaged"`   // SynCloud's own stopped containers (removed one by one)
	AnonymousVolumes int64 `json:"anonymousVolumes"` // unused volumes with a random name
	UnusedVolumes    int64 `json:"unusedVolumes"`    // every volume no container mounts (may hold data)
	BuildCache       int64 `json:"buildCache"`       // entries nothing is building with
}

type dockerView struct {
	Node            string                    `json:"node"`
	NodeName        string                    `json:"nodeName"`
	DockerVersion   string                    `json:"dockerVersion"`
	DiskPath        string                    `json:"diskPath"`
	Totals          dockerTotals              `json:"totals"`
	Reclaimable     dockerReclaimable         `json:"reclaimable"`
	Apps            []dockerApp               `json:"apps"`
	Containers      []dockerContainer         `json:"containers"`
	Images          []dockerImage             `json:"images"`
	Volumes         []dockerVolume            `json:"volumes"`
	BuildCache      []dockerusage.CacheRecord `json:"buildCache"`
	BuildCacheCount int                       `json:"buildCacheCount"`
	MeasuredAt      time.Time                 `json:"measuredAt"`
}

func (s *Server) nodeDockerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, nodedocker.ErrOffline):
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
	default:
		writeError(w, http.StatusBadGateway, CodeInternal, err.Error())
	}
}

// appOf names the app a container belongs to.
func (s *Server) appOf(ctx context.Context, c dockerusage.Container, cache map[string]dockerApp) dockerApp {
	if a, ok := cache[c.TaskID]; ok && c.TaskID != "" {
		return a
	}
	app := dockerApp{Kind: "other", Name: c.Name}
	switch {
	case c.System:
		app = dockerApp{Kind: "platform", Name: c.Name}
	case c.TaskID != "":
		app = dockerApp{Kind: "managed", Name: c.Name}
		if t, err := s.store.TaskByID(ctx, c.TaskID); err == nil {
			if sv, err := s.store.ServiceByID(ctx, t.ServiceID); err == nil {
				app = dockerApp{Kind: "service", Name: sv.Project + "/" + sv.Environment + "/" + sv.Name,
					Project: sv.Project, Environment: sv.Environment, Service: sv.Name}
			}
		}
		cache[c.TaskID] = app
	}
	return app
}

// handleNodeDocker shows what Docker holds on a node and which app each
// container, image and volume belongs to.
func (s *Server) handleNodeDocker(w http.ResponseWriter, r *http.Request) {
	if s.nodeDocker == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "node management is not enabled")
		return
	}
	n, ok := s.nodes.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such node")
		return
	}
	if !n.Connected {
		writeError(w, http.StatusConflict, CodeConflict, "the node is not connected")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	var u dockerusage.Usage
	if err := s.nodeDocker.Call(ctx, n.ID, dockerusage.OpUsage, struct{}{}, &u); err != nil {
		s.nodeDockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.dockerView(ctx, n.ID, n.Name, u))
}

func (s *Server) dockerView(ctx context.Context, nodeID, nodeName string, u dockerusage.Usage) dockerView {
	v := dockerView{Node: nodeID, NodeName: nodeName, DockerVersion: u.DockerVersion, DiskPath: u.Disk.Path, MeasuredAt: time.Now().UTC(),
		Apps: []dockerApp{}, Containers: []dockerContainer{}, Images: []dockerImage{}, Volumes: []dockerVolume{},
		BuildCache: u.BuildCache, BuildCacheCount: u.BuildCacheCount}
	cache := map[string]dockerApp{}
	imgByID := map[string]dockerusage.Image{}
	for _, im := range u.Images {
		imgByID[im.ID] = im
	}
	volByName := map[string]dockerusage.Volume{}
	for _, vo := range u.Volumes {
		volByName[vo.Name] = vo
	}

	apps := map[string]*dockerApp{}
	appImages := map[string]map[string]bool{}
	appVols := map[string]map[string]bool{}
	volOwners := map[string]map[string]bool{}
	volUsers := map[string][]string{}
	imgOwners := map[string]map[string]bool{}
	for _, c := range u.Containers {
		app := s.appOf(ctx, c, cache)
		key := app.Kind + "|" + app.Name
		a := apps[key]
		if a == nil {
			cp := app
			a = &cp
			apps[key] = a
			appImages[key], appVols[key] = map[string]bool{}, map[string]bool{}
		}
		a.Containers++
		if c.State == "running" {
			a.Running++
		}
		a.ContainerBytes += c.WritableBytes + c.LogBytes
		if !appImages[key][c.ImageID] {
			appImages[key][c.ImageID] = true
			a.ImageBytes += imgByID[c.ImageID].Bytes
		}
		if imgOwners[c.ImageID] == nil {
			imgOwners[c.ImageID] = map[string]bool{}
		}
		imgOwners[c.ImageID][app.Name] = true
		for _, vn := range c.Volumes {
			if !appVols[key][vn] {
				appVols[key][vn] = true
				a.VolumeBytes += volByName[vn].Bytes
			}
			if volOwners[vn] == nil {
				volOwners[vn] = map[string]bool{}
			}
			volOwners[vn][app.Name] = true
			volUsers[vn] = append(volUsers[vn], c.Name)
		}
		v.Containers = append(v.Containers, dockerContainer{Container: c, App: app})
		v.Totals.ContainerBytes += c.WritableBytes
		v.Totals.LogBytes += c.LogBytes
		if c.State != "running" {
			if c.Managed {
				v.Reclaimable.StoppedManaged += c.WritableBytes + c.LogBytes
			} else {
				v.Reclaimable.Containers += c.WritableBytes + c.LogBytes
			}
		}
	}
	for _, a := range apps {
		a.Bytes = a.ContainerBytes + a.VolumeBytes + a.ImageBytes
		v.Apps = append(v.Apps, *a)
	}
	sort.Slice(v.Apps, func(i, j int) bool { return v.Apps[i].Bytes > v.Apps[j].Bytes })
	sort.Slice(v.Containers, func(i, j int) bool {
		return v.Containers[i].WritableBytes+v.Containers[i].LogBytes > v.Containers[j].WritableBytes+v.Containers[j].LogBytes
	})

	for _, im := range u.Images {
		di := dockerImage{Image: im, Apps: setList(imgOwners[im.ID])}
		v.Images = append(v.Images, di)
		if im.Containers == 0 {
			own := max(im.Bytes-im.SharedBytes, 0)
			v.Reclaimable.UnusedImages += own
			if im.Dangling {
				v.Reclaimable.DanglingImages += own
			}
		}
	}
	sort.Slice(v.Images, func(i, j int) bool { return v.Images[i].Bytes > v.Images[j].Bytes })
	for _, vo := range u.Volumes {
		v.Volumes = append(v.Volumes, dockerVolume{Volume: vo, UsedBy: nonNil(volUsers[vo.Name]), Apps: setList(volOwners[vo.Name])})
		v.Totals.VolumeBytes += vo.Bytes
		if vo.RefCount == 0 {
			v.Reclaimable.UnusedVolumes += vo.Bytes
			if vo.Anonymous {
				v.Reclaimable.AnonymousVolumes += vo.Bytes
			}
		}
	}
	sort.Slice(v.Volumes, func(i, j int) bool { return v.Volumes[i].Bytes > v.Volumes[j].Bytes })
	for _, b := range u.BuildCache {
		if !b.InUse {
			v.Reclaimable.BuildCache += b.Bytes
		}
	}
	t := &v.Totals
	t.ImageBytes, t.BuildCacheBytes = u.LayersBytes, u.BuildCacheBytes
	t.DockerBytes = t.ImageBytes + t.ContainerBytes + t.LogBytes + t.VolumeBytes + t.BuildCacheBytes
	t.DiskTotalBytes, t.DiskUsedBytes, t.DiskFreeBytes = u.Disk.TotalBytes, u.Disk.UsedBytes, u.Disk.FreeBytes
	t.OtherBytes = max(t.DiskUsedBytes-t.DockerBytes, 0)
	return v
}

func setList(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// handleCleanNodeDocker removes Docker objects from a node: exactly the
// ones named, or a whole kind pruned (never SynCloud's own containers or
// named volumes in bulk).
func (s *Server) handleCleanNodeDocker(w http.ResponseWriter, r *http.Request) {
	if s.nodeDocker == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "node management is not enabled")
		return
	}
	n, ok := s.nodes.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such node")
		return
	}
	if !n.Connected {
		writeError(w, http.StatusConflict, CodeConflict, "the node is not connected")
		return
	}
	var req dockerusage.CleanRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	switch req.Kind {
	case dockerusage.KindImages, dockerusage.KindContainers, dockerusage.KindVolumes, dockerusage.KindBuildCache:
	default:
		writeError(w, http.StatusBadRequest, CodeBadRequest, "kind must be images, containers, volumes or buildcache")
		return
	}
	if len(req.IDs) > 500 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "at most 500 objects at once")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	var res dockerusage.CleanResult
	if err := s.nodeDocker.Call(ctx, n.ID, dockerusage.OpClean, req, &res); err != nil {
		s.nodeDockerError(w, err)
		return
	}
	if res.Errors == nil {
		res.Errors = []string{}
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "node:CleanDocker", "srn:syncloud:node/"+n.ID, map[string]any{"node": n.Name, "kind": req.Kind, "ids": req.IDs, "all": req.All,
		"removed": res.Removed, "reclaimedBytes": res.ReclaimedBytes, "errors": len(res.Errors)})
	writeJSON(w, http.StatusOK, res)
}
