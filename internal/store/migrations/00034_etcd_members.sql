-- +goose Up
-- The platform etcd (Phase 13): the coordination store Patroni uses for
-- every PostgreSQL cluster. One row per member container, pinned to its
-- node with a node-local volume. member_id is etcd's own ID (hex), known
-- once the member is part of the cluster.
CREATE TABLE etcd_members (
    id          TEXT PRIMARY KEY,
    ordinal     INTEGER NOT NULL UNIQUE,
    node_id     TEXT NOT NULL,
    state       TEXT NOT NULL DEFAULT 'pending',
    ip          TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    spec_hash   TEXT NOT NULL DEFAULT '',
    cluster     TEXT NOT NULL DEFAULT '', -- initial-cluster it was started with
    new_cluster INTEGER NOT NULL DEFAULT 1, -- initial-cluster-state new (1) or existing (0)
    member_id   TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- +goose Down
DROP TABLE etcd_members;
