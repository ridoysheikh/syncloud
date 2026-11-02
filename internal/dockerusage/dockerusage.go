// Package dockerusage describes what Docker holds on a node's disk and how
// to clean it: the agent measures and cleans, the controller adds which app
// each container and volume belongs to. Both sides speak these types as
// JSON inside DockerRequest/DockerReply.
package dockerusage

// Operations of a DockerRequest.
const (
	OpUsage = "usage"
	OpClean = "clean"
)

// Kinds of Docker objects.
const (
	KindImages     = "images"
	KindContainers = "containers"
	KindVolumes    = "volumes"
	KindBuildCache = "buildcache"
)

// Image is one image on the node.
type Image struct {
	ID      string   `json:"id"`
	Tags    []string `json:"tags"`
	Created int64    `json:"created"`
	// Bytes is the image's size; SharedBytes is what other images share, so
	// Bytes-SharedBytes is what removing it alone frees.
	Bytes       int64 `json:"bytes"`
	SharedBytes int64 `json:"sharedBytes"`
	Containers  int   `json:"containers"` // containers (running or not) created from it
	Dangling    bool  `json:"dangling"`   // no tag
}

// Container is one container.
type Container struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Image string `json:"image"`
	// ImageID is the image's ID, to count the image towards the container's app.
	ImageID string `json:"imageId"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Created int64  `json:"created"`
	// WritableBytes is what the container wrote into its own layer; LogBytes
	// is its json log file (Docker keeps it until the container goes).
	WritableBytes int64 `json:"writableBytes"`
	LogBytes      int64 `json:"logBytes"`
	// TaskID is the SynCloud task the container runs ("" outside SynCloud).
	// Measuring: the writable layer's size is still being measured (0 until then).
	Measuring bool     `json:"measuring"`
	TaskID    string   `json:"taskId"`
	Managed   bool     `json:"managed"`
	System    bool     `json:"system"`
	Volumes   []string `json:"volumes"` // named volumes mounted
}

// Volume is a Docker volume.
type Volume struct {
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Bytes    int64  `json:"bytes"`
	RefCount int    `json:"refCount"` // containers using it
	// Measuring: the size is still being worked out in the background.
	Measuring bool `json:"measuring"`
	// Anonymous volumes (a random 64-hex name) are the ones a plain
	// docker run leaves behind.
	Anonymous bool  `json:"anonymous"`
	Created   int64 `json:"created"`
}

// CacheRecord is a build cache entry.
type CacheRecord struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Bytes       int64  `json:"bytes"`
	InUse       bool   `json:"inUse"`
	Shared      bool   `json:"shared"`
	LastUsed    int64  `json:"lastUsed"`
}

// Disk is the filesystem Docker's data lives on.
type Disk struct {
	Path       string `json:"path"` // Docker's root directory
	TotalBytes int64  `json:"totalBytes"`
	UsedBytes  int64  `json:"usedBytes"`
	FreeBytes  int64  `json:"freeBytes"`
}

// Usage is the agent's measurement of its node.
type Usage struct {
	DockerVersion string      `json:"dockerVersion"`
	Disk          Disk        `json:"disk"`
	Images        []Image     `json:"images"`
	Containers    []Container `json:"containers"`
	Volumes       []Volume    `json:"volumes"`
	// BuildCache lists the largest entries; the totals cover all of them.
	BuildCache      []CacheRecord `json:"buildCache"`
	BuildCacheBytes int64         `json:"buildCacheBytes"`
	BuildCacheCount int           `json:"buildCacheCount"`
	// LayersBytes is the space all image layers take, shared ones once.
	LayersBytes int64 `json:"layersBytes"`
	// Pending counts the volumes and containers whose size is still being
	// measured: walking a big volume takes minutes, so Usage answers at once
	// and sizes fill in on later calls.
	Pending int `json:"pending"`
}

// CleanRequest asks for objects to be removed. With IDs it removes exactly
// those (Docker refuses anything in use); without, it prunes the kind.
type CleanRequest struct {
	Kind string   `json:"kind"`
	IDs  []string `json:"ids,omitempty"`
	// All widens a prune: images — every unused image, not only untagged
	// ones; build cache — everything not in use, not only old entries.
	// Volumes: only anonymous unused volumes are ever pruned in bulk.
	All bool `json:"all,omitempty"`
}

// CleanResult is what a clean did.
type CleanResult struct {
	ReclaimedBytes int64    `json:"reclaimedBytes"`
	Removed        int      `json:"removed"`
	Errors         []string `json:"errors"` // objects Docker refused to remove, with why
}
