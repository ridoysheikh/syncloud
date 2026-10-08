// Command imagepack turns a local Docker image into the archive a release
// carries: an OCI image layout with gzip-compressed layers, as a tar.gz.
// The controller loads it into its registry on first use (internal/sysimage).
//
//	go run ./tools/imagepack syncloud-postgres:18-r1 18-r1 dist/syncloud-postgres-18-r1-linux-amd64.tar.gz
package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ridoysheikh/syncloud/internal/registry"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: imagepack IMAGE TAG OUT.tar.gz")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, "imagepack:", err)
		os.Exit(1)
	}
}

func run(image, tag, out string) error {
	dir, err := os.MkdirTemp("", "imagepack-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	// docker save writes an OCI layout (Docker 25+).
	save := exec.Command("docker", "save", image)
	save.Stderr = os.Stderr
	stdout, err := save.StdoutPipe()
	if err != nil {
		return err
	}
	if err := save.Start(); err != nil {
		return err
	}
	if err := untar(stdout, dir); err != nil {
		return err
	}
	if err := save.Wait(); err != nil {
		return fmt.Errorf("docker save %s: %w", image, err)
	}
	if err := registry.CompressLayout(dir, tag); err != nil {
		return err
	}
	return tarGz(dir, out)
}

func untar(r io.Reader, dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(filepath.Clean(h.Name), 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			name := filepath.Clean(h.Name)
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return err
			}
			f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
	}
}

// tarGz packs dir; the layers inside are compressed already, so the outer
// gzip is fast.
func tarGz(dir, out string) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	zw, _ := gzip.NewWriterLevel(f, gzip.BestSpeed)
	tw := tar.NewWriter(zw)
	err = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(tw, in)
		return err
	})
	for _, c := range []io.Closer{tw, zw, f} {
		if cerr := c.Close(); err == nil {
			err = cerr
		}
	}
	return err
}
