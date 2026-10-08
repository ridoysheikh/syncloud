// Package sysimage loads the platform images a release carries (the
// managed PostgreSQL images) into the built-in registry the first time
// something needs them. Releases are self-contained: no public registry is
// involved, and an air-gapped host can drop the archives in place.
package sysimage

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/registry"
	"github.com/ridoysheikh/syncloud/internal/upgrade"
)

// RegistryPrefix marks an image in the built-in registry ("@registry/…").
const RegistryPrefix = "@registry/"

// retryAfter is how long a failed preparation waits before trying again.
const retryAfter = time.Minute

// Preparing means the image is being loaded; Phase says how far it got.
type Preparing struct{ Phase string }

func (p *Preparing) Error() string { return "preparing the image: " + p.Phase }

// Registry is what the seeder needs from the built-in registry.
type Registry interface {
	HasImage(ctx context.Context, repo, ref string) (bool, error)
	PushLayout(ctx context.Context, repo, tag, dir string) error
}

// Seeder loads release images into the registry on demand.
type Seeder struct {
	Registry Registry
	// Source and Version locate this controller's release, which carries
	// the archives.
	Source  upgrade.Source
	Version string
	// Dir holds archives placed by hand (air-gapped hosts): <Dir>/<archive>
	// is used before anything is downloaded.
	Dir string
	// Arch is the archives' CPU architecture (default: the controller's).
	Arch string
	Log  *slog.Logger

	mu   sync.Mutex
	jobs map[string]*job
}

type job struct {
	phase  string
	done   bool
	err    error
	failed time.Time
}

// Archive is the release file of repo:tag: "syncloud-<base of repo>-<tag>-linux-<arch>.tar.gz".
func Archive(repo, tag, arch string) string {
	return "syncloud-" + path.Base(repo) + "-" + tag + "-linux-" + arch + ".tar.gz"
}

// split turns "@registry/a/b:tag" into its repository and tag.
func split(image string) (repo, tag string, ok bool) {
	rest, ok := strings.CutPrefix(image, RegistryPrefix)
	if !ok {
		return "", "", false
	}
	i := strings.LastIndexByte(rest, ':')
	if i <= 0 || i < strings.LastIndexByte(rest, '/') {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

// Ensure returns nil once image is in the registry. Images outside the
// built-in registry are always ready. Otherwise it starts loading the
// image in the background and returns *Preparing, or the last error while
// a failed attempt waits to be retried.
func (s *Seeder) Ensure(ctx context.Context, image string) error {
	repo, tag, ok := split(image)
	if !ok {
		return nil
	}
	s.mu.Lock()
	if s.jobs == nil {
		s.jobs = map[string]*job{}
	}
	j := s.jobs[image]
	switch {
	case j != nil && j.done && j.err == nil:
		s.mu.Unlock()
		return nil
	case j != nil && !j.done:
		phase := j.phase
		s.mu.Unlock()
		return &Preparing{Phase: phase}
	case j != nil && time.Since(j.failed) < retryAfter:
		err := j.err
		s.mu.Unlock()
		return err
	}
	j = &job{phase: "checking the registry"}
	s.jobs[image] = j
	s.mu.Unlock()

	if has, err := s.Registry.HasImage(ctx, repo, tag); err == nil && has {
		s.finish(j, nil)
		return nil
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		err := s.load(ctx, j, repo, tag)
		if err != nil && s.Log != nil {
			s.Log.Warn("loading a release image", "image", repo+":"+tag, "err", err)
		} else if s.Log != nil {
			s.Log.Info("release image loaded into the registry", "image", repo+":"+tag)
		}
		s.finish(j, err)
	}()
	return &Preparing{Phase: j.phase}
}

func (s *Seeder) finish(j *job, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j.done, j.err = true, err
	if err != nil {
		j.failed = time.Now()
	}
}

func (s *Seeder) setPhase(j *job, phase string) {
	s.mu.Lock()
	j.phase = phase
	s.mu.Unlock()
}

func (s *Seeder) load(ctx context.Context, j *job, repo, tag string) error {
	arch := s.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	name := Archive(repo, tag, arch)
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	file := filepath.Join(s.Dir, name)
	placed := true
	if _, err := os.Stat(file); err != nil {
		placed = false
		if devVersion(s.Version) {
			return fmt.Errorf("a development build has no release to take %s from: build it with make postgres-image and pass --postgres-image, or place %s in %s", repo+":"+tag, name, s.Dir)
		}
		s.setPhase(j, "downloading "+name)
		if err := s.Source.FetchFile(ctx, s.Version, name, file); err != nil {
			return fmt.Errorf("download %s: %w", name, err)
		}
	}
	tmp, err := os.MkdirTemp(s.Dir, ".layout-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	s.setPhase(j, "unpacking "+name)
	if err := untarGz(file, tmp); err != nil {
		return fmt.Errorf("unpack %s: %w", name, err)
	}
	s.setPhase(j, "loading into the registry")
	if err := s.Registry.PushLayout(ctx, repo, tag, tmp); err != nil {
		return err
	}
	if !placed {
		_ = os.Remove(file) // the registry has it now
	}
	return nil
}

// devVersion reports a build that was not released.
func devVersion(v string) bool {
	return v == "" || strings.Contains(v, "-dev") || strings.Contains(v, "dirty") || strings.HasPrefix(v, "0.0.0")
}

// untarGz unpacks a tar.gz of regular files and directories into dir.
func untarGz(file, dir string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		name := filepath.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return err
			}
			out, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected entry %s in the archive", h.Name)
		}
	}
}

var _ Registry = (*registry.Browser)(nil)
