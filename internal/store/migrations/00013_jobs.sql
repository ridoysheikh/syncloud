-- +goose Up

-- Jobs run to completion (§5.11): one-off, scheduled, pre- and post-deploy.
CREATE TABLE jobs (
    id                TEXT PRIMARY KEY,
    environment_id    TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    spec              TEXT NOT NULL,          -- JSON (internal/jobs)
    last_scheduled_at INTEGER,                -- last cron slot handled
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    UNIQUE (environment_id, name)
);

CREATE TABLE job_runs (
    id             TEXT PRIMARY KEY,          -- also the task ID on the node (run_…)
    job_id         TEXT REFERENCES jobs(id) ON DELETE CASCADE, -- NULL for ad-hoc runs
    environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id     TEXT REFERENCES services(id) ON DELETE SET NULL,
    revision       INTEGER NOT NULL DEFAULT 0,
    trigger        TEXT NOT NULL,             -- manual | schedule | pre-deploy | post-deploy | retry
    attempt        INTEGER NOT NULL DEFAULT 1,
    spec           TEXT NOT NULL,             -- the task definition used (JSON)
    status         TEXT NOT NULL,             -- pending | running | succeeded | failed | timed_out | cancelled | skipped
    node_id        TEXT REFERENCES nodes(id) ON DELETE SET NULL,
    exit_code      INTEGER,
    message        TEXT NOT NULL DEFAULT '',
    deployment_id  TEXT,                      -- for hooks
    created_at     INTEGER NOT NULL,
    started_at     INTEGER,
    finished_at    INTEGER
);
CREATE INDEX job_runs_job ON job_runs (job_id, created_at);
CREATE INDEX job_runs_status ON job_runs (status);

-- Deployments can wait for a pre-deploy hook before switching revisions.
-- (status 'waiting_hook'; services.revision changes when the hook succeeds)

-- +goose Down
DROP TABLE job_runs;
DROP TABLE jobs;
