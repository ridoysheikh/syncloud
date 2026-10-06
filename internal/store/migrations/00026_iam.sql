-- +goose Up

-- IAM (§7): users beyond root (people and service accounts), groups,
-- policies, roles, temporary credentials and MFA.
ALTER TABLE users ADD COLUMN kind TEXT NOT NULL DEFAULT 'user';      -- user | service
ALTER TABLE users ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN mfa_secret_enc BLOB;                    -- TOTP secret, sealed
ALTER TABLE users ADD COLUMN mfa_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN last_login_at INTEGER;

ALTER TABLE sessions ADD COLUMN mfa INTEGER NOT NULL DEFAULT 0;      -- signed in with a TOTP code

ALTER TABLE access_keys ADD COLUMN expires_at INTEGER;
ALTER TABLE access_keys ADD COLUMN allowed_ips TEXT NOT NULL DEFAULT '[]';
ALTER TABLE api_tokens ADD COLUMN allowed_ips TEXT NOT NULL DEFAULT '[]';

CREATE TABLE iam_groups (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);

CREATE TABLE iam_group_members (
    group_id TEXT NOT NULL REFERENCES iam_groups(id) ON DELETE CASCADE,
    user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);

-- Customer policies. Managed ones are built in (internal/iam).
CREATE TABLE iam_policies (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    document    TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- policy is a customer policy ID (pol_…) or a managed name ("ReadOnly",
-- "Developer:shop").
CREATE TABLE iam_attachments (
    principal_type TEXT NOT NULL,              -- user | group | role
    principal_id   TEXT NOT NULL,
    policy         TEXT NOT NULL,
    created_at     INTEGER NOT NULL,
    PRIMARY KEY (principal_type, principal_id, policy)
);
CREATE INDEX iam_attachments_policy ON iam_attachments (policy);

CREATE TABLE iam_roles (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE,
    description         TEXT NOT NULL DEFAULT '',
    trust               TEXT NOT NULL,          -- JSON: who may assume it
    max_session_seconds INTEGER NOT NULL DEFAULT 3600,
    created_at          INTEGER NOT NULL
);

-- Temporary credentials (§7.1): role sessions (STS), device-login and Cloud
-- Shell sessions. The key ID is SYNAS…; requests also carry the session
-- token, of which only the hash is kept.
CREATE TABLE temp_credentials (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,                  -- role | device | shell
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id     TEXT REFERENCES iam_roles(id) ON DELETE CASCADE,
    secret_enc  BLOB NOT NULL,
    token_hash  TEXT NOT NULL,
    mfa         INTEGER NOT NULL DEFAULT 0,
    source_ip   TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    last_used_at INTEGER
);
CREATE INDEX temp_credentials_expires ON temp_credentials (expires_at);

-- synctl login (device flow).
CREATE TABLE device_codes (
    device_hash TEXT PRIMARY KEY,
    user_code   TEXT NOT NULL UNIQUE,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    approved_by TEXT REFERENCES users(id) ON DELETE CASCADE,
    credential  TEXT                            -- temp_credentials.id once approved
);

CREATE INDEX audit_events_actor ON audit_events (actor_id, at);
CREATE INDEX audit_events_action ON audit_events (action, at);

-- +goose Down
DROP INDEX audit_events_action;
DROP INDEX audit_events_actor;
DROP TABLE device_codes;
DROP TABLE temp_credentials;
DROP TABLE iam_roles;
DROP TABLE iam_attachments;
DROP TABLE iam_policies;
DROP TABLE iam_group_members;
DROP TABLE iam_groups;
ALTER TABLE api_tokens DROP COLUMN allowed_ips;
ALTER TABLE access_keys DROP COLUMN allowed_ips;
ALTER TABLE access_keys DROP COLUMN expires_at;
ALTER TABLE sessions DROP COLUMN mfa;
ALTER TABLE users DROP COLUMN last_login_at;
ALTER TABLE users DROP COLUMN mfa_enabled;
ALTER TABLE users DROP COLUMN mfa_secret_enc;
ALTER TABLE users DROP COLUMN disabled;
ALTER TABLE users DROP COLUMN kind;
