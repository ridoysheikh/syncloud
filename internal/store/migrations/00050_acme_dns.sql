-- +goose Up
-- The built-in challenge server: the TXT values it answers, and which domains
-- use it even though they have a provider.
CREATE TABLE acme_dns_records (
    name       TEXT NOT NULL,
    value      TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (name, value)
);
ALTER TABLE dns_zones ADD COLUMN challenge_builtin INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE dns_zones DROP COLUMN challenge_builtin;
DROP TABLE acme_dns_records;
