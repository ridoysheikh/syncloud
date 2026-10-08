-- +goose Up
-- Project settings (Phase 15d): per-environment deploy policy, and
-- environments and projects being deleted with everything in them.
ALTER TABLE environments ADD COLUMN auto_deploy INTEGER NOT NULL DEFAULT 1; -- builds deploy themselves
ALTER TABLE environments ADD COLUMN lock_reason TEXT NOT NULL DEFAULT '';  -- non-empty: deploys are locked
ALTER TABLE environments ADD COLUMN locked_by TEXT NOT NULL DEFAULT '';
ALTER TABLE environments ADD COLUMN locked_at INTEGER;
ALTER TABLE environments ADD COLUMN deleting INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN deleting INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE projects DROP COLUMN deleting;
ALTER TABLE environments DROP COLUMN deleting;
ALTER TABLE environments DROP COLUMN locked_at;
ALTER TABLE environments DROP COLUMN locked_by;
ALTER TABLE environments DROP COLUMN lock_reason;
ALTER TABLE environments DROP COLUMN auto_deploy;
