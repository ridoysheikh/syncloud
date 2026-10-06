-- +goose Up

-- Alerts (§9): notification channels, rules, the state of every rule
-- instance (so a restart does not notify again) and the alert history.
CREATE TABLE alert_channels (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    type        TEXT NOT NULL,          -- webhook | slack | discord | telegram | email
    config_enc  BLOB NOT NULL,          -- sealed JSON (URLs and tokens are secrets)
    summary     TEXT NOT NULL DEFAULT '', -- shown instead of the config
    created_at  INTEGER NOT NULL
);

CREATE TABLE alert_rules (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    spec        TEXT NOT NULL,          -- JSON
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE alert_states (
    rule_id     TEXT NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    key         TEXT NOT NULL,          -- the instance: a service, node or series
    label       TEXT NOT NULL,
    state       TEXT NOT NULL,          -- pending | firing
    since       INTEGER NOT NULL,
    value       REAL,
    message     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (rule_id, key)
);

CREATE TABLE alert_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id     TEXT NOT NULL,
    rule_name   TEXT NOT NULL,
    severity    TEXT NOT NULL,
    kind        TEXT NOT NULL,          -- firing | resolved | event
    key         TEXT NOT NULL,
    label       TEXT NOT NULL,
    message     TEXT NOT NULL,
    value       REAL,
    delivery    TEXT NOT NULL DEFAULT '', -- per channel: ok or the error
    at          INTEGER NOT NULL
);
CREATE INDEX alert_events_at ON alert_events (at);

-- +goose Down
DROP TABLE alert_events;
DROP TABLE alert_states;
DROP TABLE alert_rules;
DROP TABLE alert_channels;
