-- +goose Up
-- A domain can delegate its _acme-challenge name (a CNAME) to another domain
-- whose DNS provider SynCloud can write, so certificates renew by themselves.
ALTER TABLE dns_zones ADD COLUMN challenge_zone TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE dns_zones DROP COLUMN challenge_zone;
