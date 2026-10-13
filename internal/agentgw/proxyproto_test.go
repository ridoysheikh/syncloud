package agentgw

import (
	"io"
	"net"
	"testing"
)

func TestParseProxyV1(t *testing.T) {
	for line, want := range map[string]string{
		"PROXY TCP4 203.0.113.9 10.0.0.1 51000 443\r\n":   "203.0.113.9:51000",
		"PROXY TCP6 2001:db8::1 2001:db8::2 4000 443\r\n": "[2001:db8::1]:4000",
		"PROXY UNKNOWN\r\n": "",
	} {
		a, err := parseProxyV1(line)
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if got := ""; a != nil {
			got = a.String()
			if got != want {
				t.Errorf("%q: %s, want %s", line, got, want)
			}
		} else if want != "" {
			t.Errorf("%q: no address, want %s", line, want)
		}
	}
	for _, bad := range []string{
		"PROXY TCP4 203.0.113.9 10.0.0.1 51000 443\n",
		"PROXY TCP4 2001:db8::1 10.0.0.1 1 2\r\n",
		"PROXY TCP4 nope 10.0.0.1 1 2\r\n",
		"PROXY TCP4 203.0.113.9 10.0.0.1 99999 443\r\n",
		"\x16\x03\x01\x00", // TLS without a header
		"GET / HTTP/1.1\r\n",
	} {
		if _, err := parseProxyV1(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The header is consumed and nothing after it: the TLS bytes stay intact.
func TestProxyListenerKeepsPayload(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pl := ProxyListener(ln)
	defer pl.Close()
	go func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			return
		}
		defer c.Close()
		io.WriteString(c, "PROXY TCP4 198.51.100.7 10.0.0.1 1234 443\r\nhello")
	}()
	c, err := pl.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := c.RemoteAddr().String(); got != "198.51.100.7:1234" {
		t.Fatalf("remote %s", got)
	}
	b, _ := io.ReadAll(c)
	if string(b) != "hello" {
		t.Fatalf("payload %q", b)
	}
}
