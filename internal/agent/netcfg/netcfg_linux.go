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

	"github.com/ridoysheikh/syncloud/internal/firewall"
	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
)

// platform holds the userspace WireGuard device (when the kernel module is
// missing) and the RTT probe listener.
type platform struct {
	lastRuleset  string // live listing after the last apply (drift detection)
	lastRendered string // what was applied
	isolated     bool   // bridge ports are isolated (security groups on)
	watching     bool   // new bridge ports are isolated as they appear
	dev          *device.Device
	uapi         net.Listener
	probe        net.Listener
	probeAddr    string
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
	// Tasks resolve through the agent's DNS on the bridge gateway (§8.1).
	if err := m.dns.Listen(gateway.String()); err != nil {
		return mode, fmt.Errorf("DNS server on %s: %w", gateway, err)
	}
	var sec *firewall.Security
	secErr := ""
	if pol := m.security(); pol != nil {
		sec = &firewall.Security{Policy: pol}
		if sec.Local, err = m.localContainers(ctx); err != nil {
			secErr = "listing local containers: " + err.Error()
		}
	}
	if err := m.isolatePorts(sec != nil); err != nil {
		secErr = "isolating bridge ports: " + err.Error()
	}
	ruleset, err := firewall.RenderWith(cfg, m.firewallFor(cfg), m.services(), sec)
	if err != nil {
		return mode, err
	}
	m.countMu.Lock()
	m.secErr = secErr
	drift := m.checkDrift()
	if drift || ruleset != m.sys.lastRendered {
		rules, _, _ := readCounters()
		m.collect()
		if err := applyNft(ruleset); err != nil {
			m.countMu.Unlock()
			return mode, err
		}
		m.replaced(rules)
		m.sys.lastRendered = ruleset
		m.rememberRuleset()
	}
	m.countMu.Unlock()
	m.ensureProbe(addr.Addr())
	return mode, nil
}

// localContainers lists this node's task containers on the private network
// with their labels, so they join their security sets at once (§8.3).
func (m *Manager) localContainers(ctx context.Context) ([]firewall.LocalContainer, error) {
	list, err := m.docker.List(ctx, "syncloud.managed=true")
	if err != nil {
		return nil, err
	}
	var out []firewall.LocalContainer
	for _, c := range list {
		if c.NetworkSettings == nil {
			continue
		}
		if n, ok := c.NetworkSettings.Networks[Network]; ok && n.IPAddress != "" {
			out = append(out, firewall.LocalContainer{IP: n.IPAddress, Labels: c.Labels})
		}
	}
	return out, nil
}

// isolatePorts makes containers on the bridge unable to reach each other
// directly: the host answers ARP for the subnet (proxy_arp_pvlan) and routes
// between them, so same-node traffic passes the forward chain like traffic
// between nodes. This needs no extra kernel modules (br_netfilter).
func (m *Manager) isolatePorts(on bool) error {
	if !on && !m.sys.isolated {
		return nil
	}
	br, err := netlink.LinkByName(Bridge)
	if err != nil {
		return err
	}
	val := "0"
	if on {
		val = "1"
	}
	for _, f := range []string{"proxy_arp", "proxy_arp_pvlan"} {
		if err := os.WriteFile("/proc/sys/net/ipv4/conf/"+Bridge+"/"+f, []byte(val), 0o644); err != nil {
			return err
		}
	}
	if on {
		// Redirects would tell containers to talk to each other directly.
		for _, dev := range []string{Bridge, "all"} {
			_ = os.WriteFile("/proc/sys/net/ipv4/conf/"+dev+"/send_redirects", []byte("0"), 0o644)
		}
	}
	links, err := netlink.LinkList()
	if err != nil {
		return err
	}
	for _, l := range links {
		if l.Attrs().MasterIndex == br.Attrs().Index && l.Attrs().Protinfo != nil && l.Attrs().Protinfo.Isolated == on {
			continue
		}
		if l.Attrs().MasterIndex == br.Attrs().Index {
			if err := netlink.LinkSetIsolated(l, on); err != nil {
				return fmt.Errorf("%s: %w", l.Attrs().Name, err)
			}
		}
	}
	m.mu.Lock()
	m.sys.isolated = on
	m.mu.Unlock()
	if on && !m.sys.watching {
		m.sys.watching = true
		go m.watchPorts(br.Attrs().Index)
	}
	return nil
}

// watchPorts isolates new bridge ports (a starting container's veth) as
// soon as they are attached, before the container's process runs.
func (m *Manager) watchPorts(bridgeIndex int) {
	ch := make(chan netlink.LinkUpdate, 64)
	done := make(chan struct{})
	if err := netlink.LinkSubscribe(ch, done); err != nil {
		m.log.Warn("watch bridge ports", "err", err)
		return
	}
	for u := range ch {
		m.mu.Lock()
		on := m.sys.isolated
		m.mu.Unlock()
		if !on || u.Link == nil || u.Link.Attrs().MasterIndex != bridgeIndex {
			continue
		}
		if p := u.Link.Attrs().Protinfo; p != nil && p.Isolated {
			continue
		}
		if err := netlink.LinkSetIsolated(u.Link, true); err != nil {
			m.log.Warn("isolate bridge port", "port", u.Link.Attrs().Name, "err", err)
		}
	}
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

// listTable returns the live table without counters or drop-log entries,
// for drift detection.
func listTable() string {
	out, err := exec.Command("nft", "-s", "list", "table", "inet", "syncloud").Output()
	if err != nil {
		return ""
	}
	return firewall.StripDynamic(string(out))
}

// readCounters reads rule counters and drop-log sets from the live table.
func readCounters() (map[string]firewall.Counts, map[firewall.DropKey]uint64, bool) {
	out, err := exec.Command("nft", "-j", "list", "table", "inet", "syncloud").Output()
	if err != nil {
		return nil, nil, false
	}
	rules, drops, err := firewall.ParseCounters(out)
	return rules, drops, err == nil
}

// checkDrift compares the live table with what was applied last time.
func (m *Manager) checkDrift() bool {
	if m.sys.lastRuleset == "" {
		return false
	}
	if live := listTable(); live != m.sys.lastRuleset {
		m.mu.Lock()
		m.drift++
		m.mu.Unlock()
		m.log.Warn("managed nftables rules were changed outside SynCloud; restoring them")
		return true
	}
	return false
}

func (m *Manager) rememberRuleset() { m.sys.lastRuleset = listTable() }
