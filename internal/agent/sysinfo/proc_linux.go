//go:build linux

package sysinfo

import (
	"bufio"
	"bytes"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func osName() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "Linux"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return "Linux"
}

func kernelRelease() string {
	var u syscall.Utsname
	if syscall.Uname(&u) != nil {
		return ""
	}
	b := make([]byte, 0, len(u.Release))
	for _, c := range u.Release {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

// cpuTimes returns idle (idle+iowait) and total jiffies from /proc/stat.
func cpuTimes() (idle, total uint64) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0
	}
	for i, v := range fields[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		if i >= 8 { // guest and guest_nice are already counted in user/nice
			break
		}
		total += n
		if i == 3 || i == 4 {
			idle += n
		}
	}
	return idle, total
}

type mem struct{ total, available uint64 }

func memInfo() mem {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return mem{}
	}
	var m mem
	for _, line := range bytes.Split(b, []byte("\n")) {
		f := strings.Fields(string(line))
		if len(f) < 2 {
			continue
		}
		kb, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			m.total = kb * 1024
		case "MemAvailable:":
			m.available = kb * 1024
		}
	}
	return m
}

func diskUsage(path string) (total, used uint64) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0, 0
	}
	total = st.Blocks * uint64(st.Bsize)
	free := st.Bfree * uint64(st.Bsize)
	return total, total - free
}

func loadAvg() (l1, l5, l15 float64) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return
	}
	f := strings.Fields(string(b))
	if len(f) >= 3 {
		l1, _ = strconv.ParseFloat(f[0], 64)
		l5, _ = strconv.ParseFloat(f[1], 64)
		l15, _ = strconv.ParseFloat(f[2], 64)
	}
	return
}

// netBytes sums rx/tx bytes over physical-ish interfaces. Loopback, Docker
// bridges and veths are skipped so container traffic isn't counted twice.
func netBytes() (rx, tx uint64) {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n")[2:] {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(f[0], 10, 64)
		t, _ := strconv.ParseUint(f[8], 10, 64)
		rx += r
		tx += t
	}
	return
}

func uptime() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return int64(v)
}
