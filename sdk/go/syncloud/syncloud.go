// Package syncloud is the Go SDK for the SynCloud API (§7.1): the same
// client synctl uses, with access-key request signing, bearer tokens and
// temporary credentials.
//
//	c, err := syncloud.New("https://203-0-113-10.sslip.io", syncloud.Credentials{
//		AccessKeyID: os.Getenv("SYNCLOUD_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("SYNCLOUD_SECRET_ACCESS_KEY"),
//	})
//	svcs, err := c.ListServices(ctx, "shop", "production")
package syncloud

import "github.com/ridoysheikh/syncloud/internal/client"

type (
	Client      = client.Client
	Credentials = client.Credentials
	Error       = client.Error
)

// HeaderSessionToken carries temporary credentials' session token.
const HeaderSessionToken = client.HeaderSessionToken

// New returns a client for an endpoint such as https://203-0-113-10.sslip.io.
func New(endpoint string, creds Credentials) (*Client, error) { return client.New(endpoint, creds) }
