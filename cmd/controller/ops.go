package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"syncloud/internal/agent/docker"
	"syncloud/internal/backup"
	"syncloud/internal/config"
	"syncloud/internal/domain"
	"syncloud/internal/store"
	"syncloud/internal/system"
)

// restore unpacks a backup into the data directory (§13). The controller must
// be stopped. Sources: a local file, or the newest (or named) backup in S3.
func restore(args []string) error {
	fs := flag.NewFlagSet("syncloud-controller restore", flag.ContinueOnError)
	dataDir := fs.String("data-dir", envOr("SYNCLOUD_DATA_DIR", "/var/lib/syncloud"), "controller data directory")
	recoveryKey := fs.String("recovery-key", os.Getenv("SYNCLOUD_RECOVERY_KEY"), "recovery key (prompted when empty)")
	file := fs.String("file", "", "backup bundle file (.synbak)")
	force := fs.Bool("force", false, "overwrite an existing database")
	var c backup.Config
	fs.StringVar(&c.Endpoint, "s3-endpoint", "", "S3 endpoint URL")
	fs.StringVar(&c.Region, "s3-region", "", "S3 region")
	fs.StringVar(&c.Bucket, "s3-bucket", "", "S3 bucket")
	fs.StringVar(&c.Prefix, "s3-prefix", "", "S3 key prefix")
	fs.StringVar(&c.AccessKeyID, "s3-access-key-id", os.Getenv("SYNCLOUD_BACKUP_ACCESS_KEY_ID"), "S3 access key ID")
	fs.StringVar(&c.SecretAccessKey, "s3-secret-access-key", os.Getenv("SYNCLOUD_BACKUP_SECRET_ACCESS_KEY"), "S3 secret (or $SYNCLOUD_BACKUP_SECRET_ACCESS_KEY)")
	name := fs.String("name", "", "backup object name (default: newest)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var bundle []byte
	var err error
	switch {
	case *file != "":
		bundle, err = os.ReadFile(*file)
	case c.Endpoint != "":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		var got string
		bundle, got, err = backup.Download(ctx, c, *name)
		if err == nil {
			fmt.Fprintln(os.Stderr, "Downloaded", got)
		}
	default:
		return errors.New("give --file or the --s3-* flags")
	}
	if err != nil {
		return err
	}
	if *recoveryKey == "" {
		fmt.Fprint(os.Stderr, "Recovery key: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		*recoveryKey = strings.TrimSpace(line)
	}
	abs, err := filepath.Abs(*dataDir)
	if err != nil {
		return err
	}
	files, err := backup.Restore(bundle, *recoveryKey, abs, *force)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Restored %d files into %s.\n", len(files), abs)
	fmt.Fprintln(os.Stderr, "Start the controller. A sslip.io/nip.io base domain follows the new public IP automatically;")
	fmt.Fprintln(os.Stderr, "with your own domain, point its DNS records at this host.")
	return nil
}

// doctor checks every component and prints a fix for each problem (§5.0).
func doctor(args []string) error {
	cfg, err := config.LoadController(args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	failed := 0
	check := func(name string, err error, fix string) {
		if err == nil {
			fmt.Printf("  ✓ %s\n", name)
			return
		}
		failed++
		fmt.Printf("  ✗ %s: %v\n", name, err)
		if fix != "" {
			fmt.Printf("      → %s\n", fix)
		}
	}
	warn := func(name, msg string) { fmt.Printf("  ! %s: %s\n", name, msg) }

	fmt.Println("Controller")
	fi, err := os.Stat(filepath.Join(cfg.DataDir, "master.key"))
	if err == nil && fi.Mode().Perm()&0o077 != 0 {
		err = fmt.Errorf("mode %v", fi.Mode().Perm())
	}
	check("master key present and private", err, "chmod 600 "+filepath.Join(cfg.DataDir, "master.key"))
	_, err = os.Stat(filepath.Join(cfg.DataDir, "master.key.wrapped"))
	check("wrapped master key (recovery key) present", err, "start the controller once to create a recovery key")

	st, err := store.Open(ctx, cfg.DBPath())
	check("database opens", err, "check "+cfg.DBPath()+" and disk space")
	var base string
	if st != nil {
		defer st.Close()
		base, _, _ = st.GetSetting(ctx, store.SettingBaseDomain)
	}
	check("API answers on "+cfg.Listen, httpGet(ctx, "http://"+loopbackURLHost(cfg.Listen)+"/api/v1/system/status", 200), "systemctl status syncloud-controller; journalctl -u syncloud-controller")
	check("disk space for "+cfg.DataDir, diskFree(cfg.DataDir, 0.10), "free space; the database, logs and metrics share this disk")

	fmt.Println("Docker and system tasks")
	dc := docker.New("/var/run/docker.sock")
	_, err = dc.Version(ctx)
	check("Docker Engine reachable", err, "install Docker and make sure /var/run/docker.sock exists")
	if err == nil {
		cs, err := dc.List(ctx, "syncloud.system=true")
		running := map[string]bool{}
		for _, c := range cs {
			running[c.Labels["syncloud.task_id"]] = c.State == "running"
		}
		for _, comp := range system.Components {
			e := err
			if e == nil && !running[comp.TaskID] {
				e = errors.New("not running")
			}
			check(comp.Name+" container", e, "make sure the local agent (ctl-0) is running: systemctl status syncloud-agent")
		}
	}
	check("Traefik ping", httpGet(ctx, "http://"+loopbackURLHost(cfg.TraefikAdmin)+"/ping", 200), "docker logs syncloud-traefik")
	check("Registry", httpGet(ctx, "http://"+system.RegistryAddr+"/v2/", 401), "docker logs syncloud-registry")
	check("VictoriaMetrics", httpGet(ctx, "http://127.0.0.1:8428/health", 200), "docker logs syncloud-victoriametrics")
	check("VictoriaLogs", httpGet(ctx, "http://127.0.0.1:9428/health", 200), "docker logs syncloud-victorialogs")

	fmt.Println("Domain and certificates")
	if base == "" {
		warn("base domain", "not set (dev mode); set one in Settings → Domains")
	} else {
		ip, err := domain.NewDetector(cfg.PublicIP).PublicIP(ctx)
		check("public IP detected", err, "pass --public-ip")
		if err == nil {
			for _, h := range []string{base, domain.RegistryHost(base)} {
				var rerr error
				if ok, addrs := domain.Resolves(ctx, h, ip); !ok {
					rerr = fmt.Errorf("resolves to %v, want %s", addrs, ip)
				}
				check(h+" resolves here", rerr, "point an A record at "+ip)
			}
		}
		if st != nil {
			certs, _ := st.ListCertificates(ctx)
			for _, c := range certs {
				if c.Host != base && c.Host != domain.RegistryHost(base) {
					continue
				}
				var cerr error
				if c.Status != "valid" {
					cerr = fmt.Errorf("%s: %s", c.Status, c.LastError)
				}
				check("certificate "+c.Host, cerr, "port 80 must be reachable from the internet; retry in Settings → Domains")
			}
		}
	}
	fmt.Println("Backups")
	if st != nil {
		if _, ok, _ := st.GetSetting(ctx, "backup.config"); !ok {
			warn("backups", "off; configure S3 in Settings → Backups")
		} else {
			fmt.Println("  ✓ backups configured")
		}
	}

	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	fmt.Println("\nAll checks passed.")
	return nil
}

func httpGet(ctx context.Context, url string, want int) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) {
			return fmt.Errorf("nothing listening (%v)", op.Err)
		}
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != want {
		return fmt.Errorf("HTTP %d, want %d", resp.StatusCode, want)
	}
	return nil
}

func diskFree(path string, minFree float64) error {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return err
	}
	free := float64(s.Bavail) / float64(s.Blocks)
	if free < minFree {
		return fmt.Errorf("only %.0f%% free", free*100)
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
