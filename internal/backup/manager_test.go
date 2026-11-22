package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"

	"github.com/minio/minio-go/v7"
)

// A key rotated at the provider shows up as a signature error; the backup
// status says what to do instead of only the provider's text.
func TestExplainS3(t *testing.T) {
	c := Config{AccessKeyID: "AKIA1", Bucket: "b"}
	for code, want := range map[string]string{
		"SignatureDoesNotMatch": "rotated at the provider",
		"InvalidAccessKeyId":    "does not know access key ID AKIA1",
		"AccessDenied":          "may not write to bucket b",
	} {
		err := explainS3(minio.ErrorResponse{Code: code, Message: "provider text"}, c)
		if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "provider text") {
			t.Errorf("%s: %v", code, err)
		}
	}
	other := errors.New("dial tcp: refused")
	if explainS3(other, c) != other {
		t.Error("other errors are changed")
	}
}

// With an S3 connection, backups read its keys at every run: a key rotated
// on the connection is used at once, and no copy of it is stored.
func TestConnectionKeysAreLive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	box, _, err := secrets.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(dir, "syncloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	secret := "old-secret"
	conn := func(_ context.Context, ref string) (Creds, error) {
		if ref != "s3e_1" && ref != "minio" {
			return Creds{}, errors.New("not found")
		}
		return Creds{ID: "s3e_1", Name: "minio", URL: "http://10.0.0.5:9000", AccessKeyID: "AK", SecretAccessKey: secret}, nil
	}
	if err := st.SetSetting(ctx, settingConfig, `{"endpointId":"s3e_1","bucket":"b","intervalMinutes":60,"retain":48}`); err != nil {
		t.Fatal(err)
	}
	m := NewManager(st, box, dir, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Connection = conn
	if err := m.Load(ctx); err != nil {
		t.Fatalf("load without a stored secret: %v", err)
	}
	c, err := m.live(ctx)
	if err != nil || c.SecretAccessKey != "old-secret" || c.Endpoint != "http://10.0.0.5:9000" {
		t.Fatalf("live: %+v %v", c, err)
	}
	secret = "rotated-secret"
	if c, _ := m.live(ctx); c.SecretAccessKey != "rotated-secret" {
		t.Fatalf("rotated key not used: %q", c.SecretAccessKey)
	}
	if v, _ := m.Config(); v.SecretAccessKey != "" || v.EndpointName != "minio" || v.AccessKeyID != "AK" {
		t.Fatalf("config view: %+v", v)
	}
	if by := m.EndpointInUse(ctx, "s3e_1"); by == "" {
		t.Fatal("the connection can be deleted while backups use it")
	}
	if _, ok, _ := st.GetSetting(ctx, settingSecret); ok {
		t.Fatal("a secret is stored for a connection")
	}
	// A deleted connection fails the run with a clear error.
	m.Connection = func(context.Context, string) (Creds, error) { return Creds{}, errors.New("not found") }
	if _, err := m.RunNow(ctx); err == nil || !strings.Contains(err.Error(), "S3 connection s3e_1") {
		t.Fatalf("run without the connection: %v", err)
	}
}

// Backups set up with entered keys move to the registered connection with
// the same address and key ID; once their key is refused, to the only
// connection with the same address.
func TestMatchConnection(t *testing.T) {
	m := &Manager{cfg: &Config{Endpoint: "https://S3.example.com/", AccessKeyID: "OLD", Bucket: "b"}}
	m.Connection = func(context.Context, string) (Creds, error) { return Creds{}, nil }
	conns := []Creds{{ID: "s3e_1", URL: "https://s3.example.com", AccessKeyID: "NEW"}, {ID: "s3e_2", URL: "https://other.example.com", AccessKeyID: "OLD"}}
	m.Connections = func(context.Context) ([]Creds, error) { return conns, nil }
	ctx := context.Background()
	if _, ok := m.matchConnection(ctx, false); ok {
		t.Fatal("a working key was replaced by another key")
	}
	if c, ok := m.matchConnection(ctx, true); !ok || c.ID != "s3e_1" {
		t.Fatalf("refused key: %+v %v", c, ok)
	}
	conns = append(conns, Creds{ID: "s3e_3", URL: "https://s3.example.com", AccessKeyID: "OLD"})
	if c, ok := m.matchConnection(ctx, false); !ok || c.ID != "s3e_3" {
		t.Fatalf("same key ID: %+v %v", c, ok)
	}
	conns = conns[:2]
	conns = append(conns, Creds{ID: "s3e_4", URL: "https://s3.example.com", AccessKeyID: "OTHER"})
	if _, ok := m.matchConnection(ctx, true); ok {
		t.Fatal("picked one of two connections to the same address")
	}
	m.cfg.EndpointID = "s3e_1"
	if _, ok := m.matchConnection(ctx, true); ok {
		t.Fatal("a connection setting was matched again")
	}
	if !keyRejected(fmt.Errorf("upload: %w", minio.ErrorResponse{Code: "SignatureDoesNotMatch"})) || keyRejected(errors.New("timeout")) {
		t.Fatal("keyRejected")
	}
}
