-- +goose Up

-- Security groups (§8.3): allow rules between containers, per project. A
-- service uses the groups attached to it, or its project's default group.
CREATE TABLE security_groups (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    inbound     TEXT NOT NULL,            -- JSON rules (internal/secgroup)
    outbound    TEXT NOT NULL,
    is_default  INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (project_id, name)
);

CREATE TABLE security_group_services (
    group_id   TEXT NOT NULL REFERENCES security_groups(id) ON DELETE CASCADE,
    service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, service_id)
);
CREATE INDEX security_group_services_service ON security_group_services (service_id);

INSERT INTO security_groups (id, project_id, name, description, inbound, outbound, is_default, created_at, updated_at)
SELECT 'sg_' || lower(hex(randomblob(8))), id, 'default',
       'Services in the same environment can reach each other; all outbound traffic is allowed.',
       '[{"protocol":"any","ports":"","peers":["environment:self"],"description":"Services in the same environment"}]',
       '[{"protocol":"any","ports":"","peers":["any"],"description":"All outbound traffic"}]',
       1, created_at, created_at
FROM projects;

-- Job run containers are security group members too, so their address is kept.
ALTER TABLE job_runs ADD COLUMN ip TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE job_runs DROP COLUMN ip;
DROP TABLE security_group_services;
DROP TABLE security_groups;
