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

## Registry images in S3

The built-in registry keeps images on the controller's disk by default. To keep them in a bucket instead, so the controller's disk stops growing with images, go to **Registry → Storage → Change**, or run:

```sh
synctl registry storage set --s3 r2 --bucket syncloud-registry --wait
synctl registry storage get
```

- **Switching copies every image.** A temporary registry starts on the new storage, every tagged image is copied and checked, and then the registry switches over. Pushes are refused while images are copied; pulls keep working. If anything fails, the registry stays where it was and a retry resumes the copy.
- **Disk space:** after a switch away from the controller's disk, the images are deleted from it. Nodes still keep the images they run in their own Docker cache.
- **Nodes download layers from the bucket directly** (presigned URLs), so every node must reach the endpoint, with a valid certificate on HTTPS.
- **Going back:** `synctl registry storage set --disk` copies the images back. An old S3 location is never deleted for you. The result says where it is, so you can remove it from the bucket browser.
- **Bucket:** it must exist. A bucket used only by the registry is simplest. With `--prefix`, the registry keeps its files under that key prefix. Some S3 servers (Garage, for one) list objects wrongly under a prefix. The switch detects this and refuses, and so does garbage collection. Use a bucket without a prefix there.
- **The endpoint the registry uses** can't be deleted. Editing its credentials restarts the registry with the new ones.
- **Disaster recovery:** images in S3 survive the loss of the controller. After [restoring a backup](../operations/backup-restore.md) on a new controller, the registry comes back with all its images.

## Other uses of S3

- [Registry images](#registry-images-in-s3)
- [Controller backups](../operations/backup-restore.md)
- [PostgreSQL backups](postgres-backups.md)
