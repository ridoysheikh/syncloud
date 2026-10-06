-- +goose Up

-- Registry notifications (§5.9): distribution posts every manifest push,
-- pull and delete to the controller. Events are kept for 30 days; the
-- per-image counters live on until the image is deleted.
CREATE TABLE registry_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id    TEXT NOT NULL UNIQUE,
    at          INTEGER NOT NULL,
    action      TEXT NOT NULL,
    repository  TEXT NOT NULL,
    tag         TEXT NOT NULL DEFAULT '',
    digest      TEXT NOT NULL DEFAULT '',
    actor       TEXT NOT NULL DEFAULT '',
    addr        TEXT NOT NULL DEFAULT '',
    user_agent  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX registry_events_repo ON registry_events (repository, at);

CREATE TABLE registry_image_stats (
    repository      TEXT NOT NULL,
    digest          TEXT NOT NULL,
    pushes          INTEGER NOT NULL DEFAULT 0,
    pulls           INTEGER NOT NULL DEFAULT 0,
    last_pushed_at  INTEGER,
    last_pulled_at  INTEGER,
    PRIMARY KEY (repository, digest)
);

-- +goose Down
DROP TABLE registry_image_stats;
DROP TABLE registry_events;
