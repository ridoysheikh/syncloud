-- +goose Up
-- Deployment history (Phase 15a): what started each deployment, the build
-- and image it rolled out, a summary of what changed, and a timeline.
ALTER TABLE deployments ADD COLUMN trigger TEXT NOT NULL DEFAULT '';  -- manual | git | rollback | auto-rollback | variables | redeploy | config
ALTER TABLE deployments ADD COLUMN actor TEXT NOT NULL DEFAULT '';    -- user ID, or system
ALTER TABLE deployments ADD COLUMN build_id TEXT NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN image TEXT NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN changes TEXT NOT NULL DEFAULT '[]'; -- JSON []workload.Change
CREATE INDEX deployments_started ON deployments (started_at);

CREATE TABLE deployment_events (
    id            INTEGER PRIMARY KEY,
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    at            INTEGER NOT NULL,
    kind          TEXT NOT NULL,
    message       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX deployment_events_dep ON deployment_events (deployment_id, id);

-- Registry cleanup keeps the images of each service's last N revisions.
ALTER TABLE projects ADD COLUMN rollback_window INTEGER NOT NULL DEFAULT 10;

-- +goose Down
ALTER TABLE projects DROP COLUMN rollback_window;
DROP TABLE deployment_events;
DROP INDEX deployments_started;
ALTER TABLE deployments DROP COLUMN changes;
ALTER TABLE deployments DROP COLUMN image;
ALTER TABLE deployments DROP COLUMN build_id;
ALTER TABLE deployments DROP COLUMN actor;
ALTER TABLE deployments DROP COLUMN trigger;
