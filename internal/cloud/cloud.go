// Package cloud creates and deletes servers through cloud provider APIs for
// provider-backed node pools (§6.5). Each provider implements Provider;
// servers carry a pool label so they can be listed back.
package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ServerSpec describes a server to create.
type ServerSpec struct {
	Name     string            `json:"name"`
	Region   string            `json:"region"`
	Type     string            `json:"type"`  // instance type / size
	Image    string            `json:"image"` // OS image
	SSHKeys  []string          `json:"sshKeys,omitempty"`
	UserData string            `json:"userData"` // cloud-init: joins the cluster
	Labels   map[string]string `json:"labels"`
}

// Server is a provider's server.
type Server struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	IP     string `json:"ip"`
}

// Provider is a cloud API.
type Provider interface {
	CreateServer(ctx context.Context, s ServerSpec) (Server, error)
	DeleteServer(ctx context.Context, id string) error
	// ListServers lists servers carrying a label.
	ListServers(ctx context.Context, labelKey, labelValue string) ([]Server, error)
}

// Config holds every provider type's settings; only the type's are used.
// Tokens and URLs are secrets (sealed at rest).
type Config struct {
	Token string `json:"token,omitempty"` // hetzner, digitalocean, webhook (optional bearer)
	URL   string `json:"url,omitempty"`   // webhook; base URL override for the others (tests)
}

// Types lists the supported providers.
var Types = []string{"hetzner", "digitalocean", "webhook"}

// Validate checks a configuration and returns a summary safe to show.
func (c Config) Validate(typ string) (string, error) {
	switch typ {
	case "hetzner", "digitalocean":
		if len(c.Token) < 16 {
			return "", errors.New("an API token is required")
		}
		return typ + " API token …" + c.Token[len(c.Token)-4:], nil
	case "webhook":
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", errors.New("url must be an http(s) URL")
		}
		return u.Host, nil
	}
	return "", fmt.Errorf("type must be one of %s", strings.Join(Types, ", "))
}

// New returns a provider client.
func New(typ string, c Config) (Provider, error) {
	h := &http.Client{Timeout: 60 * time.Second}
	switch typ {
	case "hetzner":
		base := c.URL
		if base == "" {
			base = "https://api.hetzner.cloud/v1"
		}
		return &hetzner{api{base: base, token: c.Token, http: h}}, nil
	case "digitalocean":
		base := c.URL
		if base == "" {
			base = "https://api.digitalocean.com/v2"
		}
		return &digitalOcean{api{base: base, token: c.Token, http: h}}, nil
	case "webhook":
		return &webhook{api{base: c.URL, token: c.Token, http: h}}, nil
	}
	return nil, fmt.Errorf("unknown provider type %q", typ)
}

type api struct {
	base, token string
	http        *http.Client
}

func (a api) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.base, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SynCloud")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) { // keep the URL (and its secrets) out of stored errors
			return fmt.Errorf("request to %s failed: %w", req.URL.Host, ue.Err)
		}
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// ── Hetzner Cloud ───────────────────────────────────────────────────────────

type hetzner struct{ api }

type hetznerServer struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	PublicNet struct {
		IPv4 struct {
			IP string `json:"ip"`
		} `json:"ipv4"`
	} `json:"public_net"`
}

func (h hetznerServer) server() Server {
	return Server{ID: strconv.FormatInt(h.ID, 10), Name: h.Name, Status: h.Status, IP: h.PublicNet.IPv4.IP}
}

func (p *hetzner) CreateServer(ctx context.Context, s ServerSpec) (Server, error) {
	body := map[string]any{"name": s.Name, "server_type": s.Type, "image": s.Image, "location": s.Region, "user_data": s.UserData, "labels": s.Labels,
		"start_after_create": true}
	if len(s.SSHKeys) > 0 {
		body["ssh_keys"] = s.SSHKeys
	}
	var out struct {
		Server hetznerServer `json:"server"`
	}
	if err := p.do(ctx, "POST", "/servers", body, &out); err != nil {
		return Server{}, err
	}
	return out.Server.server(), nil
}

func (p *hetzner) DeleteServer(ctx context.Context, id string) error {
	return p.do(ctx, "DELETE", "/servers/"+url.PathEscape(id), nil, nil)
}

func (p *hetzner) ListServers(ctx context.Context, k, v string) ([]Server, error) {
	var out struct {
		Servers []hetznerServer `json:"servers"`
	}
	if err := p.do(ctx, "GET", "/servers?label_selector="+url.QueryEscape(k+"="+v), nil, &out); err != nil {
		return nil, err
	}
	list := make([]Server, 0, len(out.Servers))
	for _, s := range out.Servers {
		list = append(list, s.server())
	}
	return list, nil
}

// ── DigitalOcean ────────────────────────────────────────────────────────────

type digitalOcean struct{ api }

type doDroplet struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Networks struct {
		V4 []struct {
			IP   string `json:"ip_address"`
			Type string `json:"type"`
		} `json:"v4"`
	} `json:"networks"`
}

func (d doDroplet) server() Server {
	s := Server{ID: strconv.FormatInt(d.ID, 10), Name: d.Name, Status: d.Status}
	for _, n := range d.Networks.V4 {
		if n.Type == "public" {
			s.IP = n.IP
		}
	}
	return s
}

// DigitalOcean has tags, not labels: "key:value".
func doTag(k, v string) string { return strings.ReplaceAll(k+":"+v, "/", "-") }

func (p *digitalOcean) CreateServer(ctx context.Context, s ServerSpec) (Server, error) {
	var tags []string
	for k, v := range s.Labels {
		tags = append(tags, doTag(k, v))
	}
	body := map[string]any{"name": s.Name, "region": s.Region, "size": s.Type, "image": s.Image, "user_data": s.UserData, "tags": tags}
	if len(s.SSHKeys) > 0 {
		body["ssh_keys"] = s.SSHKeys
	}
	var out struct {
		Droplet doDroplet `json:"droplet"`
	}
	if err := p.do(ctx, "POST", "/droplets", body, &out); err != nil {
		return Server{}, err
	}
	return out.Droplet.server(), nil
}

func (p *digitalOcean) DeleteServer(ctx context.Context, id string) error {
	return p.do(ctx, "DELETE", "/droplets/"+url.PathEscape(id), nil, nil)
}

func (p *digitalOcean) ListServers(ctx context.Context, k, v string) ([]Server, error) {
	var out struct {
		Droplets []doDroplet `json:"droplets"`
	}
	if err := p.do(ctx, "GET", "/droplets?tag_name="+url.QueryEscape(doTag(k, v)), nil, &out); err != nil {
		return nil, err
	}
	list := make([]Server, 0, len(out.Droplets))
	for _, d := range out.Droplets {
		list = append(list, d.server())
	}
	return list, nil
}

// ── generic webhook ─────────────────────────────────────────────────────────

// webhook calls one URL for every operation, for providers without a
// built-in plugin (or a script of your own):
//
//	POST {"action":"create", "server": ServerSpec}  -> Server
//	POST {"action":"delete", "id": "…"}             -> 2xx
//	POST {"action":"list", "label": {"key":…, "value":…}} -> [Server]
type webhook struct{ api }

func (p *webhook) CreateServer(ctx context.Context, s ServerSpec) (Server, error) {
	var out Server
	err := p.do(ctx, "POST", "", map[string]any{"action": "create", "server": s}, &out)
	if err == nil && out.ID == "" {
		err = errors.New("the webhook returned no server id")
	}
	return out, err
}

func (p *webhook) DeleteServer(ctx context.Context, id string) error {
	return p.do(ctx, "POST", "", map[string]any{"action": "delete", "id": id}, nil)
}

func (p *webhook) ListServers(ctx context.Context, k, v string) ([]Server, error) {
	var out []Server
	err := p.do(ctx, "POST", "", map[string]any{"action": "list", "label": map[string]string{"key": k, "value": v}}, &out)
	return out, err
}
