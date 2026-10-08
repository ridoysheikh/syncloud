-- +goose Up
-- Release commands and build settings (Phase 15b).
-- A new service whose first deployment waits on its pre-deploy jobs (e.g.
-- database migrations) runs no tasks until they pass.
ALTER TABLE services ADD COLUMN held INTEGER NOT NULL DEFAULT 0;
-- Build settings of a Git source: command overrides and after-build checks
-- (JSON), and build-time variables (encrypted JSON).
ALTER TABLE git_sources ADD COLUMN build_settings TEXT NOT NULL DEFAULT '{}';
ALTER TABLE git_sources ADD COLUMN build_vars_enc BLOB;
-- The after-build check run of a build.
ALTER TABLE builds ADD COLUMN check_run_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE builds DROP COLUMN check_run_id;
ALTER TABLE git_sources DROP COLUMN build_vars_enc;
ALTER TABLE git_sources DROP COLUMN build_settings;
ALTER TABLE services DROP COLUMN held;
