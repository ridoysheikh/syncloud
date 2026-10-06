// Package uninstall removes SynCloud from a host (§5.0.1): services, managed
// containers, the WireGuard interface and the nftables tables. Data stays
// unless purge is set.
package uninstall

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/vishvananda/netlink"

	"syncloud/internal/agent/docker"
	"syncloud/internal/agent/netcfg"
)

type Options struct {
	Units    []string // systemd units to stop, disable and delete
	DataDirs []string // deleted with Purge
	Volumes  []string // named Docker volumes deleted with Purge
	Purge    bool
	Out      io.Writer
}

// Run removes everything and reports each step; failures of single steps are
// reported and the rest continues.
func Run(ctx context.Context, o Options) error {
	say := func(format string, args ...any) { fmt.Fprintf(o.Out, "  "+format+"\n", args...) }
	var failed int
	warn := func(what string, err error) {
		if err != nil {
			failed++
			say("! %s: %v", what, err)
		}
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		for _, u := range o.Units {
			path := filepath.Join("/etc/systemd/system", u)
			if _, err := os.Stat(path); err != nil {
				continue
			}
			_ = exec.CommandContext(ctx, "systemctl", "disable", "--now", u).Run()
			warn("remove "+path, os.Remove(path))
			say("✓ stopped and removed %s", u)
		}
		_ = exec.CommandContext(ctx, "systemctl", "daemon-reload").Run()
	}

	d := docker.New(docker.DefaultSocket)
	if _, err := d.Version(ctx); err == nil {
		list, err := d.List(ctx, "syncloud.managed=true")
		warn("list containers", err)
		for _, c := range list {
			warn("remove container "+c.ID[:12], d.Remove(ctx, c.ID))
		}
		say("✓ removed %d SynCloud containers", len(list))
		for _, n := range []string{netcfg.Network, "syncloud-system"} {
			warn("remove network "+n, d.RemoveNetwork(ctx, n))
		}
		if o.Purge {
			for _, v := range o.Volumes {
				warn("remove volume "+v, d.RemoveVolume(ctx, v))
			}
			n, err := d.RemoveVolumes(ctx, "syncloud.managed=true")
			warn("remove volumes", err)
			say("✓ removed %d volumes", n+len(o.Volumes))
		}
	} else {
		say("- Docker is not reachable; containers left alone")
	}

	if l, err := netlink.LinkByName(netcfg.Interface); err == nil {
		warn("delete "+netcfg.Interface, netlink.LinkDel(l))
		say("✓ removed the WireGuard interface %s", netcfg.Interface)
	}
	if _, err := exec.LookPath("nft"); err == nil {
		for _, fam := range []string{"inet", "ip"} {
			if exec.CommandContext(ctx, "nft", "list", "table", fam, "syncloud").Run() == nil {
				warn("delete nftables table "+fam+" syncloud", exec.CommandContext(ctx, "nft", "delete", "table", fam, "syncloud").Run())
				say("✓ removed the nftables table %s syncloud", fam)
			}
		}
	}

	if o.Purge {
		for _, dir := range o.DataDirs {
			warn("delete "+dir, os.RemoveAll(dir))
			say("✓ deleted %s", dir)
		}
	} else {
		for _, dir := range o.DataDirs {
			say("- kept %s (use --purge to delete it)", dir)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d step(s) failed", failed)
	}
	return nil
}
