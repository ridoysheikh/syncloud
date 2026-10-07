-- +goose Up
-- Volumes of database members that were removed while their node could not
-- be told (disconnected, or the stream broke). The agent removes the
-- container and volume when the node reports in again.
CREATE TABLE database_orphans (
    task_id    TEXT PRIMARY KEY,
    node_id    TEXT NOT NULL,
    volume     TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX database_orphans_node ON database_orphans (node_id);

-- +goose Down
DROP TABLE database_orphans;
