-- One-node-per-address: record the network address a node registered from so we
-- can enforce a single active node per IP (the "one node per location, for now"
-- rule). Empty string for nodes created before this migration / via admin seed.
ALTER TABLE nodes ADD COLUMN registered_ip TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_nodes_ip ON nodes(registered_ip);
