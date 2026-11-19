-- +goose Up
-- Phase 28: domains, DNS providers, certificate authorities and the whole
-- life of a certificate. Nothing that exists changes: every certificate row
-- keeps working as an automatic Let's Encrypt (ACME) certificate.

-- A connection to a DNS API (Cloudflare). The credentials are sealed.
CREATE TABLE dns_providers (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    type       TEXT NOT NULL,                -- cloudflare
    config_enc BLOB NOT NULL,                -- sealed JSON (the API token)
    status     TEXT NOT NULL DEFAULT 'ok',   -- ok | error
    last_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

-- A registered domain (zone). The table is not called domains: that holds
-- the hosts of services.
CREATE TABLE dns_zones (
    name             TEXT PRIMARY KEY,
    provider_id      TEXT REFERENCES dns_providers(id) ON DELETE SET NULL,
    provider_zone_id TEXT NOT NULL DEFAULT '',
    mode             TEXT NOT NULL DEFAULT 'manual', -- managed | manual
    wildcard_policy  TEXT NOT NULL DEFAULT 'off',    -- off | on: one wildcard certificate for the zone
    authority_id     TEXT NOT NULL DEFAULT '',       -- the zone's default certificate authority ('' = the cluster's)
    created_at       INTEGER NOT NULL
);

-- Which project may use which names. project '*' means all projects.
CREATE TABLE zone_grants (
    zone      TEXT NOT NULL REFERENCES dns_zones(name) ON DELETE CASCADE,
    project   TEXT NOT NULL,
    suffix    TEXT NOT NULL,                 -- the zone, or a name under it
    wildcards INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (zone, project, suffix)
);

-- What the platform created in a provider's zone, so it is the only thing
-- it ever removes by itself.
CREATE TABLE dns_managed_records (
    provider_id TEXT NOT NULL,
    zone        TEXT NOT NULL,
    record_id   TEXT NOT NULL,
    type        TEXT NOT NULL,
    name        TEXT NOT NULL,
    content     TEXT NOT NULL,
    owner_kind  TEXT NOT NULL DEFAULT '',     -- domain | challenge | point
    owner_id    TEXT NOT NULL DEFAULT '',
    purpose     TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (provider_id, record_id)
);

-- Certificate authorities added by the user (Let's Encrypt itself is built
-- in and uses the controller's ACME settings).
CREATE TABLE cert_authorities (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    kind            TEXT NOT NULL DEFAULT 'acme', -- acme | cloudflare-origin
    preset          TEXT NOT NULL DEFAULT '',     -- zerossl | google | sslcom | buypass | custom | ''
    directory_url   TEXT NOT NULL DEFAULT '',
    email           TEXT NOT NULL DEFAULT '',
    eab_kid         TEXT NOT NULL DEFAULT '',
    eab_hmac_enc    BLOB,
    account_key_enc BLOB,
    key_type        TEXT NOT NULL DEFAULT '',
    provider_id     TEXT NOT NULL DEFAULT '',     -- cloudflare-origin: the DNS provider holding the token
    status          TEXT NOT NULL DEFAULT 'new',  -- new | ok | error
    last_error      TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL
);

-- Certificates: where they come from and their whole life.
ALTER TABLE certificates ADD COLUMN names TEXT NOT NULL DEFAULT '';          -- JSON list; '' = [host]
ALTER TABLE certificates ADD COLUMN method TEXT NOT NULL DEFAULT 'http-01';   -- http-01 | dns-01 | dns-01-manual | import | origin-ca | self-signed
ALTER TABLE certificates ADD COLUMN zone TEXT NOT NULL DEFAULT '';
ALTER TABLE certificates ADD COLUMN source TEXT NOT NULL DEFAULT 'acme';      -- acme | imported | origin-ca | self-signed
ALTER TABLE certificates ADD COLUMN managed INTEGER NOT NULL DEFAULT 0;       -- 1: asked for by a person, not by a route
ALTER TABLE certificates ADD COLUMN authority_id TEXT NOT NULL DEFAULT '';
ALTER TABLE certificates ADD COLUMN fallback_id TEXT NOT NULL DEFAULT '';
ALTER TABLE certificates ADD COLUMN key_type TEXT NOT NULL DEFAULT '';
ALTER TABLE certificates ADD COLUMN auto_renew INTEGER NOT NULL DEFAULT 1;
ALTER TABLE certificates ADD COLUMN not_before INTEGER NOT NULL DEFAULT 0;
ALTER TABLE certificates ADD COLUMN serial TEXT NOT NULL DEFAULT '';
ALTER TABLE certificates ADD COLUMN fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE certificates ADD COLUMN revoked_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE certificates ADD COLUMN alert_days TEXT NOT NULL DEFAULT '';      -- JSON list; '' = 30,14,7,1
ALTER TABLE certificates ADD COLUMN pinned_hosts TEXT NOT NULL DEFAULT '';    -- JSON list
ALTER TABLE certificates ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE certificates ADD COLUMN issuer_name TEXT NOT NULL DEFAULT '';     -- the CA's name as the certificate says it

-- The history of a certificate (kept a year).
CREATE TABLE cert_events (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    cert_key TEXT NOT NULL,
    at       INTEGER NOT NULL,
    kind     TEXT NOT NULL,
    detail   TEXT NOT NULL DEFAULT '',
    actor    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX cert_events_cert ON cert_events (cert_key, at);

-- An ACME order that waits for DNS (or is being verified), so a restart
-- picks it up again.
CREATE TABLE cert_orders (
    cert_key        TEXT PRIMARY KEY,
    authority_id    TEXT NOT NULL DEFAULT '',
    order_uri       TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL,            -- new | awaiting_dns | verifying | issuing | valid | failed
    challenges      TEXT NOT NULL DEFAULT '[]', -- JSON: the TXT records the order needs
    key_enc         BLOB,                     -- the private key of the certificate being ordered
    created_at      INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL DEFAULT 0,
    last_checked_at INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT NOT NULL DEFAULT ''
);

-- A certificate request (CSR) waiting for the provider's signed certificate.
CREATE TABLE cert_requests (
    id         TEXT PRIMARY KEY,
    names      TEXT NOT NULL,
    key_type   TEXT NOT NULL DEFAULT '',
    key_enc    BLOB NOT NULL,
    csr_pem    TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE cert_requests;
DROP TABLE cert_orders;
DROP TABLE cert_events;
DROP TABLE cert_authorities;
DROP TABLE dns_managed_records;
DROP TABLE zone_grants;
DROP TABLE dns_zones;
DROP TABLE dns_providers;
