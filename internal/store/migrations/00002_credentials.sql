-- +goose Up

-- Access keys for signed API requests (§7.1). The secret is needed to verify
-- HMAC signatures, so it is stored encrypted with the master key.
CREATE TABLE access_keys (
    id           TEXT PRIMARY KEY,           -- SYNAK…
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    secret_enc   BLOB NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    last_used_ip TEXT
);
CREATE INDEX access_keys_user ON access_keys(user_id);

-- Personal access tokens (bearer). Only the SHA-256 is stored.
CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,           -- tok_…
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER,                    -- NULL: no expiry
    last_used_at INTEGER,
    last_used_ip TEXT
);
CREATE INDEX api_tokens_user ON api_tokens(user_id);

-- +goose Down
DROP TABLE api_tokens;
DROP TABLE access_keys;
