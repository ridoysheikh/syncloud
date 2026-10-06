package cli

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// S3 endpoints, buckets and service bindings (§16).

type s3EndpointView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Region      string `json:"region"`
	AccessKeyID string `json:"accessKeyId"`
	PathStyle   bool   `json:"pathStyle"`
	Bindings    int    `json:"bindings"`
}

type s3BindingView struct {
	Endpoint    string `json:"endpoint"`
	Bucket      string `json:"bucket"`
	Prefix      string `json:"prefix"`
	EnvPrefix   string `json:"envPrefix"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
}

// splitS3Path parses ENDPOINT/BUCKET[/KEY…].
func splitS3Path(p string) (endpoint, bucket, key string, err error) {
	parts := strings.SplitN(strings.TrimPrefix(p, "s3://"), "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", fmt.Errorf("%q: want ENDPOINT/BUCKET[/KEY]", p)
	}
	if len(parts) == 3 {
		key = parts[2]
	}
	return parts[0], parts[1], key, nil
}

func bucketPath(endpoint, bucket string) string {
	return "/api/v1/s3/endpoints/" + url.PathEscape(endpoint) + "/buckets/" + url.PathEscape(bucket)
}

func humanBytes(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
}

func (a *app) s3Cmd() *cobra.Command {
	root := &cobra.Command{Use: "s3", Aliases: []string{"storage"}, Short: "S3 endpoints, buckets and service bindings (§16)"}

	eps := &cobra.Command{Use: "endpoints", Aliases: []string{"endpoint"}, Short: "S3-compatible providers registered with the cluster"}
	var in struct {
		URL, Region, KeyID, Secret string
		PathStyle                  bool
	}
	put := func(update bool) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			secret := in.Secret
			if secret == "" {
				secret = os.Getenv("SYNCLOUD_S3_SECRET_ACCESS_KEY")
			}
			body := map[string]any{"name": args[0], "url": in.URL, "region": in.Region, "accessKeyId": in.KeyID, "secretAccessKey": secret, "pathStyle": in.PathStyle}
			var e s3EndpointView
			method, p := "POST", "/api/v1/s3/endpoints"
			if update {
				method, p = "PUT", p+"/"+url.PathEscape(args[0])
				if len(args) == 2 {
					body["name"] = args[1]
				}
			}
			if err := a.do(cmd, method, p, body, &e); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "S3 endpoint %s (%s) saved; credentials checked\n", e.Name, e.URL)
			return nil
		}
	}
	add := &cobra.Command{
		Use: "add NAME --url URL --access-key-id ID", Short: "Register an endpoint (the secret is read from --secret-access-key or $SYNCLOUD_S3_SECRET_ACCESS_KEY)",
		Args: cobra.ExactArgs(1), Annotations: op("createS3Endpoint"), RunE: put(false),
		Example: `  synctl s3 endpoints add r2 --url https://<account>.r2.cloudflarestorage.com --region auto --access-key-id … --secret-access-key …
  synctl s3 endpoints add minio --url http://minio.production.storage.internal:9000 --path-style --access-key-id … --secret-access-key …`,
	}
	update := &cobra.Command{
		Use: "update NAME [NEW-NAME]", Short: "Change an endpoint (an empty secret keeps the stored one)", Args: cobra.RangeArgs(1, 2),
		Annotations: op("updateS3Endpoint"), RunE: put(true),
	}
	for _, c := range []*cobra.Command{add, update} {
		f := c.Flags()
		f.StringVar(&in.URL, "url", "", "endpoint URL, e.g. https://s3.eu-central-1.amazonaws.com")
		f.StringVar(&in.Region, "region", "", "region")
		f.StringVar(&in.KeyID, "access-key-id", "", "access key ID")
		f.StringVar(&in.Secret, "secret-access-key", "", "secret access key (or $SYNCLOUD_S3_SECRET_ACCESS_KEY)")
		f.BoolVar(&in.PathStyle, "path-style", false, "path-style bucket addressing (MinIO, Garage)")
	}
	eps.AddCommand(add, update,
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List endpoints", Args: cobra.NoArgs, Annotations: op("listS3Endpoints"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				es, err := listOf[s3EndpointView](a, cmd, "/api/v1/s3/endpoints")
				if err != nil {
					return err
				}
				rows := [][]string{}
				for _, e := range es {
					rows = append(rows, []string{e.Name, e.URL, orDash(e.Region), e.AccessKeyID, fmt.Sprint(e.PathStyle), fmt.Sprint(e.Bindings)})
				}
				return a.printer().table(es, []string{"NAME", "URL", "REGION", "ACCESS KEY", "PATH STYLE", "BINDINGS"}, rows)
			},
		},
		&cobra.Command{
			Use: "get NAME", Short: "An endpoint and the services bound to it", Args: cobra.ExactArgs(1), Annotations: op("getS3Endpoint"),
			RunE: func(cmd *cobra.Command, args []string) error {
				var out struct {
					Endpoint s3EndpointView  `json:"endpoint"`
					Bindings []s3BindingView `json:"bindings"`
				}
				if err := a.do(cmd, "GET", "/api/v1/s3/endpoints/"+url.PathEscape(args[0]), nil, &out); err != nil {
					return err
				}
				if a.output == "json" {
					return a.printer().json(out)
				}
				fmt.Fprintf(a.out, "%s  %s  region %s  key %s\n", out.Endpoint.Name, out.Endpoint.URL, orDash(out.Endpoint.Region), out.Endpoint.AccessKeyID)
				for _, b := range out.Bindings {
					fmt.Fprintf(a.out, "  %s/%s/%s → %s/%s (%sS3_*)\n", b.Project, b.Environment, b.Service, b.Bucket, b.Prefix, b.EnvPrefix)
				}
				return nil
			},
		},
		&cobra.Command{
			Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete an endpoint no service is bound to", Args: cobra.ExactArgs(1), Annotations: op("deleteS3Endpoint"),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := a.do(cmd, "DELETE", "/api/v1/s3/endpoints/"+url.PathEscape(args[0]), nil, nil); err != nil {
					return err
				}
				fmt.Fprintln(a.out, "Deleted S3 endpoint", args[0])
				return nil
			},
		},
	)

	buckets := &cobra.Command{
		Use: "buckets ENDPOINT", Short: "Buckets of an endpoint and the services bound to them", Args: cobra.ExactArgs(1), Annotations: op("listS3Buckets"),
		RunE: func(cmd *cobra.Command, args []string) error {
			bs, err := listOf[struct {
				Name      string    `json:"name"`
				CreatedAt time.Time `json:"createdAt"`
				BoundBy   []string  `json:"boundBy"`
			}](a, cmd, "/api/v1/s3/endpoints/"+url.PathEscape(args[0])+"/buckets")
			if err != nil {
				return err
			}
			rows := [][]string{}
			for _, b := range bs {
				rows = append(rows, []string{b.Name, b.CreatedAt.Local().Format("2006-01-02"), joinOrDash(b.BoundBy)})
			}
			return a.printer().table(bs, []string{"BUCKET", "CREATED", "BOUND BY"}, rows)
		},
	}
	mb := &cobra.Command{
		Use: "mb ENDPOINT/BUCKET", Short: "Create a bucket", Args: cobra.ExactArgs(1), Annotations: op("createS3Bucket"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ep, bucket, _, err := splitS3Path(args[0])
			if err != nil {
				return err
			}
			if err := a.do(cmd, "POST", "/api/v1/s3/endpoints/"+url.PathEscape(ep)+"/buckets", map[string]string{"name": bucket}, nil); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "Created bucket", bucket)
			return nil
		},
	}
	ls := &cobra.Command{
		Use: "ls ENDPOINT/BUCKET[/PREFIX/]", Short: "List one level of a bucket", Args: cobra.ExactArgs(1), Annotations: op("listS3Objects"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ep, bucket, prefix, err := splitS3Path(args[0])
			if err != nil {
				return err
			}
			var l struct {
				Objects []struct {
					Key          string     `json:"key"`
					Folder       bool       `json:"folder"`
					Size         int64      `json:"size"`
					LastModified *time.Time `json:"lastModified"`
				} `json:"objects"`
				Truncated bool `json:"truncated"`
			}
			if err := a.do(cmd, "GET", bucketPath(ep, bucket)+"/objects?prefix="+url.QueryEscape(prefix), nil, &l); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(l)
			}
			for _, o := range l.Objects {
				if o.Folder {
					fmt.Fprintf(a.out, "%19s  %10s  %s\n", "", "DIR", o.Key)
				} else {
					fmt.Fprintf(a.out, "%19s  %10s  %s\n", o.LastModified.Local().Format("2006-01-02 15:04:05"), humanBytes(o.Size), o.Key)
				}
			}
			if l.Truncated {
				fmt.Fprintln(a.out, "(more than 1000 entries; narrow the prefix)")
			}
			return nil
		},
	}
	cp := &cobra.Command{
		Use: "cp SRC DST", Short: "Upload (FILE ENDPOINT/BUCKET/KEY) or download (ENDPOINT/BUCKET/KEY FILE|-)", Args: cobra.ExactArgs(2),
		Annotations: op("uploadS3Object", "downloadS3Object"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if _, err := os.Stat(args[0]); err == nil { // upload
				ep, bucket, key, err := splitS3Path(args[1])
				if err != nil {
					return err
				}
				if key == "" || strings.HasSuffix(key, "/") {
					key += filepath.Base(args[0])
				}
				if fi, err := os.Stat(args[0]); err == nil && fi.Size() > 256<<20 {
					return fmt.Errorf("%s is %s; synctl uploads at most 256 MiB (signed requests are checked in memory): use the dashboard or an S3 client", args[0], humanBytes(fi.Size()))
				}
				b, err := os.ReadFile(args[0])
				if err != nil {
					return err
				}
				ct := mime.TypeByExtension(path.Ext(key))
				if ct == "" {
					ct = "application/octet-stream"
				}
				resp, err := c.Send(ctx(cmd), "PUT", bucketPath(ep, bucket)+"/object?key="+url.QueryEscape(key), b, ct)
				if err != nil {
					return err
				}
				resp.Body.Close()
				fmt.Fprintf(a.out, "Uploaded %s to %s/%s/%s (%s)\n", args[0], ep, bucket, key, humanBytes(int64(len(b))))
				return nil
			}
			ep, bucket, key, err := splitS3Path(args[0])
			if err != nil {
				return err
			}
			if key == "" {
				return errors.New("give the object key to download")
			}
			resp, err := c.Send(ctx(cmd), "GET", bucketPath(ep, bucket)+"/object?key="+url.QueryEscape(key), nil, "")
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			var w io.Writer = a.out
			if args[1] != "-" {
				dst := args[1]
				if fi, err := os.Stat(dst); err == nil && fi.IsDir() {
					dst = filepath.Join(dst, path.Base(key))
				}
				f, err := os.Create(dst)
				if err != nil {
					return err
				}
				defer f.Close()
				w = f
			}
			_, err = io.Copy(w, resp.Body)
			return err
		},
	}
	rm := &cobra.Command{
		Use: "rm ENDPOINT/BUCKET/KEY", Short: "Delete an object, or everything under a key ending in /", Args: cobra.ExactArgs(1), Annotations: op("deleteS3Object"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ep, bucket, key, err := splitS3Path(args[0])
			if err != nil {
				return err
			}
			var out struct {
				Deleted int `json:"deleted"`
			}
			if err := a.do(cmd, "DELETE", bucketPath(ep, bucket)+"/object?key="+url.QueryEscape(key), nil, &out); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Deleted %d object(s)\n", out.Deleted)
			return nil
		},
	}
	du := &cobra.Command{
		Use: "du ENDPOINT/BUCKET[/PREFIX]", Short: "Objects and bytes under a prefix", Args: cobra.ExactArgs(1), Annotations: op("getS3Usage"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ep, bucket, prefix, err := splitS3Path(args[0])
			if err != nil {
				return err
			}
			var u struct {
				Objects int64 `json:"objects"`
				Bytes   int64 `json:"bytes"`
				Partial bool  `json:"partial"`
			}
			if err := a.do(cmd, "GET", bucketPath(ep, bucket)+"/usage?prefix="+url.QueryEscape(prefix), nil, &u); err != nil {
				return err
			}
			more := ""
			if u.Partial {
				more = " (at least; counting stopped)"
			}
			fmt.Fprintf(a.out, "%d objects, %s%s\n", u.Objects, humanBytes(u.Bytes), more)
			return nil
		},
	}

	var sc scope
	bindings := &cobra.Command{Use: "bindings", Aliases: []string{"bind"}, Short: "Buckets bound to a service (its tasks get S3_* and AWS_* variables)"}
	a.scopeFlags(bindings, &sc)
	svcPath := func(service string) string {
		return "/api/v1/projects/" + url.PathEscape(sc.project) + "/environments/" + url.PathEscape(sc.env) + "/services/" + url.PathEscape(service) + "/s3"
	}
	var ep, bucket, prefix, envPrefix string
	setFlags := func(c *cobra.Command) {
		c.Flags().StringVar(&ep, "endpoint", "", "endpoint name")
		c.Flags().StringVar(&bucket, "bucket", "", "bucket")
		c.Flags().StringVar(&prefix, "prefix", "", "key prefix inside the bucket")
		c.Flags().StringVar(&envPrefix, "env-prefix", "", "variable prefix for a second binding, e.g. MEDIA_")
	}
	current := func(cmd *cobra.Command, service string) ([]map[string]string, error) {
		bs, err := listOf[s3BindingView](a, cmd, svcPath(service))
		if err != nil {
			return nil, err
		}
		out := []map[string]string{}
		for _, b := range bs {
			out = append(out, map[string]string{"endpoint": b.Endpoint, "bucket": b.Bucket, "prefix": b.Prefix, "envPrefix": b.EnvPrefix})
		}
		return out, nil
	}
	save := func(cmd *cobra.Command, service string, bs []map[string]string) error {
		var out struct {
			Revision int `json:"revision"`
		}
		if err := a.do(cmd, "PUT", svcPath(service), map[string]any{"bindings": bs}, &out); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s has %d S3 binding(s); rolling out revision %d\n", service, len(bs), out.Revision)
		return nil
	}
	addB := &cobra.Command{
		Use: "add SERVICE --endpoint E --bucket B", Short: "Bind a bucket (replaces the binding with the same --env-prefix)", Args: cobra.ExactArgs(1),
		Annotations: op("setServiceS3Bindings"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := sc.need(); err != nil {
				return err
			}
			bs, err := current(cmd, args[0])
			if err != nil {
				return err
			}
			kept := []map[string]string{}
			for _, b := range bs {
				if b["envPrefix"] != envPrefix {
					kept = append(kept, b)
				}
			}
			kept = append(kept, map[string]string{"endpoint": ep, "bucket": bucket, "prefix": prefix, "envPrefix": envPrefix})
			return save(cmd, args[0], kept)
		},
	}
	setFlags(addB)
	removeB := &cobra.Command{
		Use: "remove SERVICE [--env-prefix P]", Aliases: []string{"rm"}, Short: "Unbind (the binding with that --env-prefix)", Args: cobra.ExactArgs(1),
		Annotations: op("setServiceS3Bindings"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := sc.need(); err != nil {
				return err
			}
			bs, err := current(cmd, args[0])
			if err != nil {
				return err
			}
			kept := []map[string]string{}
			for _, b := range bs {
				if b["envPrefix"] != envPrefix {
					kept = append(kept, b)
				}
			}
			if len(kept) == len(bs) {
				return fmt.Errorf("%s has no binding with env prefix %q", args[0], envPrefix)
			}
			return save(cmd, args[0], kept)
		},
	}
	removeB.Flags().StringVar(&envPrefix, "env-prefix", "", "the binding's variable prefix")
	bindings.AddCommand(addB, removeB, &cobra.Command{
		Use: "list SERVICE", Aliases: []string{"ls"}, Short: "A service's bindings", Args: cobra.ExactArgs(1), Annotations: op("getServiceS3Bindings"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := sc.need(); err != nil {
				return err
			}
			bs, err := listOf[s3BindingView](a, cmd, svcPath(args[0]))
			if err != nil {
				return err
			}
			rows := [][]string{}
			for _, b := range bs {
				rows = append(rows, []string{b.Endpoint, b.Bucket, orDash(b.Prefix), b.EnvPrefix + "S3_*, " + b.EnvPrefix + "AWS_*"})
			}
			return a.printer().table(bs, []string{"ENDPOINT", "BUCKET", "PREFIX", "VARIABLES"}, rows)
		},
	})
	root.AddCommand(eps, buckets, mb, ls, cp, rm, du, bindings)
	return root
}
