-- Adds a per-node sync flag so the API can surface whether a node is currently
-- synced. Updated on every heartbeat (RecordHeartbeat). Defaults to 0 (not yet
-- confirmed synced) for existing rows and freshly registered nodes.
ALTER TABLE nodes ADD COLUMN is_synced INTEGER NOT NULL DEFAULT 0 CHECK (is_synced IN (0, 1));
