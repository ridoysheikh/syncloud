-- +goose Up
-- The nodes that serve a project's public traffic, by name, in order (JSON
-- array; empty = the controller and edge pools, D22). ingress_move holds the
-- previous ingress while DNS moves (JSON; '' when settled).
ALTER TABLE projects ADD COLUMN ingress_nodes TEXT NOT NULL DEFAULT '[]';
ALTER TABLE projects ADD COLUMN ingress_move TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE projects DROP COLUMN ingress_move;
ALTER TABLE projects DROP COLUMN ingress_nodes;
