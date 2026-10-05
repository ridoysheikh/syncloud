-- +goose Up

-- During certificate renewal both the previous and the new serial are
-- accepted, so a crash between issuing and saving cannot lock a node out.
ALTER TABLE nodes ADD COLUMN prev_cert_serial TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE nodes DROP COLUMN prev_cert_serial;
