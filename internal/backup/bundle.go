// Package backup makes encrypted backups of the controller (§13) and ships
// them to S3. A bundle holds a consistent SQLite snapshot (VACUUM INTO) and
// the key material in the data directory (CA, registry token key, …), sealed
// with the master key. The header carries the master key wrapped by the
// recovery key, so restoring needs the bundle plus the recovery key only.
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/auth"
	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

const (
	magic  = "SYNBK1\n"
	aad    = "syncloud-backup-v1"
	dbName = "syncloud.db"
	// Ext is the bundle file extension.
	Ext = ".synbak"
)

// skipped are data-dir files never put in a bundle: the live database (a
// snapshot is added instead), the unwrapped master key and one-time secrets.
func skipped(rel string) bool {
	base := filepath.Base(rel)
	switch {
	case strings.HasPrefix(base, dbName), strings.HasPrefix(base, ".backup-"),
		base == "master.key", base == "master.key.wrapped",
		base == "setup-token", base == "recovery-key", base == "local-join.token":
		return true
	}
	return false
}

// Create writes an encrypted bundle of dataDir to w.
func Create(ctx context.Context, st *store.Store, box *secrets.Box, dataDir string, w io.Writer) error {
	snap := filepath.Join(dataDir, ".backup-"+auth.NewID("")+".db")
	defer os.Remove(snap)
	if _, err := st.W.ExecContext(ctx, `VACUUM INTO ?`, snap); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}

	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	add := func(name, path string) error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	}
	if err := add(dbName, snap); err != nil {
		return err
	}
	err := filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil || skipped(rel) {
			return err
		}
		return add(filepath.ToSlash(rel), path)
	})
	if err != nil {
		return fmt.Errorf("add files: %w", err)
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}

	wrapped := box.Wrapped()
	if len(wrapped) == 0 {
		return errors.New("no wrapped master key: backups need a recovery key")
	}
	var hdr bytes.Buffer
	hdr.WriteString(magic)
	_ = binary.Write(&hdr, binary.BigEndian, uint32(len(wrapped)))
	hdr.Write(wrapped)
	if _, err := w.Write(hdr.Bytes()); err != nil {
		return err
	}
	_, err = w.Write(box.Seal(tgz.Bytes(), []byte(aad)))
	return err
}

// Restore unpacks bundle into dataDir using the recovery key. dataDir must not
// hold a database unless force is set. It returns the restored file names.
func Restore(bundle []byte, recoveryKey, dataDir string, force bool) ([]string, error) {
	if !bytes.HasPrefix(bundle, []byte(magic)) || len(bundle) < len(magic)+4 {
		return nil, errors.New("not a SynCloud backup")
	}
	rest := bundle[len(magic):]
	n := binary.BigEndian.Uint32(rest[:4])
	if int(n) > len(rest)-4 {
		return nil, errors.New("corrupt backup header")
	}
	wrapped, sealed := rest[4:4+n], rest[4+n:]
	master, err := secrets.Unwrap(wrapped, recoveryKey)
	if err != nil {
		return nil, err
	}
	box, err := secrets.New(master)
	if err != nil {
		return nil, err
	}
	tgz, err := box.Open(sealed, []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("decrypt backup: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, dbName)); err == nil && !force {
		return nil, fmt.Errorf("%s already has a database; stop the controller and pass --force to overwrite", dataDir)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	// Remove a previous database and its WAL so the snapshot is not mixed with them.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(filepath.Join(dataDir, dbName+suffix))
	}

	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(h.Name))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("unsafe path in backup: %q", h.Name)
		}
		dst := filepath.Join(dataDir, clean)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, err
		}
		// Public certificates are bind-mounted into containers and must stay readable.
		mode := os.FileMode(0o600)
		if strings.HasSuffix(clean, ".crt") {
			mode = 0o644
		}
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(f, io.LimitReader(tr, h.Size)); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		names = append(names, h.Name)
	}
	if err := secrets.Restore(dataDir, master, wrapped); err != nil {
		return nil, err
	}
	return names, nil
}
