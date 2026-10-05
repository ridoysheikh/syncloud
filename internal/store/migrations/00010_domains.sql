-- +goose Up

-- Custom domains routed to a service port (§5.7).
CREATE TABLE domains (
    id         TEXT PRIMARY KEY,
    service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    host       TEXT NOT NULL UNIQUE,
    port_name  TEXT NOT NULL,      -- the service's http port
    created_at INTEGER NOT NULL
);
CREATE INDEX domains_service ON domains (service_id);

-- +goose Down
DROP TABLE domains;
