package netcfg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListenersFromProc(t *testing.T) {
	proc := t.TempDir()
	write := func(name, content string) {
		p := filepath.Join(proc, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	head := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	write("net/tcp", head+
		"   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1111 1 0 100 0 0 10 0\n"+
		"   1: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2222 1 0 100 0 0 10 0\n"+
		"   2: 0100007F:1F90 0100007F:D431 01 00000000:00000000 00:00000000 00000000     0        0 3333 1 0 100 0 0 10 0\n")
	write("net/tcp6", head+
		"   0: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 4444 1 0 100 0 0 10 0\n")
	write("net/udp", head+
		"   0: 00000000:CA6C 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 5555 2 0\n")
	write("123/comm", "sshd\n")
	_ = os.MkdirAll(filepath.Join(proc, "123", "fd"), 0o755)
	if err := os.Symlink("socket:[1111]", filepath.Join(proc, "123", "fd", "3")); err != nil {
		t.Fatal(err)
	}
	ls, err := Listeners(proc)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, l := range ls {
		got[l.GetProtocol()+" "+l.GetAddress()+" "+itoa(l.GetPort())] = l.GetProcess()
	}
	want := map[string]string{"tcp 0.0.0.0 22": "sshd", "tcp 127.0.0.1 8080": "", "tcp :: 443": "", "udp 0.0.0.0 51820": ""}
	if len(got) != len(want) {
		t.Fatalf("listeners %v", got)
	}
	for k, v := range want {
		if p, ok := got[k]; !ok || p != v {
			t.Errorf("%s: %q, %v (all %v)", k, p, ok, got)
		}
	}
}

func itoa(n uint32) string {
	b := []byte{}
	for {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
		if n == 0 {
			return string(b)
		}
	}
}
