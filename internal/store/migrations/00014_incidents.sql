-- +goose Up

-- Service incidents (§5.6): opened when a service turns degraded or down,
-- closed on recovery. Only transitions are stored.
CREATE TABLE incidents (
    id          TEXT PRIMARY KEY,
    service_id  TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    state       TEXT NOT NULL,          -- degraded | down (worst seen)
    cause       TEXT NOT NULL,
    opened_at   INTEGER NOT NULL,
    closed_at   INTEGER
);
CREATE INDEX incidents_service ON incidents (service_id, opened_at);

-- +goose Down
DROP TABLE incidents;
