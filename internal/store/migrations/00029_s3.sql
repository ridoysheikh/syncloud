-- +goose Up
-- S3 endpoints (§16): any S3-compatible provider, registered once for the
-- cluster. The secret key is sealed with the master key.
CREATE TABLE s3_endpoints (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    url           TEXT NOT NULL,
    region        TEXT NOT NULL DEFAULT '',
    access_key_id TEXT NOT NULL,
    secret_enc    BLOB NOT NULL,
    path_style    INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL
);

-- A bucket (and optional prefix) attached to a service; its tasks get the
-- endpoint, bucket and credentials as environment variables.
CREATE TABLE s3_bindings (
    id          TEXT PRIMARY KEY,
    service_id  TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    endpoint_id TEXT NOT NULL REFERENCES s3_endpoints(id),
    bucket      TEXT NOT NULL,
    prefix      TEXT NOT NULL DEFAULT '',
    env_prefix  TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    UNIQUE (service_id, env_prefix)
);
CREATE INDEX s3_bindings_endpoint ON s3_bindings (endpoint_id);

-- +goose Down
DROP TABLE s3_bindings;
DROP TABLE s3_endpoints;
