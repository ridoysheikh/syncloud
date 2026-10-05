-- +goose Up

-- Stable virtual IPs for services (§8.6): an index into 10.92.0.0/16,
-- released with the same cool-down as node addresses.
ALTER TABLE services ADD COLUMN vip_index INTEGER;
CREATE UNIQUE INDEX services_vip ON services (vip_index);

-- +goose StatementBegin
CREATE TRIGGER services_vip_release AFTER DELETE ON services WHEN OLD.vip_index IS NOT NULL
BEGIN
    INSERT OR REPLACE INTO ipam_released (kind, idx, released_at) VALUES ('vip', OLD.vip_index, unixepoch());
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER services_vip_release;
DROP INDEX services_vip;
ALTER TABLE services DROP COLUMN vip_index;
