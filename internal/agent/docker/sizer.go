package docker

import (
	"context"
	"io/fs"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// sizeTTL is how long a measured size is trusted before it is measured
// again; the old value is shown meanwhile.
const sizeTTL = 10 * time.Minute

type sized struct {
	bytes int64
	at    time.Time
}

// sizer measures directory sizes in the background. Walking a volume with
// millions of files (a registry's) takes minutes, which must not hold up an
// answer, so callers get the last known size, or "not yet", and the walk
// finishes on its own.
type sizer struct {
	mu      sync.Mutex
	known   map[string]sized
	running map[string]bool
	sem     chan struct{}
}

func newSizer() *sizer {
	return &sizer{known: map[string]sized{}, running: map[string]bool{}, sem: make(chan struct{}, 2)}
}

// get returns the size of dir under key and whether one is known; it starts a
// measurement when there is none or it is stale.
func (z *sizer) get(key, dir string) (int64, bool) {
	z.mu.Lock()
	defer z.mu.Unlock()
	k, ok := z.known[key]
	if (!ok || time.Since(k.at) > sizeTTL) && !z.running[key] && dir != "" {
		z.running[key] = true
		go z.measure(key, dir)
	}
	return k.bytes, ok
}

func (z *sizer) measure(key, dir string) {
	z.sem <- struct{}{}
	defer func() { <-z.sem }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	n := dirSize(ctx, dir)
	z.mu.Lock()
	z.known[key] = sized{n, time.Now()}
	delete(z.running, key)
	z.mu.Unlock()
}

// forget drops sizes of objects that no longer exist.
func (z *sizer) forget(live map[string]bool) {
	z.mu.Lock()
	defer z.mu.Unlock()
	for k := range z.known {
		if !live[k] {
			delete(z.known, k)
		}
	}
}

// dirSize is the disk space dir takes (allocated blocks, hard links once).
func dirSize(ctx context.Context, dir string) int64 {
	var total int64
	type inode struct{ dev, ino uint64 }
	links := map[inode]bool{}
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil // vanished or unreadable: skip it
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			total += fi.Size()
			return nil
		}
		if st.Nlink > 1 && !d.IsDir() {
			id := inode{uint64(st.Dev), uint64(st.Ino)}
			if links[id] {
				return nil
			}
			links[id] = true
		}
		total += st.Blocks * 512
		return nil
	})
	return total
}
