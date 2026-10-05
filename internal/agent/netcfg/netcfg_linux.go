//go:build linux

package netcfg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/ipc"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
)

// platform holds the userspace WireGuard device (when the kernel module is
// missing) and the RTT probe listener.
type platform struct {
	dev       *device.Device
	uapi      net.Listener
	probe     net.Listener
	probeAddr string
}

func (p *platform) close() {
	if p.probe != nil {
		p.probe.Close()
	}
	if p.uapi != nil {
		p.uapi.Close()
	}
	if p.dev != nil {
		p.dev.Close()
	}
}

func (m *Manager) apply(ctx context.Context, cfg *agentv1.NetworkConfig) (string, error) {
	addr, err := netip.ParsePrefix(cfg.GetNodeAddress())
	if err != nil {
		return "", fmt.Errorf("node address: %w", err)
	}
	subnet, err := netip.ParsePrefix(cfg.GetContainerSubnet())
	if err != nil {
		return "", fmt.Errorf("container subnet: %w", err)
	}
	containers, err := netip.ParsePrefix(cfg.GetContainerCidr())
	if err != nil {
		return "", fmt.Errorf("container CIDR: %w", err)
	}

	mode, link, err := m.ensureLink()
	if err != nil {
		return mode, err
	}
	if err := m.configureWireGuard(cfg); err != nil {
		return mode, err
	}
	if err := netlink.AddrReplace(link, &netlink.Addr{IPNet: ipnet(addr)}); err != nil {
		return mode, fmt.Errorf("set address: %w", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return mode, fmt.Errorf("link up: %w", err)
	}
	// Remote container subnets go through WireGuard; the local /24 is more
	// specific and stays on the Docker bridge.
	if err := netlink.RouteReplace(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: ipnet(containers), Scope: netlink.SCOPE_LINK}); err != nil {
		return mode, fmt.Errorf("route %s: %w", containers, err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil {
		return mode, fmt.Errorf("enable IP forwarding: %w", err)
	}

	gateway := subnet.Addr().Next()
	err = m.docker.EnsureBridge(ctx, Network, subnet.String(), gateway.String(), map[string]string{
		"com.docker.network.bridge.name":                 Bridge,
		"com.docker.network.bridge.enable_ip_masquerade": "false",           // masquerade below, except to the mesh
		"com.docker.network.bridge.gateway_mode_ipv4":    "nat-unprotected", // container IPs are routable over the mesh
		"com.docker.network.driver.mtu":                  fmt.Sprint(mtu),
	}, map[string]string{"syncloud.managed": "true"})
	if err != nil {
		return mode, fmt.Errorf("docker network %s: %w", Network, err)
	}
	if err := applyNft(rules(cfg, subnet)); err != nil {
		return mode, err
	}
	m.ensureProbe(addr.Addr())
	return mode, nil
}

// ensureLink creates the WireGuard interface: the kernel module when present,
// otherwise an in-process userspace device (wireguard-go).
func (m *Manager) ensureLink() (string, netlink.Link, error) {
	if link, err := netlink.LinkByName(Interface); err == nil {
		mode := "kernel"
		if m.sys.dev != nil {
			mode = "userspace"
		}
		return mode, link, nil
	}
	// SYNCLOUD_WIREGUARD_MODE=userspace skips the kernel module (buggy or
	// restricted kernels, and tests).
	if os.Getenv("SYNCLOUD_WIREGUARD_MODE") != "userspace" {
		err := netlink.LinkAdd(&netlink.Wireguard{LinkAttrs: netlink.LinkAttrs{Name: Interface, MTU: mtu}})
		if err == nil {
			link, err := netlink.LinkByName(Interface)
			return "kernel", link, err
		}
		if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENODEV) && !strings.Contains(err.Error(), "not supported") {
			return "", nil, fmt.Errorf("create %s: %w", Interface, err)
		}
		m.log.Info("WireGuard kernel module not available; using userspace WireGuard")
	}
	tdev, err := tun.CreateTUN(Interface, mtu)
	if err != nil {
		return "userspace", nil, fmt.Errorf("create TUN %s: %w", Interface, err)
	}
	dev := device.NewDevice(tdev, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, "wireguard: "))
	f, err := ipc.UAPIOpen(Interface)
	if err != nil {
		dev.Close()
		return "userspace", nil, fmt.Errorf("WireGuard control socket: %w", err)
	}
	uapi, err := ipc.UAPIListen(Interface, f)
	if err != nil {
		dev.Close()
		return "userspace", nil, err
	}
	go func() {
		for {
			c, err := uapi.Accept()
			if err != nil {
				return
			}
			go dev.IpcHandle(c)
		}
	}()
	m.sys.dev, m.sys.uapi = dev, uapi
	link, err := netlink.LinkByName(Interface)
	return "userspace", link, err
}

func (m *Manager) configureWireGuard(cfg *agentv1.NetworkConfig) error {
	c, err := wgctrl.New()
	if err != nil {
		return err
	}
	defer c.Close()
	port := int(cfg.GetListenPort())
	keepalive := 25 * time.Second
	wc := wgtypes.Config{PrivateKey: &m.key, ListenPort: &port, ReplacePeers: true}
	for _, p := range cfg.GetPeers() {
		key, err := wgtypes.ParseKey(p.GetPublicKey())
		if err != nil {
			return fmt.Errorf("peer %s key: %w", p.GetName(), err)
		}
		pc := wgtypes.PeerConfig{PublicKey: key, ReplaceAllowedIPs: true, PersistentKeepaliveInterval: &keepalive}
		if p.GetEndpoint() != "" {
			ua, err := net.ResolveUDPAddr("udp", p.GetEndpoint())
			if err != nil {
				return fmt.Errorf("peer %s endpoint: %w", p.GetName(), err)
			}
			pc.Endpoint = ua
		}
		for _, a := range p.GetAllowedIps() {
			pfx, err := netip.ParsePrefix(a)
			if err != nil {
				return err
			}
			pc.AllowedIPs = append(pc.AllowedIPs, *ipnet(pfx))
		}
		wc.Peers = append(wc.Peers, pc)
	}
	if err := c.ConfigureDevice(Interface, wc); err != nil {
		return fmt.Errorf("configure WireGuard: %w", err)
	}
	return nil
}

func (m *Manager) peerStatus() []*agentv1.PeerStatus {
	c, err := wgctrl.New()
	if err != nil {
		return nil
	}
	defer c.Close()
	d, err := c.Device(Interface)
	if err != nil {
		return nil
	}
	out := make([]*agentv1.PeerStatus, 0, len(d.Peers))
	for _, p := range d.Peers {
		ps := &agentv1.PeerStatus{PublicKey: p.PublicKey.String(), RxBytes: uint64(p.ReceiveBytes), TxBytes: uint64(p.TransmitBytes)}
		if !p.LastHandshakeTime.IsZero() {
			ps.LastHandshakeUnix = p.LastHandshakeTime.Unix()
		}
		if p.Endpoint != nil {
			ps.Endpoint = p.Endpoint.String()
		}
		out = append(out, ps)
	}
	return out
}

// ensureProbe listens on the mesh address so peers can measure RTT.
func (m *Manager) ensureProbe(ip netip.Addr) {
	addr := net.JoinHostPort(ip.String(), ProbePort)
	if m.sys.probe != nil && m.sys.probeAddr == addr {
		return
	}
	if m.sys.probe != nil {
		m.sys.probe.Close()
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		m.log.Warn("mesh probe listener", "addr", addr, "err", err)
		return
	}
	m.sys.probe, m.sys.probeAddr = ln, addr
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
}

// rules is the agent's nftables table. It only touches its own table, so it
// coexists with Docker's iptables rules. Phase 1 adds the host firewall and
// service VIPs here (§8.3, §8.6).
func rules(cfg *agentv1.NetworkConfig, subnet netip.Prefix) string {
	internal := strings.Join([]string{cfg.GetMeshCidr(), cfg.GetContainerCidr(), cfg.GetServiceCidr()}, ", ")
	return fmt.Sprintf(`table ip syncloud
delete table ip syncloud
table ip syncloud {
	chain forward {
		type filter hook forward priority filter - 10; policy accept;
		# Container IPs are only reachable from the mesh and the local bridge.
		ip daddr %[1]s iifname != { "%[2]s", "%[3]s" } ct state new drop
	}
	chain postrouting {
		type nat hook postrouting priority srcnat + 10; policy accept;
		# No NAT inside the private network; masquerade to the internet.
		ip saddr %[4]s ip daddr != { %[5]s } oifname != "%[3]s" masquerade
	}
}
`, cfg.GetContainerCidr(), Interface, Bridge, subnet, internal)
}

func applyNft(ruleset string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(ruleset)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return errors.New("nft not found: install the nftables package")
		}
		return fmt.Errorf("nft: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func ipnet(p netip.Prefix) *net.IPNet {
	return &net.IPNet{IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())}
}
