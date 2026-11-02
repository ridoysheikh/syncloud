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
// files), volumes and build cache, and the disk they live on.
func (c *Client) Usage(ctx context.Context) (dockerusage.Usage, error) {
	var df struct {
		LayersSize int64 `json:"LayersSize"`
		Images     []struct {
			ID         string   `json:"Id"`
			RepoTags   []string `json:"RepoTags"`
			Created    int64    `json:"Created"`
			Size       int64    `json:"Size"`
			SharedSize int64    `json:"SharedSize"`
			Containers int      `json:"Containers"`
		} `json:"Images"`
		Containers []struct {
			ID      string   `json:"Id"`
			Names   []string `json:"Names"`
			Image   string   `json:"Image"`
			ImageID string   `json:"ImageID"`
			State   string   `json:"State"`
			Status  string   `json:"Status"`
			Created int64    `json:"Created"`
			SizeRw  int64    `json:"SizeRw"`
			Labels  map[string]string
			Mounts  []struct {
				Type string `json:"Type"`
				Name string `json:"Name"`
			} `json:"Mounts"`
		} `json:"Containers"`
		Volumes []struct {
			Name      string `json:"Name"`
			Driver    string `json:"Driver"`
			CreatedAt string `json:"CreatedAt"`
			UsageData *struct {
				Size     int64 `json:"Size"`
				RefCount int   `json:"RefCount"`
			} `json:"UsageData"`
		} `json:"Volumes"`
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
	// Measuring every container and volume can take a while on a big host.
	if err := c.json(ctx, "GET", "/system/df", nil, nil, &df); err != nil {
		return dockerusage.Usage{}, err
	}
	var info struct {
		DockerRootDir string `json:"DockerRootDir"`
		ServerVersion string `json:"ServerVersion"`
	}
	_ = c.json(ctx, "GET", "/info", nil, nil, &info)

	u := dockerusage.Usage{DockerVersion: info.ServerVersion, LayersBytes: max(df.LayersSize, 0),
		Images: []dockerusage.Image{}, Containers: []dockerusage.Container{}, Volumes: []dockerusage.Volume{}, BuildCache: []dockerusage.CacheRecord{}}
	for _, im := range df.Images {
		tags := []string{}
		for _, t := range im.RepoTags {
			if t != "<none>:<none>" {
				tags = append(tags, t)
			}
		}
		u.Images = append(u.Images, dockerusage.Image{ID: im.ID, Tags: tags, Created: im.Created, Bytes: max(im.Size, 0),
			SharedBytes: max(im.SharedSize, 0), Containers: max(im.Containers, 0), Dangling: len(tags) == 0})
	}
	for _, ct := range df.Containers {
		name := ""
		if len(ct.Names) > 0 {
			name = strings.TrimPrefix(ct.Names[0], "/")
		}
		x := dockerusage.Container{ID: ct.ID, Name: name, Image: ct.Image, ImageID: ct.ImageID, State: ct.State, Status: ct.Status,
			Created: ct.Created, WritableBytes: max(ct.SizeRw, 0), TaskID: ct.Labels["syncloud.task_id"],
			Managed: ct.Labels["syncloud.managed"] == "true", System: ct.Labels["syncloud.system"] == "true", Volumes: []string{}}
		for _, m := range ct.Mounts {
			if m.Type == "volume" && m.Name != "" {
				x.Volumes = append(x.Volumes, m.Name)
			}
		}
		x.LogBytes = c.logBytes(ctx, ct.ID)
		u.Containers = append(u.Containers, x)
	}
	for _, v := range df.Volumes {
		x := dockerusage.Volume{Name: v.Name, Driver: v.Driver, Anonymous: anonymousVolume.MatchString(v.Name)}
		if v.UsageData != nil {
			x.Bytes, x.RefCount = max(v.UsageData.Size, 0), max(v.UsageData.RefCount, 0)
		}
		if t, err := time.Parse(time.RFC3339, v.CreatedAt); err == nil {
			x.Created = t.Unix()
		}
		u.Volumes = append(u.Volumes, x)
	}
	for _, b := range df.BuildCache {
		x := dockerusage.CacheRecord{ID: b.ID, Type: b.Type, Description: b.Description, Bytes: max(b.Size, 0), InUse: b.InUse, Shared: b.Shared}
		if t, err := time.Parse(time.RFC3339Nano, b.LastUsedAt); err == nil {
			x.LastUsed = t.Unix()
		}
		u.BuildCache = append(u.BuildCache, x)
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

// logBytes is the size of a container's json log file (0 when unknown).
func (c *Client) logBytes(ctx context.Context, id string) int64 {
	var in struct {
		LogPath string `json:"LogPath"`
	}
	if c.json(ctx, "GET", "/containers/"+id+"/json", nil, nil, &in) != nil || in.LogPath == "" {
		return 0
	}
	fi, err := os.Stat(in.LogPath)
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
