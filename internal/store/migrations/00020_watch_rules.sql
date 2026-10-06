-- +goose Up

-- Watch rules (§5.8): a branch pattern ("main", "release/*"), an optional
-- tag pattern ("v*"), path filters for monorepos, the builder (auto =
-- Dockerfile, then Nixpacks, then static) and the last seen commit of every
-- matching ref.
ALTER TABLE git_sources ADD COLUMN tags TEXT NOT NULL DEFAULT '';
ALTER TABLE git_sources ADD COLUMN paths TEXT NOT NULL DEFAULT '[]';
ALTER TABLE git_sources ADD COLUMN builder TEXT NOT NULL DEFAULT 'auto';
ALTER TABLE git_sources ADD COLUMN ref_shas TEXT NOT NULL DEFAULT '{}';
ALTER TABLE git_sources ADD COLUMN last_webhook_at INTEGER;

-- The commit a build is compared with for path filters ('' = always build).
ALTER TABLE builds ADD COLUMN base_sha TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE builds DROP COLUMN base_sha;
ALTER TABLE git_sources DROP COLUMN last_webhook_at;
ALTER TABLE git_sources DROP COLUMN ref_shas;
ALTER TABLE git_sources DROP COLUMN builder;
ALTER TABLE git_sources DROP COLUMN paths;
ALTER TABLE git_sources DROP COLUMN tags;
