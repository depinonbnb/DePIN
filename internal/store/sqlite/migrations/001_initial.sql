-- 001_initial.sql
-- Initial schema for the DePINonBNB persistence layer.
-- See docs/design/persistence.md §2 for the full specification.
-- All tables use STRICT mode; all timestamps are unix milliseconds.

CREATE TABLE IF NOT EXISTS nodes (
    id                       TEXT    PRIMARY KEY,
    wallet_address           TEXT    NOT NULL,
    node_type                TEXT    NOT NULL,
    verification_method      TEXT    NOT NULL,
    rpc_endpoint             TEXT    NOT NULL DEFAULT '',
    auth_token               TEXT    NOT NULL DEFAULT '',
    registered_at            INTEGER NOT NULL,
    last_verified_at         INTEGER NOT NULL DEFAULT 0,
    last_heartbeat_at        INTEGER NOT NULL DEFAULT 0,
    total_challenges_passed  INTEGER NOT NULL DEFAULT 0,
    total_challenges_failed  INTEGER NOT NULL DEFAULT 0,
    total_uptime_minutes     INTEGER NOT NULL DEFAULT 0,
    total_points             INTEGER NOT NULL DEFAULT 0,
    is_active                INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0,1)),
    cheat_status             TEXT    NOT NULL DEFAULT 'clean',
    warning_count            INTEGER NOT NULL DEFAULT 0,
    cheat_reason             TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX IF NOT EXISTS idx_nodes_wallet  ON nodes(wallet_address);
CREATE INDEX IF NOT EXISTS idx_nodes_flagged ON nodes(cheat_status) WHERE is_active = 1;

CREATE TABLE IF NOT EXISTS verification_results (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id          TEXT    NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    challenge_id     TEXT    NOT NULL,
    passed           INTEGER NOT NULL CHECK (passed IN (0,1)),
    response_time_ms INTEGER NOT NULL DEFAULT 0,
    failure_reason   TEXT    NOT NULL DEFAULT '',
    suspicious       INTEGER NOT NULL DEFAULT 0 CHECK (suspicious IN (0,1)),
    suspicious_note  TEXT    NOT NULL DEFAULT '',
    ts               INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_vresults_node_ts ON verification_results(node_id, ts DESC);

CREATE TABLE IF NOT EXISTS heartbeats (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id      TEXT    NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    ts           INTEGER NOT NULL,
    block_number INTEGER NOT NULL DEFAULT 0,
    is_synced    INTEGER NOT NULL DEFAULT 0 CHECK (is_synced IN (0,1)),
    latency_ms   INTEGER NOT NULL DEFAULT 0,
    peers_count  INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX IF NOT EXISTS idx_heartbeats_node_ts ON heartbeats(node_id, ts DESC);

CREATE TABLE IF NOT EXISTS pending_challenges (
    id               TEXT    PRIMARY KEY,
    node_id          TEXT    NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    challenge_type   TEXT    NOT NULL,
    params_json      TEXT    NOT NULL DEFAULT '{}',
    expected_answer  TEXT    NOT NULL,
    created_at       INTEGER NOT NULL,
    expires_at       INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_pending_expires ON pending_challenges(expires_at);

CREATE TABLE IF NOT EXISTS suspicious_events (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id TEXT    NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    event   TEXT    NOT NULL,
    ts      INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_suspicious_node_ts ON suspicious_events(node_id, ts DESC);

-- admin_actions intentionally has NO ON DELETE CASCADE per design doc §9 (open
-- question 2): audit history must outlive the row it references. We accept the
-- possibility of orphaned admin_actions rows since nodes are never hard-deleted
-- in normal operation.
CREATE TABLE IF NOT EXISTS admin_actions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id       TEXT    NOT NULL REFERENCES nodes(id),
    action        TEXT    NOT NULL,
    reason        TEXT    NOT NULL DEFAULT '',
    ts            INTEGER NOT NULL,
    admin_subject TEXT    NOT NULL DEFAULT 'system'
) STRICT;

CREATE INDEX IF NOT EXISTS idx_admin_actions_node_ts ON admin_actions(node_id, ts DESC);

CREATE TABLE IF NOT EXISTS nonces_seen (
    wallet     TEXT    NOT NULL,
    nonce      TEXT    NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (wallet, nonce)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_nonces_expires ON nonces_seen(expires_at);

-- snapshots stores one row per published reward cycle (ADR-0008). cycle_id is
-- an opaque caller-supplied string ("cycle-N", or a date stamp, or whatever the
-- snapshot job uses); root and total_amount are hex-encoded so amounts beyond
-- 2^63 (BNB wei is 1e18 per token, easily exceeds int64) round-trip cleanly.
-- Snapshots are append-only — never UPDATE a published cycle.
CREATE TABLE IF NOT EXISTS snapshots (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    cycle_id     TEXT    NOT NULL UNIQUE,
    root         TEXT    NOT NULL,
    total_amount TEXT    NOT NULL DEFAULT '0x0',
    node_count   INTEGER NOT NULL DEFAULT 0,
    published_at INTEGER NOT NULL,
    ipfs_cid     TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX IF NOT EXISTS idx_snapshots_published_at ON snapshots(published_at DESC);

-- snapshot_proofs holds the (wallet, amount, proof) triples for a cycle. amount
-- is hex-encoded big.Int; proof_json is a JSON array of hex strings, each one
-- a sibling hash on the path from leaf to root. Wallet addresses are stored
-- lower-cased so lookups match regardless of how the caller cased the param.
CREATE TABLE IF NOT EXISTS snapshot_proofs (
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    wallet      TEXT    NOT NULL,
    amount      TEXT    NOT NULL,
    proof_json  TEXT    NOT NULL,
    PRIMARY KEY (snapshot_id, wallet)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_snapshot_proofs_wallet ON snapshot_proofs(wallet);
