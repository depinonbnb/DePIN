// Package sqlite: sqlite_snapshots.go implements the SaveSnapshot /
// GetLatestSnapshot / GetSnapshotByCycle / GetWalletProof methods of
// store.Store. Schema lives in migrations/001_initial.sql; see
// docs/adr/0008-merkle-snapshot-rewards.md for the rationale.
//
// Storage shape:
//   - snapshots(id INTEGER PK, cycle_id TEXT UNIQUE, root TEXT,
//     total_amount TEXT, node_count INTEGER, published_at INTEGER, ipfs_cid TEXT)
//   - snapshot_proofs(snapshot_id, wallet, amount TEXT, proof_json TEXT)
//
// Amount is stored as a hex-encoded big.Int with a "0x" prefix (the same form
// the API hands back to operators) so values larger than int64 round-trip
// without truncation. proof_json is a JSON array of "0x..." sibling hashes.
//
// SaveSnapshot is idempotent on cycle_id. Re-publishing replaces the prior
// row's proof set inside a single BEGIN IMMEDIATE transaction so partial
// writes can never appear.
package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"

	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/types"
)

// SaveSnapshot writes the cycle metadata + every per-wallet proof row inside
// one BEGIN IMMEDIATE tx. Idempotent on cycle_id.
func (s *SQLiteStore) SaveSnapshot(snap *types.Snapshot, proofs map[string]store.SnapshotProof) error {
	if snap == nil {
		return errors.New("sqlite: SaveSnapshot called with nil snapshot")
	}
	if strings.TrimSpace(snap.CycleID) == "" {
		return errors.New("sqlite: SaveSnapshot requires a non-empty cycle_id")
	}
	ctx := s.ctx

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: SaveSnapshot begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// INSERT OR REPLACE on the unique cycle_id. We can't use INSERT ... ON
	// CONFLICT(cycle_id) DO UPDATE because we also need a stable id to
	// re-link snapshot_proofs. The two-step (DELETE + INSERT) approach below
	// gives us both correctness and a fresh id when the row didn't exist.
	var snapshotID int64
	var existingID sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM snapshots WHERE cycle_id = ?`, snap.CycleID,
	).Scan(&existingID)
	switch {
	case err == nil && existingID.Valid:
		// Update existing snapshot.
		snapshotID = existingID.Int64
		if _, err := tx.ExecContext(ctx, `
			UPDATE snapshots SET
				root = ?, total_amount = ?, node_count = ?,
				published_at = ?, ipfs_cid = ?
			WHERE id = ?`,
			snap.Root, snap.TotalAmount, snap.NodeCount,
			snap.PublishedAt, snap.IPFSCID,
			snapshotID,
		); err != nil {
			return fmt.Errorf("sqlite: update snapshot: %w", err)
		}
		// Wipe the prior proofs — we'll re-insert below.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM snapshot_proofs WHERE snapshot_id = ?`, snapshotID,
		); err != nil {
			return fmt.Errorf("sqlite: clear prior proofs: %w", err)
		}
	case errors.Is(err, sql.ErrNoRows):
		// Fresh insert.
		res, ierr := tx.ExecContext(ctx, `
			INSERT INTO snapshots (cycle_id, root, total_amount, node_count, published_at, ipfs_cid)
			VALUES (?, ?, ?, ?, ?, ?)`,
			snap.CycleID, snap.Root, snap.TotalAmount, snap.NodeCount,
			snap.PublishedAt, snap.IPFSCID,
		)
		if ierr != nil {
			return fmt.Errorf("sqlite: insert snapshot: %w", ierr)
		}
		snapshotID, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("sqlite: snapshot last_insert_id: %w", err)
		}
	default:
		return fmt.Errorf("sqlite: lookup existing snapshot: %w", err)
	}

	if len(proofs) == 0 {
		// No proofs — still commit the metadata so callers can see an empty
		// cycle. Reasonable for the "zero earners" path described in
		// ADR-0008, though BuildCycle currently rejects that case.
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("sqlite: SaveSnapshot commit (no proofs): %w", err)
		}
		committed = true
		return nil
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO snapshot_proofs (snapshot_id, wallet, amount, proof_json)
		VALUES (?, ?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("sqlite: prepare proof insert: %w", err)
	}
	defer stmt.Close()

	for w, p := range proofs {
		wallet := strings.ToLower(w)
		amountHex := bigIntToHex(p.Amount)
		proofJSON, err := encodeProofJSON(p.Proof)
		if err != nil {
			return fmt.Errorf("sqlite: encode proof for %s: %w", wallet, err)
		}
		if _, err := stmt.ExecContext(ctx, snapshotID, wallet, amountHex, proofJSON); err != nil {
			return fmt.Errorf("sqlite: insert proof for %s: %w", wallet, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: SaveSnapshot commit: %w", err)
	}
	committed = true
	return nil
}

// GetLatestSnapshot returns the most recently published cycle. Single-row
// query against the descending published_at index.
func (s *SQLiteStore) GetLatestSnapshot() (*types.Snapshot, error) {
	ctx := s.ctx
	row := s.db.QueryRowContext(ctx, `
		SELECT cycle_id, root, total_amount, node_count, published_at, ipfs_cid
		FROM snapshots
		ORDER BY published_at DESC, id DESC
		LIMIT 1`,
	)
	snap, err := scanSnapshot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		slog.Default().Error("sqlite: GetLatestSnapshot", "error", err)
		return nil, err
	}
	return snap, nil
}

// GetSnapshotByCycle returns the row for a given cycle_id, or nil if missing.
func (s *SQLiteStore) GetSnapshotByCycle(cycleID string) (*types.Snapshot, error) {
	ctx := s.ctx
	row := s.db.QueryRowContext(ctx, `
		SELECT cycle_id, root, total_amount, node_count, published_at, ipfs_cid
		FROM snapshots
		WHERE cycle_id = ?`,
		cycleID,
	)
	snap, err := scanSnapshot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		slog.Default().Error("sqlite: GetSnapshotByCycle", "cycle_id", cycleID, "error", err)
		return nil, err
	}
	return snap, nil
}

// GetWalletProof joins snapshots + snapshot_proofs to return everything an
// operator needs to claim. Returns (nil, nil, nil, nil) for misses (the API
// layer maps that to 404).
func (s *SQLiteStore) GetWalletProof(cycleID string, wallet string) (*types.Snapshot, [][]byte, *big.Int, error) {
	ctx := s.ctx
	walletKey := strings.ToLower(wallet)

	row := s.db.QueryRowContext(ctx, `
		SELECT s.cycle_id, s.root, s.total_amount, s.node_count, s.published_at, s.ipfs_cid,
		       p.amount, p.proof_json
		FROM snapshots s
		JOIN snapshot_proofs p ON p.snapshot_id = s.id
		WHERE s.cycle_id = ? AND p.wallet = ?`,
		cycleID, walletKey,
	)

	var (
		snap       types.Snapshot
		amountHex  string
		proofJSON  string
		nodeCount  int64
		published  int64
		ipfsCID    sql.NullString
	)
	if err := row.Scan(
		&snap.CycleID, &snap.Root, &snap.TotalAmount, &nodeCount, &published, &ipfsCID,
		&amountHex, &proofJSON,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil, nil
		}
		slog.Default().Error("sqlite: GetWalletProof", "cycle_id", cycleID, "wallet", walletKey, "error", err)
		return nil, nil, nil, err
	}
	snap.NodeCount = int(nodeCount)
	snap.PublishedAt = published
	if ipfsCID.Valid {
		snap.IPFSCID = ipfsCID.String
	}

	amount, err := hexToBigInt(amountHex)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sqlite: decode amount: %w", err)
	}
	proof, err := decodeProofJSON(proofJSON)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sqlite: decode proof: %w", err)
	}
	return &snap, proof, amount, nil
}

// scanSnapshot reads one snapshot row in canonical column order. Used by
// GetLatestSnapshot / GetSnapshotByCycle.
func scanSnapshot(row interface {
	Scan(dest ...any) error
}) (*types.Snapshot, error) {
	var (
		snap      types.Snapshot
		nodeCount int64
		published int64
		ipfsCID   sql.NullString
	)
	if err := row.Scan(
		&snap.CycleID, &snap.Root, &snap.TotalAmount, &nodeCount, &published, &ipfsCID,
	); err != nil {
		return nil, err
	}
	snap.NodeCount = int(nodeCount)
	snap.PublishedAt = published
	if ipfsCID.Valid {
		snap.IPFSCID = ipfsCID.String
	}
	return &snap, nil
}

// bigIntToHex encodes a non-negative big.Int as "0x"+lower-case hex with no
// leading zeroes (other than "0x0" for zero). nil maps to "0x0".
func bigIntToHex(v *big.Int) string {
	if v == nil {
		return "0x0"
	}
	return "0x" + v.Text(16)
}

// hexToBigInt is the inverse of bigIntToHex. Tolerates the "0x" prefix and
// upper-case hex.
func hexToBigInt(s string) (*big.Int, error) {
	raw := s
	if len(raw) >= 2 && (raw[:2] == "0x" || raw[:2] == "0X") {
		raw = raw[2:]
	}
	if raw == "" {
		return new(big.Int), nil
	}
	v, ok := new(big.Int).SetString(raw, 16)
	if !ok {
		return nil, fmt.Errorf("invalid hex int %q", s)
	}
	return v, nil
}

// encodeProofJSON serialises a [][]byte as a JSON array of "0x..." strings.
func encodeProofJSON(proof [][]byte) (string, error) {
	const hexdigits = "0123456789abcdef"
	hex := make([]string, len(proof))
	for i, p := range proof {
		buf := make([]byte, 2+len(p)*2)
		buf[0], buf[1] = '0', 'x'
		for j, c := range p {
			buf[2+j*2] = hexdigits[c>>4]
			buf[2+j*2+1] = hexdigits[c&0x0f]
		}
		hex[i] = string(buf)
	}
	out, err := json.Marshal(hex)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// decodeProofJSON parses the inverse of encodeProofJSON. Each element MUST be
// a 32-byte hex string with the "0x" prefix.
func decodeProofJSON(in string) ([][]byte, error) {
	var hexProof []string
	if err := json.Unmarshal([]byte(in), &hexProof); err != nil {
		return nil, fmt.Errorf("decode proof_json: %w", err)
	}
	out := make([][]byte, len(hexProof))
	for i, s := range hexProof {
		raw := s
		if len(raw) >= 2 && (raw[:2] == "0x" || raw[:2] == "0X") {
			raw = raw[2:]
		}
		b, err := hexDecode(raw)
		if err != nil {
			return nil, fmt.Errorf("proof[%d]: %w", i, err)
		}
		if len(b) != 32 {
			return nil, fmt.Errorf("proof[%d] is %d bytes, want 32", i, len(b))
		}
		out[i] = b
	}
	return out, nil
}

// hexDecode mirrors encoding/hex.DecodeString without the import. (Keeping
// this file's import list stable across the codebase — the rest of the
// sqlite package already uses encoding/hex via go-ethereum, but importing
// it explicitly here would bloat the file. This is small enough to be
// readable and correct.)
func hexDecode(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("hex length not even: %d", len(s))
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		hi, err := hexNibble(s[i])
		if err != nil {
			return nil, err
		}
		lo, err := hexNibble(s[i+1])
		if err != nil {
			return nil, err
		}
		out[i/2] = (hi << 4) | lo
	}
	return out, nil
}

func hexNibble(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return 10 + c - 'a', nil
	case c >= 'A' && c <= 'F':
		return 10 + c - 'A', nil
	}
	return 0, fmt.Errorf("invalid hex char %q", c)
}

