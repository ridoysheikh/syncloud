package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
	"github.com/ridoysheikh/syncloud/internal/pki"
)

// Endpoint is one way to reach the gateway. With ServerName set it is a
// Traefik route (agent.<domain>:443): that name is sent for routing and the
// certificate is checked for pki.GatewayName.
type Endpoint struct {
	Address    string `json:"address"`
	ServerName string `json:"serverName,omitempty"`
}

// ViaTraefik reports whether e goes through Traefik.
func (e Endpoint) ViaTraefik() bool { return e.ServerName != "" }

// endpoints are the addresses to try, in order: the controller's list,
// then the address the node joined with as the last resort.
func (st State) endpoints() []Endpoint {
	out := slices.Clone(st.Gateways)
	if st.Gateway != "" && !slices.Contains(out, Endpoint{Address: st.Gateway}) {
		out = append(out, Endpoint{Address: st.Gateway})
	}
	return out
}

func endpointsFrom(list []*agentv1.GatewayEndpoint) []Endpoint {
	var out []Endpoint
	for _, g := range list {
		if _, _, err := net.SplitHostPort(g.GetAddress()); err == nil {
			out = append(out, Endpoint{Address: g.GetAddress(), ServerName: g.GetServerName()})
		}
	}
	return out
}

// endpointTLS is base set up for e: the cluster CA is the only trusted root
// either way.
func endpointTLS(base *tls.Config, e Endpoint) (*tls.Config, error) {
	c := base.Clone()
	if e.ServerName == "" {
		host, _, err := net.SplitHostPort(e.Address)
		if err != nil {
			return nil, fmt.Errorf("gateway address %q: %w", e.Address, err)
		}
		c.ServerName = host
		return c, nil
	}
	roots := c.RootCAs
	c.ServerName = e.ServerName
	c.InsecureSkipVerify = true // the default check would use ServerName; VerifyConnection checks the chain instead
	c.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("gateway sent no certificate")
		}
		inter := x509.NewCertPool()
		for _, ic := range cs.PeerCertificates[1:] {
			inter.AddCert(ic)
		}
		_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
			Roots: roots, Intermediates: inter, DNSName: pki.GatewayName,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		return err
	}
	return c, nil
}

// saveGateways stores the controller's list of gateway addresses.
func saveGateways(dataDir string, eps []Endpoint) error {
	path := filepath.Join(dataDir, stateFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	st.Gateways = eps
	out, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// CheckGateways opens a TLS connection to every gateway address and writes
// one line per address. It returns how many answered.
func CheckGateways(ctx context.Context, dataDir string, w io.Writer) (int, error) {
	st, base, _, err := loadState(dataDir)
	if err != nil {
		return 0, err
	}
	ok := 0
	for _, e := range st.endpoints() {
		label := e.Address
		if e.ViaTraefik() {
			label += " (Traefik, " + e.ServerName + ")"
		}
		start := time.Now()
		err := probeEndpoint(ctx, base, e)
		if err != nil {
			fmt.Fprintf(w, "✗ %s: %v\n", label, err)
			continue
		}
		ok++
		fmt.Fprintf(w, "✓ %s (%dms)\n", label, time.Since(start).Milliseconds())
	}
	return ok, nil
}

func probeEndpoint(ctx context.Context, base *tls.Config, e Endpoint) error {
	cfg, err := endpointTLS(base, e)
	if err != nil {
		return err
	}
	cfg.NextProtos = []string{"h2"}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	d := tls.Dialer{Config: cfg}
	c, err := d.DialContext(ctx, "tcp", e.Address)
	if err != nil {
		return err
	}
	return c.Close()
}
