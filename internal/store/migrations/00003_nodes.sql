-- +goose Up

-- Worker nodes (§6). Live state (heartbeats, metrics) is kept in memory;
-- only status transitions are written here (D5).
CREATE TABLE nodes (
    id             TEXT PRIMARY KEY,         -- node_…
    name           TEXT NOT NULL UNIQUE,     -- e.g. "ctl-0", "w-01"
    status         TEXT NOT NULL,            -- pending | ready | suspect | not_ready
    info           TEXT NOT NULL DEFAULT '{}', -- NodeInfo JSON from the last Hello
    cert_serial    TEXT NOT NULL,
    created_at     INTEGER NOT NULL,
    status_at      INTEGER NOT NULL,
    last_seen_at   INTEGER
);

-- Join tokens (§6.1). Only the SHA-256 is stored.
CREATE TABLE join_tokens (
    id          TEXT PRIMARY KEY,            -- jt_…
    token_hash  TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_by  TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    single_use  INTEGER NOT NULL,
    uses        INTEGER NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE join_tokens;
DROP TABLE nodes;
