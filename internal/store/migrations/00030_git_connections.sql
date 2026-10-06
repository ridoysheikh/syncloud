-- +goose Up
-- Git provider connections (§5.8): a GitHub App created from a manifest, or
-- an access token for GitHub, GitLab or Gitea/Forgejo. Keys and tokens are
-- sealed with the master key.
CREATE TABLE git_connections (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    name        TEXT NOT NULL UNIQUE,
    api_url     TEXT NOT NULL,
    web_url     TEXT NOT NULL,
    account     TEXT NOT NULL DEFAULT '',
    app_id      INTEGER NOT NULL DEFAULT 0,
    app_slug    TEXT NOT NULL DEFAULT '',
    secrets_enc BLOB NOT NULL,
    created_at  INTEGER NOT NULL
);

-- A Git source may come from a connection: the repository is then named
-- (owner/repo), tokens are minted by the connection, and the webhook is
-- created on the repository by SynCloud.
ALTER TABLE git_sources ADD COLUMN connection_id TEXT NOT NULL DEFAULT '';
ALTER TABLE git_sources ADD COLUMN repo TEXT NOT NULL DEFAULT '';
ALTER TABLE git_sources ADD COLUMN hook_id TEXT NOT NULL DEFAULT '';
ALTER TABLE git_sources ADD COLUMN hook_error TEXT NOT NULL DEFAULT '';
CREATE INDEX git_sources_connection ON git_sources (connection_id, repo);

-- +goose Down
DROP INDEX git_sources_connection;
ALTER TABLE git_sources DROP COLUMN hook_error;
ALTER TABLE git_sources DROP COLUMN hook_id;
ALTER TABLE git_sources DROP COLUMN repo;
ALTER TABLE git_sources DROP COLUMN connection_id;
DROP TABLE git_connections;
