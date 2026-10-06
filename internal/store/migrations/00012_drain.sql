-- +goose Up

-- A draining node gets no new tasks, and its tasks move to other nodes
-- (replacements first, §5.4).
ALTER TABLE nodes ADD COLUMN draining INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE nodes DROP COLUMN draining;
