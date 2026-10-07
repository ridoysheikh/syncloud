-- +goose Up
-- The nodes a project's services and jobs may run on, by name (JSON array;
-- empty = any schedulable node). Services can narrow it further (§6.3).
ALTER TABLE projects ADD COLUMN nodes TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE projects DROP COLUMN nodes;
