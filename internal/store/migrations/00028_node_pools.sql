-- +goose Up

-- Cloud providers (§6.5): API credentials sealed with the master key.
CREATE TABLE cloud_providers (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    type       TEXT NOT NULL,                 -- hetzner | digitalocean | webhook
    config_enc BLOB NOT NULL,
    summary    TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

-- Node pools: manual (join by hand) or backed by a provider.
CREATE TABLE node_pools (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    role        TEXT NOT NULL DEFAULT 'worker', -- worker | edge
    provider_id TEXT REFERENCES cloud_providers(id) ON DELETE RESTRICT,
    spec        TEXT NOT NULL,                  -- JSON (internal/nodepool): server, capacity, scaling
    min_nodes   INTEGER NOT NULL DEFAULT 0,
    max_nodes   INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

ALTER TABLE nodes ADD COLUMN pool_id TEXT REFERENCES node_pools(id) ON DELETE SET NULL;
ALTER TABLE nodes ADD COLUMN scale_in_protected INTEGER NOT NULL DEFAULT 0;
ALTER TABLE join_tokens ADD COLUMN pool_id TEXT REFERENCES node_pools(id) ON DELETE CASCADE;
ALTER TABLE join_tokens ADD COLUMN node_name TEXT NOT NULL DEFAULT '';   -- forced name (provider servers)

-- Servers a pool asked a provider for, until their node joins (or the
-- attempt times out).
CREATE TABLE pool_servers (
    pool_id    TEXT NOT NULL REFERENCES node_pools(id) ON DELETE CASCADE,
    server_id  TEXT NOT NULL,                   -- the provider's ID
    name       TEXT NOT NULL,                   -- also the node name
    state      TEXT NOT NULL,                   -- creating | joined | failed | deleting
    node_id    TEXT,
    message    TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (pool_id, server_id)
);

-- Scaling decisions with their reason.
CREATE TABLE pool_events (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    pool_id TEXT NOT NULL REFERENCES node_pools(id) ON DELETE CASCADE,
    at      INTEGER NOT NULL,
    kind    TEXT NOT NULL,                      -- scale-out | scale-in | failed | info
    message TEXT NOT NULL
);
CREATE INDEX pool_events_pool ON pool_events (pool_id, at);

-- +goose Down
DROP TABLE pool_events;
DROP TABLE pool_servers;
ALTER TABLE join_tokens DROP COLUMN node_name;
ALTER TABLE join_tokens DROP COLUMN pool_id;
ALTER TABLE nodes DROP COLUMN scale_in_protected;
ALTER TABLE nodes DROP COLUMN pool_id;
DROP TABLE node_pools;
DROP TABLE cloud_providers;
