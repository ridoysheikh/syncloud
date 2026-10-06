-- +goose Up

-- Target tracking autoscaling (§5.5): one policy per service.
CREATE TABLE autoscaling_policies (
    service_id         TEXT PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    enabled            INTEGER NOT NULL DEFAULT 1,
    min_count          INTEGER NOT NULL,
    max_count          INTEGER NOT NULL,
    metric             TEXT NOT NULL,       -- cpu | memory | rps | latency
    target             REAL NOT NULL,
    scale_out_cooldown INTEGER NOT NULL,    -- seconds
    scale_in_cooldown  INTEGER NOT NULL,    -- seconds
    scale_in_checks    INTEGER NOT NULL,    -- consecutive evaluations below target
    updated_at         INTEGER NOT NULL,
    updated_by         TEXT NOT NULL DEFAULT ''
);

-- Every change the autoscaler makes, with the reason.
CREATE TABLE scaling_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id  TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    at          INTEGER NOT NULL,
    from_count  INTEGER NOT NULL,
    to_count    INTEGER NOT NULL,
    metric      TEXT NOT NULL,
    value       REAL,
    target      REAL NOT NULL,
    reason      TEXT NOT NULL
);
CREATE INDEX scaling_events_service ON scaling_events (service_id, id);

-- +goose Down
DROP TABLE scaling_events;
DROP TABLE autoscaling_policies;
