-- +goose Up
-- A project's traffic protection on its load balancers (Phase 25): spec is
-- JSON (rate limit, concurrency, body limit, block and bypass lists, Redis,
-- network guard); secret is the bypass header's value, sealed.
CREATE TABLE project_protection (
    project_id TEXT PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
    spec       TEXT NOT NULL,
    secret     BLOB,
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE project_protection;
