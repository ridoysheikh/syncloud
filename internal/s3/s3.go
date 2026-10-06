// Package s3 manages S3 endpoints, service bindings and the bucket browser
// (§16). SynCloud has no shared volumes: anything shared or durable goes to
// an S3-compatible store, external (AWS, R2, B2, …) or self-hosted (MinIO or
// Garage deployed as a service and registered here like any other).
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"syncloud/internal/auth"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
)

// Endpoint is an endpoint as the API shows it (never the secret).
type Endpoint struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Region      string    `json:"region"`
	AccessKeyID string    `json:"accessKeyId"`
	PathStyle   bool      `json:"pathStyle"`
	CreatedAt   time.Time `json:"createdAt"`
	Bindings    int       `json:"bindings"`
}

// EndpointInput creates or updates an endpoint. An empty secret on update
// keeps the stored one.
type EndpointInput struct {
	Name            string `json:"name"`
	URL             string `json:"url"`
	Region          string `json:"region"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	PathStyle       bool   `json:"pathStyle"`
}

// ErrInvalid wraps validation errors.
type ErrInvalid struct{ Err error }

func (e ErrInvalid) Error() string { return e.Err.Error() }

var (
	nameRE      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	bucketRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	envPrefixRE = regexp.MustCompile(`^([A-Z][A-Z0-9_]{0,30}_)?$`)
)

type Manager struct {
	st  *store.Store
	box *secrets.Box
	now func() time.Time
}

func New(st *store.Store, box *secrets.Box) *Manager {
	return &Manager{st: st, box: box, now: time.Now}
}

func aad(id string) []byte { return []byte("s3-endpoint:" + id) }

func view(e store.S3Endpoint, bindings int) Endpoint {
	return Endpoint{ID: e.ID, Name: e.Name, URL: e.URL, Region: e.Region, AccessKeyID: e.AccessKeyID, PathStyle: e.PathStyle, CreatedAt: e.CreatedAt, Bindings: bindings}
}

func (m *Manager) List(ctx context.Context) ([]Endpoint, error) {
	es, err := m.st.ListS3Endpoints(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Endpoint, 0, len(es))
	for _, e := range es {
		bs, err := m.st.EndpointS3Bindings(ctx, e.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, view(e, len(bs)))
	}
	return out, nil
}

func (m *Manager) Get(ctx context.Context, ref string) (Endpoint, error) {
	e, err := m.st.S3EndpointByRef(ctx, ref)
	if err != nil {
		return Endpoint{}, err
	}
	bs, err := m.st.EndpointS3Bindings(ctx, e.ID)
	if err != nil {
		return Endpoint{}, err
	}
	return view(e, len(bs)), nil
}

func normalizeURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("url must be http(s)://host[:port] without a path")
	}
	return u.Scheme + "://" + u.Host, nil
}

// Put creates (ref == "") or updates an endpoint, after checking that the
// credentials work.
func (m *Manager) Put(ctx context.Context, ref string, in EndpointInput) (Endpoint, error) {
	var e store.S3Endpoint
	var secret string
	if ref != "" {
		cur, err := m.st.S3EndpointByRef(ctx, ref)
		if err != nil {
			return Endpoint{}, err
		}
		e = cur
		if in.SecretAccessKey == "" {
			b, err := m.box.Open(cur.SecretEnc, aad(cur.ID))
			if err != nil {
				return Endpoint{}, err
			}
			secret = string(b)
		}
	} else {
		e = store.S3Endpoint{ID: auth.NewID("s3e_"), CreatedAt: m.now().UTC().Truncate(time.Second)}
	}
	if in.SecretAccessKey != "" {
		secret = in.SecretAccessKey
	}
	if !nameRE.MatchString(in.Name) {
		return Endpoint{}, ErrInvalid{errors.New("name must be lowercase letters, digits and hyphens (at most 32)")}
	}
	u, err := normalizeURL(in.URL)
	if err != nil {
		return Endpoint{}, ErrInvalid{err}
	}
	if in.AccessKeyID == "" || secret == "" {
		return Endpoint{}, ErrInvalid{errors.New("accessKeyId and secretAccessKey are required")}
	}
	e.Name, e.URL, e.Region, e.AccessKeyID, e.PathStyle = in.Name, u, strings.TrimSpace(in.Region), strings.TrimSpace(in.AccessKeyID), in.PathStyle
	cl, err := client(e, secret)
	if err != nil {
		return Endpoint{}, ErrInvalid{err}
	}
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := cl.ListBuckets(tctx); err != nil {
		return Endpoint{}, ErrInvalid{fmt.Errorf("cannot list buckets with these credentials: %w", err)}
	}
	e.SecretEnc = m.box.Seal([]byte(secret), aad(e.ID))
	if err := m.st.PutS3Endpoint(ctx, e); err != nil {
		return Endpoint{}, err
	}
	return m.Get(ctx, e.ID)
}

func (m *Manager) Delete(ctx context.Context, ref string) error {
	e, err := m.st.S3EndpointByRef(ctx, ref)
	if err != nil {
		return err
	}
	return m.st.DeleteS3Endpoint(ctx, e.ID)
}

func client(e store.S3Endpoint, secret string) (*minio.Client, error) {
	u, err := url.Parse(e.URL)
	if err != nil {
		return nil, err
	}
	lookup := minio.BucketLookupAuto
	if e.PathStyle {
		lookup = minio.BucketLookupPath
	}
	return minio.New(u.Host, &minio.Options{
		Creds: credentials.NewStaticV4(e.AccessKeyID, secret, ""), Secure: u.Scheme == "https", Region: e.Region, BucketLookup: lookup,
	})
}

// Creds are what a bound task receives.
type Creds struct {
	URL, Region, AccessKeyID, SecretAccessKey string
	PathStyle                                 bool
}

// Credentials unseals an endpoint's credentials (by ID or name).
func (m *Manager) Credentials(ctx context.Context, ref string) (Creds, error) {
	e, err := m.st.S3EndpointByRef(ctx, ref)
	if err != nil {
		return Creds{}, err
	}
	b, err := m.box.Open(e.SecretEnc, aad(e.ID))
	if err != nil {
		return Creds{}, err
	}
	return Creds{URL: e.URL, Region: e.Region, AccessKeyID: e.AccessKeyID, SecretAccessKey: string(b), PathStyle: e.PathStyle}, nil
}

func (m *Manager) client(ctx context.Context, ref string) (*minio.Client, error) {
	e, err := m.st.S3EndpointByRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	b, err := m.box.Open(e.SecretEnc, aad(e.ID))
	if err != nil {
		return nil, err
	}
	return client(e, string(b))
}

// Bucket is a bucket and the services bound to it.
type Bucket struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	BoundBy   []string  `json:"boundBy"` // project/env/service
}

func (m *Manager) Buckets(ctx context.Context, ref string) ([]Bucket, error) {
	e, err := m.st.S3EndpointByRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	cl, err := m.client(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	bs, err := cl.ListBuckets(ctx)
	if err != nil {
		return nil, err
	}
	bindings, err := m.st.EndpointS3Bindings(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	by := map[string][]string{}
	for _, b := range bindings {
		by[b.Bucket] = append(by[b.Bucket], b.Project+"/"+b.Environment+"/"+b.Service)
	}
	out := make([]Bucket, 0, len(bs))
	for _, b := range bs {
		out = append(out, Bucket{Name: b.Name, CreatedAt: b.CreationDate.UTC(), BoundBy: append([]string{}, by[b.Name]...)})
	}
	return out, nil
}

func (m *Manager) CreateBucket(ctx context.Context, ref, name string) error {
	if !bucketRE.MatchString(name) {
		return ErrInvalid{errors.New("bucket names are 3–63 lowercase letters, digits, dots and hyphens")}
	}
	e, err := m.st.S3EndpointByRef(ctx, ref)
	if err != nil {
		return err
	}
	cl, err := m.client(ctx, e.ID)
	if err != nil {
		return err
	}
	return cl.MakeBucket(ctx, name, minio.MakeBucketOptions{Region: e.Region})
}

// Object is one entry of a listing: an object or a "folder" (common prefix).
type Object struct {
	Key          string     `json:"key"`
	Folder       bool       `json:"folder"`
	Size         int64      `json:"size"`
	LastModified *time.Time `json:"lastModified,omitempty"`
	ContentType  string     `json:"contentType,omitempty"`
}

// Listing is one page of a bucket under a prefix.
type Listing struct {
	Prefix    string   `json:"prefix"`
	Objects   []Object `json:"objects"`
	Truncated bool     `json:"truncated"`
}

const pageSize = 1000

// Objects lists one level under prefix ("folders" first), at most 1000
// entries starting after startAfter.
func (m *Manager) Objects(ctx context.Context, ref, bucket, prefix, startAfter string) (Listing, error) {
	cl, err := m.client(ctx, ref)
	if err != nil {
		return Listing{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := Listing{Prefix: prefix, Objects: []Object{}}
	for o := range cl.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, StartAfter: startAfter}) {
		if o.Err != nil {
			return out, o.Err
		}
		if len(out.Objects) == pageSize {
			out.Truncated = true
			break
		}
		if strings.HasSuffix(o.Key, "/") && o.Size == 0 && o.LastModified.IsZero() {
			out.Objects = append(out.Objects, Object{Key: o.Key, Folder: true})
			continue
		}
		at := o.LastModified.UTC()
		out.Objects = append(out.Objects, Object{Key: o.Key, Size: o.Size, LastModified: &at, ContentType: o.ContentType})
	}
	sort.SliceStable(out.Objects, func(i, j int) bool { return out.Objects[i].Folder && !out.Objects[j].Folder })
	return out, nil
}

// Open streams an object.
func (m *Manager) Open(ctx context.Context, ref, bucket, key string) (io.ReadCloser, minio.ObjectInfo, error) {
	cl, err := m.client(ctx, ref)
	if err != nil {
		return nil, minio.ObjectInfo{}, err
	}
	o, err := cl.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, minio.ObjectInfo{}, err
	}
	info, err := o.Stat()
	if err != nil {
		o.Close()
		return nil, minio.ObjectInfo{}, err
	}
	return o, info, nil
}

func (m *Manager) Upload(ctx context.Context, ref, bucket, key string, r io.Reader, size int64, contentType string) error {
	if key == "" || strings.HasPrefix(key, "/") {
		return ErrInvalid{errors.New("invalid object key")}
	}
	cl, err := m.client(ctx, ref)
	if err != nil {
		return err
	}
	_, err = cl.PutObject(ctx, bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

// Delete removes an object, or every object under a folder key ("…/").
func (m *Manager) DeleteObject(ctx context.Context, ref, bucket, key string) (int, error) {
	cl, err := m.client(ctx, ref)
	if err != nil {
		return 0, err
	}
	if !strings.HasSuffix(key, "/") {
		return 1, cl.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
	}
	objs := make(chan minio.ObjectInfo)
	n := 0
	go func() {
		defer close(objs)
		for o := range cl.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: key, Recursive: true}) {
			if o.Err == nil {
				n++
				objs <- o
			}
		}
	}()
	for e := range cl.RemoveObjects(ctx, bucket, objs, minio.RemoveObjectsOptions{}) {
		if e.Err != nil {
			return 0, e.Err
		}
	}
	return n, nil
}

// Usage is a bucket's (or prefix's) size, counted by listing (at most 1M objects).
type Usage struct {
	Objects int64 `json:"objects"`
	Bytes   int64 `json:"bytes"`
	Partial bool  `json:"partial"`
}

func (m *Manager) Usage(ctx context.Context, ref, bucket, prefix string) (Usage, error) {
	cl, err := m.client(ctx, ref)
	if err != nil {
		return Usage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var u Usage
	for o := range cl.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if o.Err != nil {
			if errors.Is(o.Err, context.DeadlineExceeded) {
				u.Partial = true
				return u, nil
			}
			return u, o.Err
		}
		u.Objects++
		u.Bytes += o.Size
		if u.Objects >= 1_000_000 {
			u.Partial = true
			break
		}
	}
	return u, nil
}

// BindingInput attaches a bucket to a service.
type BindingInput struct {
	Endpoint  string `json:"endpoint"` // name or ID
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`
	EnvPrefix string `json:"envPrefix"` // e.g. MEDIA_ for a second binding
}

// Resolve validates bindings for a service and returns them ready to store.
func (m *Manager) Resolve(ctx context.Context, serviceID string, in []BindingInput) ([]store.S3Binding, error) {
	seen := map[string]bool{}
	now := m.now().UTC().Truncate(time.Second)
	out := make([]store.S3Binding, 0, len(in))
	for _, b := range in {
		if !bucketRE.MatchString(b.Bucket) {
			return nil, ErrInvalid{fmt.Errorf("invalid bucket %q", b.Bucket)}
		}
		if !envPrefixRE.MatchString(b.EnvPrefix) {
			return nil, ErrInvalid{fmt.Errorf("envPrefix %q must be empty or uppercase ending in _, e.g. MEDIA_", b.EnvPrefix)}
		}
		if seen[b.EnvPrefix] {
			return nil, ErrInvalid{fmt.Errorf("two bindings with envPrefix %q: give each a different one", b.EnvPrefix)}
		}
		seen[b.EnvPrefix] = true
		e, err := m.st.S3EndpointByRef(ctx, b.Endpoint)
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrInvalid{fmt.Errorf("no S3 endpoint %q", b.Endpoint)}
		} else if err != nil {
			return nil, err
		}
		out = append(out, store.S3Binding{ID: auth.NewID("s3b_"), ServiceID: serviceID, EndpointID: e.ID, Endpoint: e.Name,
			Bucket: b.Bucket, Prefix: strings.TrimLeft(b.Prefix, "/"), EnvPrefix: b.EnvPrefix, CreatedAt: now})
	}
	return out, nil
}

// Env is the environment a bound task gets.
func Env(envPrefix, bucket, prefix string, c Creds) map[string]string {
	p := envPrefix
	return map[string]string{
		p + "S3_ENDPOINT": c.URL, p + "S3_BUCKET": bucket, p + "S3_PREFIX": prefix, p + "S3_FORCE_PATH_STYLE": fmt.Sprint(c.PathStyle),
		p + "AWS_ACCESS_KEY_ID": c.AccessKeyID, p + "AWS_SECRET_ACCESS_KEY": c.SecretAccessKey, p + "AWS_REGION": c.Region,
	}
}
