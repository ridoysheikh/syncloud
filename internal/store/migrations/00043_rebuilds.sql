-- +goose Up
-- A commit can be built again by hand (a rebuild); automatic triggers still
-- build each commit once (see builds.enqueue).
DROP INDEX builds_sha;
CREATE INDEX builds_sha ON builds (service_id, sha);

-- +goose Down
DELETE FROM builds WHERE rowid NOT IN (SELECT min(rowid) FROM builds GROUP BY service_id, sha);
DROP INDEX builds_sha;
CREATE UNIQUE INDEX builds_sha ON builds (service_id, sha);
