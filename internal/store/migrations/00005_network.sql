-- +goose Up

-- Mesh addresses (§8, §8.2): a WireGuard IP index and a container /24 index
-- per node. Indexes map to addresses in internal/mesh.
CREATE TABLE node_network (
    node_id      TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    mesh_index   INTEGER NOT NULL UNIQUE,
    subnet_index INTEGER NOT NULL UNIQUE,
    public_key   TEXT NOT NULL DEFAULT '',
    endpoint     TEXT NOT NULL DEFAULT '',   -- host:port peers dial
    updated_at   INTEGER NOT NULL
);

-- Released indexes stay out of the pool for a cool-down, so stale traffic
-- never reaches a new owner.
CREATE TABLE ipam_released (
    kind        TEXT NOT NULL,               -- mesh | subnet
    idx         INTEGER NOT NULL,
    released_at INTEGER NOT NULL,
    PRIMARY KEY (kind, idx)
);

-- +goose StatementBegin
CREATE TRIGGER node_network_release AFTER DELETE ON node_network
BEGIN
    INSERT OR REPLACE INTO ipam_released (kind, idx, released_at) VALUES
        ('mesh', OLD.mesh_index, unixepoch()),
        ('subnet', OLD.subnet_index, unixepoch());
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER node_network_release;
DROP TABLE ipam_released;
DROP TABLE node_network;
