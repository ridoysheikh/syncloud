-- +goose Up
-- Ports and domains (Phase 15c).

-- How each port of a service is reached, outside its revisions (changing
-- it does not redeploy). No row: the defaults (generated address on,
-- not public).
CREATE TABLE service_routing (
    service_id  TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    port_name   TEXT NOT NULL,
    generated   INTEGER NOT NULL DEFAULT 1, -- http: serve the generated address
    label       TEXT UNIQUE,                -- http: <label>.<base domain> instead of the generated name
    public_port INTEGER UNIQUE,             -- tcp/udp: the port on the controller and edge nodes
    allow       TEXT NOT NULL DEFAULT '[]', -- tcp/udp: client CIDRs (JSON; empty = anyone)
    PRIMARY KEY (service_id, port_name)
);

-- Custom domains gain a path prefix (several services can share a host),
-- prefix stripping and redirects. Rebuilt for UNIQUE (host, path).
CREATE TABLE domains_new (
    id           TEXT PRIMARY KEY,
    service_id   TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    host         TEXT NOT NULL,
    path         TEXT NOT NULL DEFAULT '',  -- "" or a prefix like /api
    port_name    TEXT NOT NULL,
    strip_prefix INTEGER NOT NULL DEFAULT 0,
    redirect_to  TEXT NOT NULL DEFAULT '',  -- answer with a 301 to this host instead
    created_at   INTEGER NOT NULL,
    UNIQUE (host, path)
);
INSERT INTO domains_new (id, service_id, host, port_name, created_at) SELECT id, service_id, host, port_name, created_at FROM domains;
DROP TABLE domains;
ALTER TABLE domains_new RENAME TO domains;
CREATE INDEX domains_service ON domains (service_id);

-- +goose Down
CREATE TABLE domains_old (
    id         TEXT PRIMARY KEY,
    service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    host       TEXT NOT NULL UNIQUE,
    port_name  TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
INSERT OR IGNORE INTO domains_old (id, service_id, host, port_name, created_at) SELECT id, service_id, host, port_name, created_at FROM domains WHERE redirect_to = '';
DROP TABLE domains;
ALTER TABLE domains_old RENAME TO domains;
CREATE INDEX domains_service ON domains (service_id);
DROP TABLE service_routing;
