package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultCloudflareAPI is Cloudflare's API.
const DefaultCloudflareAPI = "https://api.cloudflare.com/client/v4"

// Cloudflare is the Cloudflare DNS API with an API token. The recommended
// token has Zone:Zone:Read and Zone:DNS:Edit on the chosen zones only.
type Cloudflare struct {
	Token string
	Base  string
	HTTP  *http.Client
	// Sleep waits between retries (tests replace it).
	Sleep func(time.Duration)
}

func NewCloudflare(token, base string, hc *http.Client) *Cloudflare {
	if base == "" {
		base = DefaultCloudflareAPI
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Cloudflare{Token: token, Base: strings.TrimRight(base, "/"), HTTP: hc, Sleep: time.Sleep}
}

type cfEnvelope struct {
	Success bool            `json:"success"`
	Errors  []cfError       `json:"errors"`
	Result  json.RawMessage `json:"result"`
	Info    struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e cfEnvelope) message() string {
	var parts []string
	for _, x := range e.Errors {
		parts = append(parts, x.Message)
	}
	return strings.Join(parts, "; ")
}

// do makes one API call, retrying on 429 (and a transient 5xx) with the
// server's Retry-After or a short backoff.
func (c *Cloudflare) do(ctx context.Context, method, path string, q url.Values, body any) (cfEnvelope, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return cfEnvelope{}, err
		}
	}
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var last error
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
		if err != nil {
			return cfEnvelope{}, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return cfEnvelope{}, &Error{Provider: "Cloudflare", Message: err.Error()}
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		var env cfEnvelope
		_ = json.Unmarshal(raw, &env)
		switch {
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 && attempt < 2:
			wait := time.Duration(attempt+1) * time.Second
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s < 120 {
				wait = time.Duration(s) * time.Second
			}
			last = &Error{Provider: "Cloudflare", Status: resp.StatusCode, Message: firstNonEmpty(env.message(), resp.Status)}
			select {
			case <-ctx.Done():
				return cfEnvelope{}, ctx.Err()
			default:
			}
			c.Sleep(wait)
			continue
		case resp.StatusCode >= 300 || !env.Success:
			msg := env.message()
			if msg == "" {
				msg = resp.Status
			}
			return cfEnvelope{}, &Error{Provider: "Cloudflare", Status: resp.StatusCode, Message: msg}
		}
		return env, nil
	}
	return cfEnvelope{}, last
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (c *Cloudflare) Verify(ctx context.Context) error {
	// Works for user and account tokens alike: the token must see a zone.
	_, err := c.do(ctx, "GET", "/zones", url.Values{"per_page": {"1"}}, nil)
	return err
}

type cfZone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	NameServers []string `json:"name_servers"`
}

func (c *Cloudflare) Zones(ctx context.Context) ([]ZoneInfo, error) {
	var out []ZoneInfo
	for page := 1; page <= 100; page++ {
		env, err := c.do(ctx, "GET", "/zones", url.Values{"per_page": {"50"}, "page": {strconv.Itoa(page)}}, nil)
		if err != nil {
			return nil, err
		}
		var zs []cfZone
		if err := json.Unmarshal(env.Result, &zs); err != nil {
			return nil, err
		}
		for _, z := range zs {
			out = append(out, ZoneInfo{ID: z.ID, Name: Fqdn(z.Name), Status: z.Status, NameServers: z.NameServers})
		}
		if page >= env.Info.TotalPages {
			break
		}
	}
	return out, nil
}

type cfRecord struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Proxied  *bool  `json:"proxied,omitempty"`
	Priority *int   `json:"priority,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

func fromCF(r cfRecord) Record {
	out := Record{ID: r.ID, Type: strings.ToUpper(r.Type), Name: Fqdn(r.Name), Content: r.Content, TTL: r.TTL, Priority: r.Priority, Comment: r.Comment}
	if r.Proxied != nil {
		out.Proxied = *r.Proxied
	}
	return out
}

func toCF(r Record) cfRecord {
	out := cfRecord{Type: strings.ToUpper(r.Type), Name: Fqdn(r.Name), Content: r.Content, TTL: r.TTL, Priority: r.Priority, Comment: r.Comment}
	if out.TTL == 0 {
		out.TTL = 1
	}
	// Only these types can be proxied.
	if out.Type == "A" || out.Type == "AAAA" || out.Type == "CNAME" {
		p := r.Proxied
		out.Proxied = &p
	}
	return out
}

func (c *Cloudflare) Records(ctx context.Context, zoneID string) ([]Record, error) {
	var out []Record
	for page := 1; page <= 500; page++ {
		env, err := c.do(ctx, "GET", "/zones/"+url.PathEscape(zoneID)+"/dns_records", url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}, nil)
		if err != nil {
			return nil, err
		}
		var rs []cfRecord
		if err := json.Unmarshal(env.Result, &rs); err != nil {
			return nil, err
		}
		for _, r := range rs {
			out = append(out, fromCF(r))
		}
		if page >= env.Info.TotalPages {
			break
		}
	}
	return out, nil
}

func (c *Cloudflare) Create(ctx context.Context, zoneID string, r Record) (Record, error) {
	env, err := c.do(ctx, "POST", "/zones/"+url.PathEscape(zoneID)+"/dns_records", nil, toCF(r))
	if err != nil {
		return Record{}, err
	}
	var out cfRecord
	if err := json.Unmarshal(env.Result, &out); err != nil {
		return Record{}, err
	}
	return fromCF(out), nil
}

func (c *Cloudflare) Update(ctx context.Context, zoneID, id string, r Record) (Record, error) {
	env, err := c.do(ctx, "PUT", "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(id), nil, toCF(r))
	if err != nil {
		return Record{}, err
	}
	var out cfRecord
	if err := json.Unmarshal(env.Result, &out); err != nil {
		return Record{}, err
	}
	return fromCF(out), nil
}

func (c *Cloudflare) Delete(ctx context.Context, zoneID, id string) error {
	_, err := c.do(ctx, "DELETE", "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(id), nil, nil)
	return err
}

// OriginCertificate asks Cloudflare to sign an origin certificate (not
// publicly trusted: good only behind Cloudflare's proxy in Full (strict)).
func (c *Cloudflare) OriginCertificate(ctx context.Context, csrPEM string, hostnames []string, validityDays int) (string, error) {
	if validityDays == 0 {
		validityDays = 5475
	}
	env, err := c.do(ctx, "POST", "/certificates", nil, map[string]any{
		"hostnames": hostnames, "requested_validity": validityDays, "request_type": "origin-ecc", "csr": csrPEM,
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Certificate string `json:"certificate"`
	}
	if err := json.Unmarshal(env.Result, &out); err != nil {
		return "", err
	}
	if out.Certificate == "" {
		return "", &Error{Provider: "Cloudflare", Message: "no certificate in the answer"}
	}
	return out.Certificate, nil
}

var _ = fmt.Sprintf
