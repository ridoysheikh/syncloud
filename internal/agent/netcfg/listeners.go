package netcfg

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
)

// Listeners reads the host's listening sockets from proc (normally "/proc"):
// TCP sockets in LISTEN state and unconnected UDP sockets, with the name of
// the process that owns each when it can be found.
func Listeners(proc string) ([]*agentv1.Listener, error) {
	owners := socketOwners(proc)
	seen := map[string]bool{}
	var out []*agentv1.Listener
	found := false
	for _, f := range []struct{ file, proto string }{{"tcp", "tcp"}, {"tcp6", "tcp"}, {"udp", "udp"}, {"udp6", "udp"}} {
		ls, err := parseSockets(filepath.Join(proc, "net", f.file), f.proto)
		if err != nil {
			continue
		}
		found = true
		for _, l := range ls {
			l.Process = owners[l.Process] // Process holds the inode until here
			key := fmt.Sprintf("%s|%s|%d", l.Protocol, l.Address, l.Port)
			if !seen[key] {
				seen[key] = true
				out = append(out, l)
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("no socket tables under %s/net", proc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Protocol+out[i].Address < out[j].Protocol+out[j].Address
	})
	return out, nil
}

// parseSockets reads one /proc/net table. The listener's Process field
// carries the socket inode for the caller to resolve.
func parseSockets(path, proto string) ([]*agentv1.Listener, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []*agentv1.Listener
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		local, remote, state, inode := fields[1], fields[2], fields[3], fields[9]
		switch {
		case proto == "tcp" && state != "0A": // LISTEN
			continue
		case proto == "udp" && !strings.HasSuffix(remote, ":0000"): // connected
			continue
		}
		addr, port, ok := parseHexAddr(local)
		if !ok {
			continue
		}
		out = append(out, &agentv1.Listener{Protocol: proto, Address: addr.String(), Port: port, Process: inode})
	}
	return out, sc.Err()
}

// parseHexAddr decodes "0100007F:1F90" (IPv4) or the 32-digit IPv6 form:
// each 32-bit word is in host (little-endian) order.
func parseHexAddr(s string) (netip.Addr, uint32, bool) {
	h, p, ok := strings.Cut(s, ":")
	if !ok {
		return netip.Addr{}, 0, false
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	raw, err := hex.DecodeString(h)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.Addr{}, 0, false
	}
	for i := 0; i < len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	addr, _ := netip.AddrFromSlice(raw)
	return addr.Unmap(), uint32(port), true
}

// socketOwners maps socket inodes to the name of a process holding them.
func socketOwners(proc string) map[string]string {
	out := map[string]string{}
	pids, _ := os.ReadDir(proc)
	for _, p := range pids {
		if _, err := strconv.Atoi(p.Name()); err != nil {
			continue
		}
		dir := filepath.Join(proc, p.Name())
		fds, err := os.ReadDir(filepath.Join(dir, "fd"))
		if err != nil {
			continue
		}
		var comm string
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(target, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			if _, ok := out[inode]; ok {
				continue
			}
			if comm == "" {
				b, _ := os.ReadFile(filepath.Join(dir, "comm"))
				comm = strings.TrimSpace(string(b))
			}
			out[inode] = comm
		}
	}
	return out
}
