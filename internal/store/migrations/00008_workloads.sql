-- +goose Up

-- Workloads (§4): project → environment → service → task definition revisions
-- and tasks. Specs are JSON (internal/workload).
CREATE TABLE projects (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);

CREATE TABLE environments (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE (project_id, name)
);

CREATE TABLE services (
    id             TEXT PRIMARY KEY,
    environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    revision       INTEGER NOT NULL,          -- current task definition
    desired_count  INTEGER NOT NULL,
    deleting       INTEGER NOT NULL DEFAULT 0, -- tasks are being stopped before the row goes
    status         TEXT NOT NULL DEFAULT '',   -- last reconciler message, e.g. why tasks cannot be placed
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    UNIQUE (environment_id, name)
);

-- Immutable revisions.
CREATE TABLE task_definitions (
    service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    revision   INTEGER NOT NULL,
    spec       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    created_by TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (service_id, revision)
);

CREATE TABLE tasks (
    id           TEXT PRIMARY KEY,
    service_id   TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    revision     INTEGER NOT NULL,
    node_id      TEXT REFERENCES nodes(id) ON DELETE SET NULL,
    desired      TEXT NOT NULL,                -- running | stopped
    state        TEXT NOT NULL,                -- pending | pulling | starting | running | exited | failed | stopped | lost
    ip           TEXT NOT NULL DEFAULT '',
    container_id TEXT NOT NULL DEFAULT '',
    health       TEXT NOT NULL DEFAULT '',
    exit_code    INTEGER NOT NULL DEFAULT 0,
    error        TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    started_at   INTEGER,
    finished_at  INTEGER,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX tasks_service ON tasks (service_id, desired);
CREATE INDEX tasks_node ON tasks (node_id, desired);

-- Whether new tasks may be placed on a node (§6.4: the controller node runs
-- apps only when enabled).
ALTER TABLE nodes ADD COLUMN schedulable INTEGER NOT NULL DEFAULT 1;
UPDATE nodes SET schedulable = 0 WHERE name = 'ctl-0';

-- +goose Down
ALTER TABLE nodes DROP COLUMN schedulable;
DROP TABLE tasks;
DROP TABLE task_definitions;
DROP TABLE services;
DROP TABLE environments;
DROP TABLE projects;
