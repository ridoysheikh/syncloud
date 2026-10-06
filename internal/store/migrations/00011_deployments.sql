-- +goose Up

-- Rollouts from one revision to another (§5.4).
CREATE TABLE deployments (
    id          TEXT PRIMARY KEY,
    service_id  TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    from_rev    INTEGER NOT NULL,       -- 0 for the first deployment
    to_rev      INTEGER NOT NULL,
    status      TEXT NOT NULL,          -- in_progress | succeeded | failed | rolled_back | superseded
    failed      INTEGER NOT NULL DEFAULT 0, -- tasks of to_rev that failed or turned unhealthy
    message     TEXT NOT NULL DEFAULT '',
    started_at  INTEGER NOT NULL,
    finished_at INTEGER
);
CREATE INDEX deployments_service ON deployments (service_id, started_at);

-- +goose Down
DROP TABLE deployments;
