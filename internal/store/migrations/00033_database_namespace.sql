-- +goose Up
-- Databases get one cluster-wide namespace (Phase 12e): a database is either
-- standalone (no environment) or in a project environment, and its name is
-- unique in the cluster. "network" holds the access list and the public
-- endpoint settings (JSON owned by package dbs).
--
-- SQLite cannot drop NOT NULL in place, so the three tables are rebuilt.
-- Children are copied aside and dropped first, so dropping databases
-- cascades into nothing.
CREATE TABLE tmp_database_members AS SELECT * FROM database_members;
CREATE TABLE tmp_database_events AS SELECT * FROM database_events;
CREATE TABLE tmp_databases AS SELECT * FROM databases;
DROP TABLE database_events;
DROP TABLE database_members;
DROP TABLE databases;

CREATE TABLE databases (
    id             TEXT PRIMARY KEY,
    environment_id TEXT REFERENCES environments(id) ON DELETE CASCADE, -- NULL = standalone
    name           TEXT NOT NULL UNIQUE,
    engine         TEXT NOT NULL DEFAULT 'valkey',
    version        TEXT NOT NULL,
    spec           TEXT NOT NULL,
    state          TEXT NOT NULL DEFAULT '{}',
    network        TEXT NOT NULL DEFAULT '{}',
    secrets_enc    BLOB NOT NULL,
    status         TEXT NOT NULL DEFAULT '',
    deleting       INTEGER NOT NULL DEFAULT 0,
    vip_rw         INTEGER,
    vip_ro         INTEGER,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);
CREATE INDEX databases_environment ON databases(environment_id);

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

CREATE TABLE database_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    database_id TEXT NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
    at          INTEGER NOT NULL,
    kind        TEXT NOT NULL,
    from_value  TEXT NOT NULL DEFAULT '',
    to_value    TEXT NOT NULL DEFAULT '',
    reason      TEXT NOT NULL DEFAULT '',
    actor       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX database_events_db ON database_events(database_id, at);

-- Names were unique per environment; a later duplicate gets a suffix.
INSERT INTO databases (id, environment_id, name, engine, version, spec, state, secrets_enc, status, deleting, vip_rw, vip_ro, created_at, updated_at)
SELECT d.id, d.environment_id,
       CASE WHEN EXISTS (SELECT 1 FROM tmp_databases o WHERE o.name = d.name AND o.id < d.id)
            THEN substr(d.name, 1, 24) || '-' || substr(d.id, 4, 4) ELSE d.name END,
       d.engine, d.version, d.spec, d.state, d.secrets_enc, d.status, d.deleting, d.vip_rw, d.vip_ro, d.created_at, d.updated_at
FROM tmp_databases d;
INSERT INTO database_members SELECT * FROM tmp_database_members;
INSERT INTO database_events SELECT * FROM tmp_database_events;
DROP TABLE tmp_database_members;
DROP TABLE tmp_database_events;
DROP TABLE tmp_databases;

-- +goose Down
-- Standalone databases have no environment to go back to.
DELETE FROM databases WHERE environment_id IS NULL;
ALTER TABLE databases DROP COLUMN network;
