-- +goose Up
-- Base backup runs of PostgreSQL clusters (Phase 13c): one-shot WAL-G
-- tasks the controller starts on a member's node. The backups themselves
-- live in S3; these rows are the schedule's memory and the run history.
CREATE TABLE database_backups (
    id          TEXT PRIMARY KEY, -- the task ID (dbk_…)
    database_id TEXT NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
    node_id     TEXT NOT NULL,
    member      TEXT NOT NULL,             -- m<n> whose data was copied
    trigger     TEXT NOT NULL,             -- schedule | manual
    state       TEXT NOT NULL DEFAULT 'running', -- running | ok | failed
    error       TEXT NOT NULL DEFAULT '',
    started_at  INTEGER NOT NULL,
    finished_at INTEGER
);
CREATE INDEX database_backups_db ON database_backups (database_id, started_at);

-- +goose Down
DROP TABLE database_backups;
