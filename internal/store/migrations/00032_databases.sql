-- +goose Up
-- Managed Valkey databases (Phase 12): a database lives in a project
-- environment and shares its DNS namespace with services. Its passwords are
-- sealed with the master key; spec and state are JSON.
CREATE TABLE databases (
    id             TEXT PRIMARY KEY,
    environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    engine         TEXT NOT NULL DEFAULT 'valkey',
    version        TEXT NOT NULL,
    spec           TEXT NOT NULL,          -- what the user asked for
    state          TEXT NOT NULL DEFAULT '{}', -- current memory, replicas, primary
    secrets_enc    BLOB NOT NULL,          -- app and admin passwords
    status         TEXT NOT NULL DEFAULT '',
    deleting       INTEGER NOT NULL DEFAULT 0,
    vip_rw         INTEGER,
    vip_ro         INTEGER,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    UNIQUE (environment_id, name)
);

-- One row per container: data members (kind 'data', ordinal = m<n>) and
-- sentinels (kind 'sentinel', s<n>). A member is pinned to its node; its
-- volume stays there.
CREATE TABLE database_members (
    id          TEXT PRIMARY KEY,
    database_id TEXT NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL,
    ordinal     INTEGER NOT NULL,
    node_id     TEXT NOT NULL,
    desired     TEXT NOT NULL DEFAULT 'running',
    state       TEXT NOT NULL DEFAULT 'pending',
    ip          TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    spec_hash   TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (database_id, kind, ordinal)
);
CREATE INDEX database_members_node ON database_members(node_id);

-- Scaling events of databases (memory and replicas).
CREATE TABLE database_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    database_id TEXT NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
    at          INTEGER NOT NULL,
    kind        TEXT NOT NULL,   -- memory | replicas | failover | member
    from_value  TEXT NOT NULL DEFAULT '',
    to_value    TEXT NOT NULL DEFAULT '',
    reason      TEXT NOT NULL DEFAULT '',
    actor       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX database_events_db ON database_events(database_id, at);

-- +goose Down
DROP TABLE database_events;
DROP TABLE database_members;
DROP TABLE databases;
