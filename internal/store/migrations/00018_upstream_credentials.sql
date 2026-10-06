-- +goose Up

-- Credentials for third-party registries (§5.9): nodes use them to pull
-- public or private images (Docker Hub rate limits, GHCR, Quay, …) and
-- builds use them for private FROM images. Passwords are sealed with the
-- master key.
CREATE TABLE upstream_credentials (
    id           TEXT PRIMARY KEY,
    host         TEXT NOT NULL UNIQUE,
    username     TEXT NOT NULL,
    password_enc BLOB NOT NULL,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

-- +goose Down
DROP TABLE upstream_credentials;
