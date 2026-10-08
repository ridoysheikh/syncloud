# S3 storage

SynCloud has no shared disks between nodes. Keep files that must survive a node, or be shared, in S3. That works with AWS S3, Cloudflare R2, Backblaze B2, Wasabi, DigitalOcean Spaces, or a self-hosted MinIO or Garage.

## Register an endpoint

**Storage → S3 → Add endpoint**, or:

```sh
synctl s3 endpoints add r2 --url https://<account>.r2.cloudflarestorage.com --region auto \
  --access-key-id … --secret-access-key …
```

The credentials are tested before the endpoint is saved, then sealed and never shown again. For MinIO and Garage, add `--path-style`.

**Self-hosting S3:** deploy MinIO or Garage as an ordinary service, pinned to one node, then register it like any other endpoint.

## Browse buckets

Open an endpoint to see its buckets and which services use them. You can create buckets, browse folders, and upload, download or delete objects or whole folders.

```sh
synctl s3 buckets r2
synctl s3 mb r2/media
synctl s3 ls r2/media/images/
synctl s3 cp photo.png r2/media/images/      # upload (up to 256 MiB from synctl)
synctl s3 cp r2/media/images/photo.png .     # download
synctl s3 rm r2/media/images/                # a whole folder
synctl s3 du r2/media
```

## Give a service a bucket

On the service's **S3** tab, or:

```sh
synctl s3 bindings add web -p shop --endpoint r2 --bucket media --prefix web/
```

Its tasks then get `S3_ENDPOINT`, `S3_BUCKET`, `S3_PREFIX`, `S3_FORCE_PATH_STYLE`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_REGION`. Most S3 SDKs read the `AWS_*` variables directly.

- **More than one bucket:** bind a second bucket with `--env-prefix MEDIA_` (its variables become `MEDIA_S3_BUCKET` and so on).
- **Credentials stay out of revisions:** a binding change rolls out a new revision, but revisions store only the endpoint, bucket and prefix. Credentials are added when each task starts.
- **Endpoints in use:** an endpoint can't be deleted while services use it.

## Other uses of S3

- [Controller backups](../operations/backup-restore.md)
- [PostgreSQL backups](postgres-backups.md)
