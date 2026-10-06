-- +goose Up

-- Which task or job run held which container address, and when (§8.2).
-- Kept 30 days.
CREATE TABLE ip_history (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ip          TEXT NOT NULL,
    owner_id    TEXT NOT NULL,               -- task or job run ID
    owner       TEXT NOT NULL,               -- project/env/service, or job run
    node_id     TEXT NOT NULL DEFAULT '',
    assigned_at INTEGER NOT NULL,
    released_at INTEGER
);
CREATE INDEX ip_history_ip ON ip_history (ip, assigned_at);
CREATE INDEX ip_history_owner ON ip_history (owner_id) WHERE released_at IS NULL;

-- +goose Down
DROP TABLE ip_history;
