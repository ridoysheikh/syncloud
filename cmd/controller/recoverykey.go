package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/ridoysheikh/syncloud/internal/auth"
	"github.com/ridoysheikh/syncloud/internal/backup"
	"github.com/ridoysheikh/syncloud/internal/s3"
	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// The recovery key is managed only here, on the controller host (Phase 19b
// revised): no API, synctl command or dashboard page can show or change it.
// Reaching it needs root on this machine (the data directory is 0700), and a
// new key is shown once, when it is made, and never stored.

const recoveryKeyUsage = `Usage: syncloud-controller recovery-key rotate [--data-dir DIR] [--yes]
       syncloud-controller recovery-key verify [--data-dir DIR]
       syncloud-controller recovery-key rekey  [--data-dir DIR]

  rotate  make a new recovery key, show it once, and re-key the backups in S3
          so only the new key opens them; the old key stops working for them
  verify  check that a recovery key (read from the terminal) opens this install
  rekey   re-key the stored backups again (after a rotation that could not
          reach S3); it needs no recovery key`

func recoveryKeyCmd(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Println(recoveryKeyUsage)
		return nil
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("syncloud-controller recovery-key "+sub, flag.ContinueOnError)
	dataDir := fs.String("data-dir", envOr("SYNCLOUD_DATA_DIR", "/var/lib/syncloud"), "controller data directory")
	yes := fs.Bool("yes", false, "rotate without asking")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := filepath.Abs(*dataDir)
	if err != nil {
		return err
	}
	box, err := secrets.Load(dir)
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%w: run this as root (sudo) on the controller host", err)
	} else if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	switch sub {
	case "rotate":
		return rotateRecoveryKey(ctx, dir, box, *yes)
	case "verify":
		return verifyRecoveryKey(box)
	case "rekey":
		st, err := store.Open(ctx, filepath.Join(dir, "syncloud.db"))
		if err != nil {
			return err
		}
		defer st.Close()
		return rekeyBackups(ctx, st, box, dir)
	}
	return fmt.Errorf("unknown command %q\n%s", sub, recoveryKeyUsage)
}

func rotateRecoveryKey(ctx context.Context, dir string, box *secrets.Box, yes bool) error {
	if !yes {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("confirm on a terminal, or pass --yes")
		}
		fmt.Fprintln(os.Stderr, "This makes a new recovery key. The stored backups are re-keyed, so the current key")
		fmt.Fprintln(os.Stderr, "no longer opens them. The new key is shown once and is not stored anywhere.")
		fmt.Fprint(os.Stderr, "Type ROTATE to continue: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if strings.TrimSpace(line) != "ROTATE" {
			return errors.New("not rotated")
		}
	}
	st, err := store.Open(ctx, filepath.Join(dir, "syncloud.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	key := secrets.NewRecoveryKey()
	// The running controller reads the new wrapped key before its next
	// backup, so it needs no restart.
	if err := box.Rewrap(key); err != nil {
		return fmt.Errorf("rotate: %w", err)
	}
	// Shown before anything else can fail, so the key in use is never lost.
	fmt.Printf("\nNew recovery key (shown once, not stored; save it offline now):\n\n    %s\n\n", key)

	// No copy is kept on the instance any more (older releases kept one).
	if err := st.DeleteSetting(ctx, store.SettingRecoveryKey); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not remove the copy older releases kept:", err)
	}
	// Before setup, the wizard checks the key's last characters.
	if _, ok, err := st.GetSetting(ctx, store.SettingRecoverySuffixHash); err == nil && ok {
		_ = st.SetSetting(ctx, store.SettingRecoverySuffixHash, auth.HashToken(secrets.RecoveryKeySuffix(key)))
	}
	if f := filepath.Join(dir, recoveryKeyFile); fileExists(f) {
		_ = os.WriteFile(f, []byte(key+"\n"), 0o600)
	}
	host, _ := os.Hostname()
	_ = st.WriteAudit(ctx, store.AuditEvent{At: time.Now().UTC(), Action: "backups:RotateRecoveryKey",
		Resource: "srn:syncloud:backup/recovery-key", IP: "host", UserAgent: "syncloud-controller recovery-key rotate",
		Detail: map[string]any{"host": host, "uid": os.Getuid()}})
	return rekeyBackups(ctx, st, box, dir)
}

// rekeyBackups re-keys the backups in S3 to the current recovery key. It
// runs twice: a backup the controller was uploading while the key changed
// may still carry the old key, and the second pass catches it.
func rekeyBackups(ctx context.Context, st *store.Store, box *secrets.Box, dir string) error {
	m := backup.NewManager(st, box, dir, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s3m := s3.New(st, box)
	m.Connection, m.Connections = backupConnection(s3m), backupConnections(s3m)
	if err := m.Load(ctx); err != nil {
		return fmt.Errorf("read the backup settings: %w", err)
	}
	if _, ok := m.Config(); !ok {
		fmt.Println("Backups to S3 are not set up, so there is nothing to re-key.")
		return nil
	}
	fmt.Println("Re-keying the stored backups…")
	var total backup.RekeyResult
	for pass := 0; pass < 2; pass++ {
		res, err := m.RekeyAll(ctx)
		if err != nil {
			return fmt.Errorf("re-key the backups: %w (run 'syncloud-controller recovery-key rekey' to retry)", err)
		}
		total.Rekeyed += res.Rekeyed
		total.Current, total.Skipped, total.Failed = res.Current, res.Skipped, res.Failed
		if res.Rekeyed == 0 && len(res.Failed) == 0 {
			break
		}
	}
	if total.Rekeyed > 0 {
		fmt.Printf("Re-keyed %d backups; the current key opens them.\n", total.Rekeyed)
	}
	fmt.Printf("%d stored backups open with the current recovery key.\n", total.Current)
	if len(total.Skipped) > 0 {
		fmt.Printf("Left as they were (made by another install or an older master key): %s\n", strings.Join(total.Skipped, ", "))
	}
	if len(total.Failed) > 0 {
		for _, f := range total.Failed {
			fmt.Fprintln(os.Stderr, "Could not re-key:", f)
		}
		return errors.New("some backups still open only with the previous key; run 'syncloud-controller recovery-key rekey' to retry")
	}
	return nil
}

func verifyRecoveryKey(box *secrets.Box) error {
	fmt.Fprint(os.Stderr, "Recovery key: ")
	var line string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		line = string(b)
	} else {
		b, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		line = b
	}
	if err := box.Verify(strings.TrimSpace(line)); err != nil {
		return errors.New("this key does not open this install's backups")
	}
	fmt.Println("The key matches: it opens this install's backups.")
	return nil
}
