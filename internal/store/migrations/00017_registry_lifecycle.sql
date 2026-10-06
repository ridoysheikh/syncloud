-- +goose Up

-- Lifecycle policy per registry repository (§5.10): rules as JSON.
CREATE TABLE registry_lifecycle (
    repository TEXT PRIMARY KEY,
    rules      TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    updated_by TEXT NOT NULL DEFAULT ''
);

-- Each lifecycle + garbage collection run.
CREATE TABLE registry_gc_runs (
    id              TEXT PRIMARY KEY,
    trigger         TEXT NOT NULL,           -- schedule | manual
    status          TEXT NOT NULL,           -- running | succeeded | failed
    expired         INTEGER NOT NULL DEFAULT 0,
    reclaimed_bytes INTEGER NOT NULL DEFAULT 0,
    message         TEXT NOT NULL DEFAULT '',
    details         TEXT NOT NULL DEFAULT '[]', -- expired images
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER
);
CREATE INDEX registry_gc_runs_started ON registry_gc_runs(started_at);

-- +goose Down
DROP TABLE registry_gc_runs;
DROP TABLE registry_lifecycle;
