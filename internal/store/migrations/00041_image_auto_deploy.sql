-- +goose Up
-- Auto-deploy for services that run a ready-made image: new pushes of the
-- image's tag are deployed, found by watching the registry (instantly for
-- the built-in one) or reported by CI through a deploy webhook.
CREATE TABLE image_watches (
    service_id     TEXT PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    auto_deploy    INTEGER NOT NULL DEFAULT 0, -- watch the registry and deploy new pushes
    poll_seconds   INTEGER NOT NULL DEFAULT 300, -- how often other registries are checked
    webhook_hash   TEXT NOT NULL DEFAULT '',   -- sha256 of the deploy webhook token; '' = none
    checked_at     INTEGER,
    last_digest    TEXT NOT NULL DEFAULT '',   -- what the tag pointed to at the last check
    last_error     TEXT NOT NULL DEFAULT '',
    last_deploy_at INTEGER,
    updated_at     INTEGER NOT NULL
);
CREATE UNIQUE INDEX image_watches_webhook ON image_watches (webhook_hash) WHERE webhook_hash <> '';

-- +goose Down
DROP TABLE image_watches;
