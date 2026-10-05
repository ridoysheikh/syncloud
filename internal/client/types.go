package client

import (
	"context"
	"time"
)

// Types mirror the OpenAPI schemas (internal/api/openapi.json).

type SystemStatus struct {
	Version       string `json:"version"`
	SetupRequired bool   `json:"setupRequired"`
	BaseDomain    string `json:"baseDomain"`
}

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	IsRoot    bool      `json:"isRoot"`
	CreatedAt time.Time `json:"createdAt"`
}

type AccessKey struct {
	ID              string     `json:"id"`
	Description     string     `json:"description"`
	CreatedAt       time.Time  `json:"createdAt"`
	LastUsedAt      *time.Time `json:"lastUsedAt"`
	LastUsedIP      string     `json:"lastUsedIp"`
	SecretAccessKey string     `json:"secretAccessKey,omitempty"`
}

type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	LastUsedIP string     `json:"lastUsedIp"`
	Token      string     `json:"token,omitempty"`
}

type list[T any] struct {
	Items []T `json:"items"`
}

func (c *Client) SystemStatus(ctx context.Context) (SystemStatus, error) {
	var out SystemStatus
	return out, c.Do(ctx, "GET", "/api/v1/system/status", nil, &out)
}

func (c *Client) Whoami(ctx context.Context) (User, error) {
	var out User
	return out, c.Do(ctx, "GET", "/api/v1/auth/me", nil, &out)
}

func (c *Client) ListAccessKeys(ctx context.Context) ([]AccessKey, error) {
	var out list[AccessKey]
	return out.Items, c.Do(ctx, "GET", "/api/v1/iam/access-keys", nil, &out)
}

func (c *Client) CreateAccessKey(ctx context.Context, description string) (AccessKey, error) {
	var out AccessKey
	return out, c.Do(ctx, "POST", "/api/v1/iam/access-keys", map[string]string{"description": description}, &out)
}

func (c *Client) DeleteAccessKey(ctx context.Context, id string) error {
	return c.Do(ctx, "DELETE", "/api/v1/iam/access-keys/"+id, nil, nil)
}

func (c *Client) ListTokens(ctx context.Context) ([]Token, error) {
	var out list[Token]
	return out.Items, c.Do(ctx, "GET", "/api/v1/iam/tokens", nil, &out)
}

func (c *Client) CreateToken(ctx context.Context, name string, expiresInDays int) (Token, error) {
	var out Token
	return out, c.Do(ctx, "POST", "/api/v1/iam/tokens", map[string]any{"name": name, "expiresInDays": expiresInDays}, &out)
}

func (c *Client) DeleteToken(ctx context.Context, id string) error {
	return c.Do(ctx, "DELETE", "/api/v1/iam/tokens/"+id, nil, nil)
}
