-- +goose Up

-- Host firewall policies (§8.3). targets: JSON array of node IDs, or ["*"]
-- for every node. rules: JSON array of {protocol, ports, sources, description}.
CREATE TABLE firewall_policies (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    targets     TEXT NOT NULL DEFAULT '["*"]',
    rules       TEXT NOT NULL DEFAULT '[]',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- SSH stays reachable by default; restrict it to known addresses in the dashboard.
INSERT INTO firewall_policies (id, name, description, targets, rules, created_at, updated_at) VALUES (
    'fwp_default', 'default', 'SSH from anywhere. Restrict the source to your addresses.', '["*"]',
    '[{"protocol":"tcp","ports":"22","sources":[],"description":"SSH"}]',
    unixepoch(), unixepoch());

-- +goose Down
DROP TABLE firewall_policies;
