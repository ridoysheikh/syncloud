package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ridoysheikh/syncloud/internal/dockerusage"
)

var anonymousVolume = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Usage measures what Docker holds: images, containers (with their log
// files), volumes and build cache, and the disk they live on. It answers
// from endpoints that return at once; the sizes of volumes and of container
// layers, which take a directory walk, are measured in the background and
// show up as they finish (Usage.Pending counts the ones still running).
func (c *Client) Usage(ctx context.Context) (dockerusage.Usage, error) {
	var info struct {
		DockerRootDir string `json:"DockerRootDir"`
		ServerVersion string `json:"ServerVersion"`
	}
	if err := c.json(ctx, "GET", "/info", nil, nil, &info); err != nil {
		return dockerusage.Usage{}, err
	}
	var imgs []struct {
		ID         string   `json:"Id"`
		RepoTags   []string `json:"RepoTags"`
		Created    int64    `json:"Created"`
		Size       int64    `json:"Size"`
		SharedSize int64    `json:"SharedSize"`
		Containers int      `json:"Containers"`
	}
	if err := c.json(ctx, "GET", "/images/json", url.Values{"shared-size": {"1"}}, nil, &imgs); err != nil {
		return dockerusage.Usage{}, err
	}
	var cts []struct {
		ID      string   `json:"Id"`
		Names   []string `json:"Names"`
		Image   string   `json:"Image"`
		ImageID string   `json:"ImageID"`
		State   string   `json:"State"`
		Status  string   `json:"Status"`
		Created int64    `json:"Created"`
		Labels  map[string]string
		Mounts  []struct {
			Type string `json:"Type"`
			Name string `json:"Name"`
		} `json:"Mounts"`
	}
	if err := c.json(ctx, "GET", "/containers/json", url.Values{"all": {"1"}}, nil, &cts); err != nil {
		return dockerusage.Usage{}, err
	}
	var vols struct {
		Volumes []struct {
			Name       string `json:"Name"`
			Driver     string `json:"Driver"`
			Mountpoint string `json:"Mountpoint"`
			CreatedAt  string `json:"CreatedAt"`
		} `json:"Volumes"`
	}
	if err := c.json(ctx, "GET", "/volumes", nil, nil, &vols); err != nil {
		return dockerusage.Usage{}, err
	}

	u := dockerusage.Usage{DockerVersion: info.ServerVersion,
		Images: []dockerusage.Image{}, Containers: []dockerusage.Container{}, Volumes: []dockerusage.Volume{}, BuildCache: []dockerusage.CacheRecord{}}
	var uniq, sharedMax int64
	for _, im := range imgs {
		tags := []string{}
		for _, t := range im.RepoTags {
			if t != "<none>:<none>" {
				tags = append(tags, t)
			}
		}
		x := dockerusage.Image{ID: im.ID, Tags: tags, Created: im.Created, Bytes: max(im.Size, 0), SharedBytes: max(im.SharedSize, 0),
			Containers: max(im.Containers, 0), Dangling: len(tags) == 0}
		u.Images = append(u.Images, x)
		uniq += max(x.Bytes-x.SharedBytes, 0)
		sharedMax = max(sharedMax, x.SharedBytes)
	}
	u.LayersBytes = uniq + sharedMax // replaced below when Docker reports it
	live := map[string]bool{}
	refs := map[string]int{}
	for _, ct := range cts {
		name := ""
		if len(ct.Names) > 0 {
			name = strings.TrimPrefix(ct.Names[0], "/")
		}
		x := dockerusage.Container{ID: ct.ID, Name: name, Image: ct.Image, ImageID: ct.ImageID, State: ct.State, Status: ct.Status,
			Created: ct.Created, TaskID: ct.Labels["syncloud.task_id"],
			Managed: ct.Labels["syncloud.managed"] == "true", System: ct.Labels["syncloud.system"] == "true", Volumes: []string{}}
		for _, m := range ct.Mounts {
			if m.Type == "volume" && m.Name != "" {
				x.Volumes = append(x.Volumes, m.Name)
				refs[m.Name]++
			}
		}
		logPath, upper := c.layout(ctx, ct.ID)
		x.LogBytes = fileSize(logPath)
		if upper != "" {
			live["c:"+ct.ID] = true
			n, known := c.sizer.get("c:"+ct.ID, upper)
			x.WritableBytes, x.Measuring = n, !known
			if !known {
				u.Pending++
			}
		}
		u.Containers = append(u.Containers, x)
	}
	for _, v := range vols.Volumes {
		x := dockerusage.Volume{Name: v.Name, Driver: v.Driver, RefCount: refs[v.Name], Anonymous: anonymousVolume.MatchString(v.Name)}
		if t, err := time.Parse(time.RFC3339, v.CreatedAt); err == nil {
			x.Created = t.Unix()
		}
		if v.Driver == "local" && v.Mountpoint != "" {
			live["v:"+v.Name] = true
			n, known := c.sizer.get("v:"+v.Name, v.Mountpoint)
			x.Bytes, x.Measuring = n, !known
			if !known {
				u.Pending++
			}
		}
		u.Volumes = append(u.Volumes, x)
	}
	c.sizer.forget(live)

	// Build cache and the exact layer total come from df, restricted to the
	// two kinds that are quick to compute; a slow answer is not waited for.
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var df struct {
		LayersSize int64 `json:"LayersSize"`
		BuildCache []struct {
			ID          string `json:"ID"`
			Type        string `json:"Type"`
			Description string `json:"Description"`
			InUse       bool   `json:"InUse"`
			Shared      bool   `json:"Shared"`
			Size        int64  `json:"Size"`
			LastUsedAt  string `json:"LastUsedAt"`
		} `json:"BuildCache"`
	}
	if c.json(dctx, "GET", "/system/df", url.Values{"type": {"image", "build-cache"}}, nil, &df) == nil {
		if df.LayersSize > 0 {
			u.LayersBytes = df.LayersSize
		}
		for _, b := range df.BuildCache {
			x := dockerusage.CacheRecord{ID: b.ID, Type: b.Type, Description: b.Description, Bytes: max(b.Size, 0), InUse: b.InUse, Shared: b.Shared}
			if t, err := time.Parse(time.RFC3339Nano, b.LastUsedAt); err == nil {
				x.LastUsed = t.Unix()
			}
			u.BuildCache = append(u.BuildCache, x)
		}
	}
	// Thousands of cache entries would not fit the stream's message limit:
	// list the largest, total all.
	u.BuildCacheCount = len(u.BuildCache)
	for _, b := range u.BuildCache {
		u.BuildCacheBytes += b.Bytes
	}
	sort.Slice(u.BuildCache, func(i, j int) bool { return u.BuildCache[i].Bytes > u.BuildCache[j].Bytes })
	if len(u.BuildCache) > 300 {
		u.BuildCache = u.BuildCache[:300]
	}
	u.Disk = diskOf(info.DockerRootDir)
	return u, nil
}

// layout returns a container's log file and, on the overlay2 driver, the
// directory its writable layer lives in.
func (c *Client) layout(ctx context.Context, id string) (logPath, upper string) {
	var in struct {
		LogPath     string `json:"LogPath"`
		GraphDriver struct {
			Data map[string]string `json:"Data"`
		} `json:"GraphDriver"`
	}
	if c.json(ctx, "GET", "/containers/"+id+"/json", nil, nil, &in) != nil {
		return "", ""
	}
	return in.LogPath, in.GraphDriver.Data["UpperDir"]
}

// fileSize is a file's size (0 when it cannot be read).
func fileSize(path string) int64 {
	if path == "" {
		return 0
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func diskOf(path string) dockerusage.Disk {
	if path == "" {
		path = "/var/lib/docker"
	}
	d := dockerusage.Disk{Path: path}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return d
	}
	d.TotalBytes = int64(st.Blocks) * int64(st.Bsize)
	d.FreeBytes = int64(st.Bavail) * int64(st.Bsize)
	d.UsedBytes = d.TotalBytes - int64(st.Bfree)*int64(st.Bsize)
	return d
}

// Clean removes the objects of r (see dockerusage.CleanRequest). Bulk
// prunes never touch SynCloud's own containers or named volumes.
func (c *Client) Clean(ctx context.Context, r dockerusage.CleanRequest) (dockerusage.CleanResult, error) {
	res := dockerusage.CleanResult{Errors: []string{}}
	if len(r.IDs) > 0 {
		for _, id := range r.IDs {
			var err error
			switch r.Kind {
			case dockerusage.KindImages:
				err = c.json(ctx, "DELETE", "/images/"+url.PathEscape(id), nil, nil, nil)
			case dockerusage.KindContainers:
				err = c.json(ctx, "DELETE", "/containers/"+url.PathEscape(id), url.Values{"v": {"false"}}, nil, nil)
			case dockerusage.KindVolumes:
				err = c.json(ctx, "DELETE", "/volumes/"+url.PathEscape(id), nil, nil, nil)
			case dockerusage.KindBuildCache:
				f, _ := json.Marshal(map[string][]string{"id": {id}})
				var out struct{ SpaceReclaimed int64 }
				if err = c.json(ctx, "POST", "/build/prune", url.Values{"filters": {string(f)}, "all": {"true"}}, nil, &out); err == nil {
					res.ReclaimedBytes += out.SpaceReclaimed
				}
			default:
				return res, fmt.Errorf("unknown kind %q", r.Kind)
			}
			if err != nil {
				res.Errors = append(res.Errors, id+": "+err.Error())
				continue
			}
			res.Removed++
		}
		return res, nil
	}
	filters := func(m map[string][]string) url.Values {
		b, _ := json.Marshal(m)
		return url.Values{"filters": {string(b)}}
	}
	var out struct {
		SpaceReclaimed    int64    `json:"SpaceReclaimed"`
		ContainersDeleted []string `json:"ContainersDeleted"`
		ImagesDeleted     []struct {
			Deleted string `json:"Deleted"`
		} `json:"ImagesDeleted"`
		VolumesDeleted []string `json:"VolumesDeleted"`
		CachesDeleted  []string `json:"CachesDeleted"`
	}
	var err error
	switch r.Kind {
	case dockerusage.KindContainers:
		// Stopped containers outside SynCloud; its own are removed one by one.
		err = c.json(ctx, "POST", "/containers/prune", filters(map[string][]string{"label!": {"syncloud.managed=true"}}), nil, &out)
		res.Removed = len(out.ContainersDeleted)
	case dockerusage.KindImages:
		dangling := "true"
		if r.All {
			dangling = "false"
		}
		err = c.json(ctx, "POST", "/images/prune", filters(map[string][]string{"dangling": {dangling}}), nil, &out)
		for _, d := range out.ImagesDeleted {
			if d.Deleted != "" {
				res.Removed++
			}
		}
	case dockerusage.KindVolumes:
		// Docker's default: anonymous volumes no container uses. Named ones
		// (databases, registry, service data) are only ever removed by name.
		err = c.json(ctx, "POST", "/volumes/prune", nil, nil, &out)
		res.Removed = len(out.VolumesDeleted)
	case dockerusage.KindBuildCache:
		q := url.Values{}
		if r.All {
			q.Set("all", "true")
		}
		err = c.json(ctx, "POST", "/build/prune", q, nil, &out)
		res.Removed = len(out.CachesDeleted)
	default:
		return res, fmt.Errorf("unknown kind %q", r.Kind)
	}
	res.ReclaimedBytes = out.SpaceReclaimed
	return res, err
}
