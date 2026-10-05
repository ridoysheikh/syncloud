// Package client is the Go client for the SynCloud API, used by synctl
// (and published as the Go SDK later, §7.1). It signs requests with an access
// key, or sends a personal access token as a Bearer credential.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"syncloud/internal/sigv"
)

type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	// Token is a personal access token; used only when no access key is set.
	Token string
}

type Client struct {
	endpoint *url.URL
	creds    Credentials
	http     *http.Client
	now      func() time.Time
}

// New returns a client for endpoint, e.g. "https://203-0-113-10.sslip.io".
func New(endpoint string, creds Credentials) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid endpoint %q: want http(s)://host[:port]", endpoint)
	}
	return &Client{endpoint: u, creds: creds, http: &http.Client{Timeout: 60 * time.Second}, now: time.Now}, nil
}

// Error is an API error response.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("%s (HTTP %d): %s", e.Code, e.Status, e.Message) }

// Raw sends a request with an optional raw JSON body and returns the response
// status and body. Paths are relative to the endpoint, e.g. "/api/v1/auth/me".
func (c *Client) Raw(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	ref, err := url.Parse(path)
	if err != nil {
		return 0, nil, err
	}
	u := c.endpoint.ResolveReference(ref)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	switch {
	case c.creds.AccessKeyID != "":
		sigv.Sign(req, c.creds.AccessKeyID, c.creds.SecretAccessKey, body, c.now())
	case c.creds.Token != "":
		req.Header.Set("Authorization", "Bearer "+c.creds.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return resp.StatusCode, out, err
}

// Do sends in (if non-nil) as JSON and decodes a successful response into out (if non-nil).
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	status, data, err := c.Raw(ctx, method, path, body)
	if err != nil {
		return err
	}
	if status >= 400 {
		return decodeError(status, data)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func decodeError(status int, data []byte) error {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &env) != nil || env.Error.Code == "" {
		return &Error{Status: status, Code: "http_error", Message: strings.TrimSpace(string(data))}
	}
	return &Error{Status: status, Code: env.Error.Code, Message: env.Error.Message}
}

// Stream sends a signed GET and returns the open response body for
// streaming endpoints (e.g. Server-Sent Events). The caller closes it.
func (c *Client) Stream(ctx context.Context, path string) (io.ReadCloser, error) {
	ref, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint.ResolveReference(ref).String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	switch {
	case c.creds.AccessKeyID != "":
		sigv.Sign(req, c.creds.AccessKeyID, c.creds.SecretAccessKey, nil, c.now())
	case c.creds.Token != "":
		req.Header.Set("Authorization", "Bearer "+c.creds.Token)
	}
	// Streams outlive the client's request timeout.
	hc := *c.http
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp.Body, nil
}
