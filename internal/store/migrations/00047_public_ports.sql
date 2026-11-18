-- +goose Up
-- Chosen public ports and the port table (Phase 26).

-- service_routing loses UNIQUE (public_port): the same number may be held
-- on different load balancers, and the port table below is what refuses a
-- second holder on one node. It gains the port's mode (auto: assigned from
-- the range; chosen: given by the user), PROXY protocol and TLS at the
-- load balancer (tcp only; off by default, as before).
CREATE TABLE service_routing_new (
    service_id     TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    port_name      TEXT NOT NULL,
    generated      INTEGER NOT NULL DEFAULT 1,
    label          TEXT UNIQUE,
    public_port    INTEGER,
    allow          TEXT NOT NULL DEFAULT '[]',
    port_mode      TEXT NOT NULL DEFAULT 'auto', -- auto | chosen
    proxy_protocol INTEGER NOT NULL DEFAULT 0,   -- tcp: send PROXY v2 to the app
    tls            TEXT NOT NULL DEFAULT '',     -- tcp: '' (raw) | terminate
    PRIMARY KEY (service_id, port_name)
);
INSERT INTO service_routing_new (service_id, port_name, generated, label, public_port, allow)
    SELECT service_id, port_name, generated, label, public_port, allow FROM service_routing;
DROP TABLE service_routing;
ALTER TABLE service_routing_new RENAME TO service_routing;

-- Every public port on every load-balancer node it is served from. The
-- primary key is what makes a duplicate impossible. Filled from the
-- existing ports by the controller at start (it knows the protocols and
-- the nodes).
CREATE TABLE public_ports (
    node       TEXT NOT NULL,    -- node name
    protocol   TEXT NOT NULL,    -- tcp | udp
    port       INTEGER NOT NULL,
    owner_kind TEXT NOT NULL,    -- service | database
    owner_id   TEXT NOT NULL,
    port_name  TEXT NOT NULL,    -- the service's port, or rw/ro for databases
    mode       TEXT NOT NULL DEFAULT 'auto',
    created_at INTEGER NOT NULL, -- unix nanoseconds: the first holder of a number keeps its Traefik names
    PRIMARY KEY (node, protocol, port)
);
CREATE INDEX public_ports_owner ON public_ports (owner_kind, owner_id);

-- +goose Down
DROP TABLE public_ports;
CREATE TABLE service_routing_old (
    service_id  TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    port_name   TEXT NOT NULL,
    generated   INTEGER NOT NULL DEFAULT 1,
    label       TEXT UNIQUE,
    public_port INTEGER UNIQUE,
    allow       TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (service_id, port_name)
);
INSERT INTO service_routing_old (service_id, port_name, generated, label, public_port, allow)
    SELECT service_id, port_name, generated, label, public_port, allow FROM service_routing;
DROP TABLE service_routing;
ALTER TABLE service_routing_old RENAME TO service_routing;
