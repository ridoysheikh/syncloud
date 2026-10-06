-- +goose Up

-- Quotas (§7.2) per project, optionally overridden per environment
-- (environment_id '' is the project-wide quota).
CREATE TABLE quotas (
    project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id TEXT NOT NULL DEFAULT '',
    limits         TEXT NOT NULL,               -- JSON (internal/quota)
    updated_at     INTEGER NOT NULL,
    PRIMARY KEY (project_id, environment_id)
);

-- Usage metering: totals per project, environment and UTC day, added to
-- every minute.
CREATE TABLE usage_daily (
    project            TEXT NOT NULL,
    environment        TEXT NOT NULL,
    day                TEXT NOT NULL,           -- YYYY-MM-DD
    cpu_reserved_hours REAL NOT NULL DEFAULT 0, -- core-hours
    mem_reserved_hours REAL NOT NULL DEFAULT 0, -- GiB-hours
    cpu_used_hours     REAL NOT NULL DEFAULT 0,
    mem_used_hours     REAL NOT NULL DEFAULT 0,
    net_out_bytes      INTEGER NOT NULL DEFAULT 0,
    log_bytes          INTEGER NOT NULL DEFAULT 0,
    build_seconds      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (project, environment, day)
);

-- +goose Down
DROP TABLE usage_daily;
DROP TABLE quotas;
