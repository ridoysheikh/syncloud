# S3 storage and metrics

## S3 storage

SynCloud has no shared volumes. Use a node-local volume for scratch data that may be lost with the node; use **S3** for anything shared, durable or portable.

### Endpoints

Register any S3-compatible provider under **Storage → S3**, or with:

```sh
synctl s3 endpoints add r2 --url https://<account>.r2.cloudflarestorage.com --region auto \
  --access-key-id … --secret-access-key …
```

This works with AWS S3, Cloudflare R2, Backblaze B2, Wasabi and DigitalOcean Spaces.

- The credentials are checked by listing buckets before the endpoint is saved.
- The secret is sealed with the master key and never shown again.

**Self-hosted S3.** Deploy MinIO or Garage as an ordinary service (for example pinned to one node with a node-local volume), then register it like any other endpoint, with `--path-style`.

### Bucket browser

Open an endpoint to list its buckets and the services bound to each. From there you can:

- create a bucket;
- browse folders;
- upload, download and delete objects, or whole folders;
- count a prefix's size.

Downloads always arrive as file attachments, so they never render inside the dashboard.

The same actions are available from synctl:

```sh
synctl s3 buckets r2
synctl s3 mb r2/media
synctl s3 ls r2/media/images/
synctl s3 cp photo.png r2/media/images/      # upload (at most 256 MiB from synctl)
synctl s3 cp r2/media/images/photo.png .     # download
synctl s3 rm r2/media/images/                # everything under a folder
synctl s3 du r2/media
```

### Binding a bucket to a service

Add bindings on a service's **S3** tab, or with:

```sh
synctl s3 bindings add web -p shop --endpoint r2 --bucket media --prefix web/
```

The service's tasks get these environment variables:

- `S3_ENDPOINT`
- `S3_BUCKET`
- `S3_PREFIX`
- `S3_FORCE_PATH_STYLE`
- `AWS_ACCESS_KEY_ID`
- `AWS_SECRET_ACCESS_KEY`
- `AWS_REGION`

Most S3 SDKs read the `AWS_*` variables directly.

To bind a second bucket, give it a variable prefix (`--env-prefix MEDIA_` gives `MEDIA_S3_BUCKET` and so on).

Changing bindings creates a new revision and rolls it out. Revisions store only the endpoint name, bucket and prefix; credentials are added when each task starts. A service's own variables win over binding variables. An endpoint cannot be deleted while services are bound to it.

## Metrics explorer

Use **Monitoring → Metrics** to run PromQL over everything VictoriaMetrics stores. That includes:

- task CPU, memory, network and disk (`syncloud_task_*`);
- node and mesh traffic (`syncloud_node_*`);
- Traefik's request metrics (`traefik_*`);
- uptime checks and autoscaling targets.

The page lists metric names and has example queries. You can also run queries from the CLI:

```sh
synctl metrics query 'sum by (service) (rate(traefik_service_requests_total[5m]))' --range 6h
synctl metrics names
```

A query can read every project's series, so IAM checks the action `metrics:ExploreMetrics` on `srn:syncloud:metrics`. Grant it only to people who may see the whole cluster. Per-service and per-environment charts stay available to project members.
