-- +goose Up

-- Git sources and builds (§5.8). One source per service.
CREATE TABLE git_sources (
    id              TEXT PRIMARY KEY,
    service_id      TEXT NOT NULL UNIQUE REFERENCES services(id) ON DELETE CASCADE,
    url             TEXT NOT NULL,              -- https://… (SSH comes later)
    branch          TEXT NOT NULL,
    dockerfile      TEXT NOT NULL DEFAULT 'Dockerfile',
    context         TEXT NOT NULL DEFAULT '',   -- subdirectory of the repository
    token_enc       BLOB,                       -- sealed HTTPS token (optional)
    auto_deploy     INTEGER NOT NULL DEFAULT 1,
    poll_seconds    INTEGER NOT NULL DEFAULT 60,
    webhook_secret  TEXT NOT NULL,
    last_sha        TEXT NOT NULL DEFAULT '',
    last_checked_at INTEGER,
    last_error      TEXT NOT NULL DEFAULT '',
    failures        INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL
);

CREATE TABLE builds (
    id          TEXT PRIMARY KEY,
    service_id  TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    sha         TEXT NOT NULL,
    ref         TEXT NOT NULL,
    trigger     TEXT NOT NULL,              -- poll | webhook | manual
    status      TEXT NOT NULL,              -- queued | building | succeeded | failed | cancelled
    image       TEXT NOT NULL,              -- @registry/<project>/<service>:<sha>
    run_id      TEXT NOT NULL DEFAULT '',   -- the BuildKit job run (logs)
    message     TEXT NOT NULL DEFAULT '',
    deployed    INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    started_at  INTEGER,
    finished_at INTEGER
);
CREATE UNIQUE INDEX builds_sha ON builds (service_id, sha);  -- a commit builds once
CREATE INDEX builds_status ON builds (status);

-- +goose Down
DROP TABLE builds;
DROP TABLE git_sources;
