-- +goose Up
-- Detailed deployment logs: each step can name the node and task it ran on
-- and a level, and keeps millisecond time (image pulls and health checks of
-- one task are often within the same second).
ALTER TABLE deployment_events ADD COLUMN level TEXT NOT NULL DEFAULT 'info'; -- info | warn | error
ALTER TABLE deployment_events ADD COLUMN node TEXT NOT NULL DEFAULT '';     -- node name
ALTER TABLE deployment_events ADD COLUMN task_id TEXT NOT NULL DEFAULT '';
ALTER TABLE deployment_events ADD COLUMN at_ms INTEGER NOT NULL DEFAULT 0;  -- unix ms; 0 = only "at" (seconds)

-- +goose Down
ALTER TABLE deployment_events DROP COLUMN at_ms;
ALTER TABLE deployment_events DROP COLUMN task_id;
ALTER TABLE deployment_events DROP COLUMN node;
ALTER TABLE deployment_events DROP COLUMN level;
