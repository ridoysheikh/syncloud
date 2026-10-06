-- +goose Up

-- Traefik middleware presets (§5.7): reusable per project, attached to
-- services (every HTTP route of the service uses them).
CREATE TABLE middlewares (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,              -- rate-limit, basic-auth, ip-allowlist, … (internal/traefik)
    config     TEXT NOT NULL,              -- JSON; basic-auth passwords are bcrypt hashes
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (project_id, name)
);

CREATE TABLE middleware_services (
    middleware_id TEXT NOT NULL REFERENCES middlewares(id) ON DELETE CASCADE,
    service_id    TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    PRIMARY KEY (middleware_id, service_id)
);
CREATE INDEX middleware_services_service ON middleware_services (service_id);

-- +goose Down
DROP TABLE middleware_services;
DROP TABLE middlewares;
