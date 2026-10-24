-- +goose Up
-- Sources the host firewall drops before anything else (Phase 24). node is
-- a node name ('' = every node); expires_at 0 = until removed; auto marks
-- bans made by a guard (e.g. SSH over its rate limit).
CREATE TABLE firewall_blocks (
    id         TEXT PRIMARY KEY,
    source     TEXT NOT NULL,
    node       TEXT NOT NULL DEFAULT '',
    reason     TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL DEFAULT '',
    auto       INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX firewall_blocks_source ON firewall_blocks (source, node);

-- +goose Down
DROP TABLE firewall_blocks;
