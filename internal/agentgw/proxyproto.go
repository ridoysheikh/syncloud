package agentgw

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ProxyListener accepts connections that start with a PROXY protocol v1
// header (Traefik passing agent.<domain>:443 on): RemoteAddr is then the
// agent's own address, which the mesh uses as its WireGuard endpoint. The
// header is read on first use, so a slow client never blocks Accept. A
// connection without a valid header fails.
func ProxyListener(ln net.Listener) net.Listener { return &proxyListener{ln} }

type proxyListener struct{ net.Listener }

func (l *proxyListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &proxyConn{Conn: c}, nil
}

type proxyConn struct {
	net.Conn
	once   sync.Once
	remote net.Addr
	err    error
}

// maxProxyHeader is v1's limit, CRLF included.
const maxProxyHeader = 107

func (c *proxyConn) init() {
	c.once.Do(func() {
		_ = c.Conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		defer c.Conn.SetReadDeadline(time.Time{})
		// Byte by byte: nothing after the header may be consumed.
		var line []byte
		b := make([]byte, 1)
		for len(line) < maxProxyHeader {
			if _, err := c.Conn.Read(b); err != nil {
				c.err = err
				return
			}
			line = append(line, b[0])
			if b[0] == '\n' {
				break
			}
		}
		c.remote, c.err = parseProxyV1(string(line))
	})
}

func (c *proxyConn) Read(p []byte) (int, error) {
	if c.init(); c.err != nil {
		return 0, c.err
	}
	return c.Conn.Read(p)
}

func (c *proxyConn) RemoteAddr() net.Addr {
	if c.init(); c.remote != nil {
		return c.remote
	}
	return c.Conn.RemoteAddr()
}

var errProxyHeader = errors.New("invalid PROXY protocol header")

// parseProxyV1 reads "PROXY TCP4 src dst sport dport\r\n" (or TCP6, or
// UNKNOWN, which keeps the connection's own address).
func parseProxyV1(line string) (net.Addr, error) {
	if !strings.HasSuffix(line, "\r\n") {
		return nil, errProxyHeader
	}
	f := strings.Fields(strings.TrimSuffix(line, "\r\n"))
	if len(f) < 2 || f[0] != "PROXY" {
		return nil, errProxyHeader
	}
	if f[1] == "UNKNOWN" {
		return nil, nil
	}
	if len(f) != 6 || (f[1] != "TCP4" && f[1] != "TCP6") {
		return nil, errProxyHeader
	}
	ip := net.ParseIP(f[2])
	port, err := strconv.Atoi(f[4])
	if ip == nil || err != nil || port < 0 || port > 65535 || (f[1] == "TCP4") != (ip.To4() != nil) {
		return nil, errProxyHeader
	}
	return &net.TCPAddr{IP: ip, Port: port}, nil
}
