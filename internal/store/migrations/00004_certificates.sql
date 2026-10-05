-- +goose Up

-- TLS certificates for public hostnames (§5.0.2, §5.7). Keys are sealed with
-- the master key. A self-signed placeholder is stored until ACME succeeds.
CREATE TABLE certificates (
    host            TEXT PRIMARY KEY,
    issuer          TEXT NOT NULL,             -- acme | self-signed
    cert_pem        TEXT NOT NULL,
    key_enc         BLOB NOT NULL,
    not_after       INTEGER NOT NULL,
    status          TEXT NOT NULL,             -- valid | pending | failed | rate_limited
    last_error      TEXT NOT NULL DEFAULT '',
    failures        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL
);

-- +goose Down
DROP TABLE certificates;
