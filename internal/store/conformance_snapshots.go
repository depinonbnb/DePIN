// Package store: conformance_snapshots.go covers the ADR-0008 snapshot
// methods (SaveSnapshot, GetLatestSnapshot, GetSnapshotByCycle,
// GetWalletProof). Both backends (memory + sqlite) run this same suite so
// they cannot drift — see internal/store/conformance.go for the entry point.
package store

import (
	"bytes"
	"math/big"
	"strings"
	"testing"

	"github.com/depinonbnb/depin/internal/types"
)

// fixtureSnapshot returns a deterministic snapshot + proofs map suitable for
// round-trip tests. Two wallets, two distinct amounts, three-element proof
// path so we exercise the [][]byte serialisation more than trivially.
func fixtureSnapshot(cycleID string, publishedAt int64) (*types.Snapshot, map[string]SnapshotProof) {
	snap := &types.Snapshot{
		CycleID:     cycleID,
		Root:        "0x" + strings.Repeat("ab", 32),
		TotalAmount: "0x96", // 150
		NodeCount:   2,
		PublishedAt: publishedAt,
		IPFSCID:     "",
	}
	proofs := map[string]SnapshotProof{
		"0x1111111111111111111111111111111111111111": {
			Wallet: "0x1111111111111111111111111111111111111111",
			Amount: big.NewInt(50),
			Proof: [][]byte{
				bytes.Repeat([]byte{0x01}, 32),
				bytes.Repeat([]byte{0x02}, 32),
				bytes.Repeat([]byte{0x03}, 32),
			},
		},
		"0x2222222222222222222222222222222222222222": {
			Wallet: "0x2222222222222222222222222222222222222222",
			Amount: big.NewInt(100),
			Proof: [][]byte{
				bytes.Repeat([]byte{0x04}, 32),
			},
		},
	}
	return snap, proofs
}

// runSaveSnapshotRoundTrip writes a snapshot, reads back the metadata via
// GetSnapshotByCycle, and reads back a per-wallet proof via GetWalletProof.
func runSaveSnapshotRoundTrip(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	snap, proofs := fixtureSnapshot("cycle-rt", 1_700_000_000_000)
	if err := s.SaveSnapshot(snap, proofs); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	got, err := s.GetSnapshotByCycle("cycle-rt")
	if err != nil {
		t.Fatalf("GetSnapshotByCycle: %v", err)
	}
	if got == nil {
		t.Fatal("GetSnapshotByCycle returned nil for an existing cycle")
	}
	if got.CycleID != snap.CycleID {
		t.Errorf("CycleID mismatch: got %q want %q", got.CycleID, snap.CycleID)
	}
	if got.Root != snap.Root {
		t.Errorf("Root mismatch: got %q want %q", got.Root, snap.Root)
	}
	if got.NodeCount != snap.NodeCount {
		t.Errorf("NodeCount mismatch: got %d want %d", got.NodeCount, snap.NodeCount)
	}
	if got.PublishedAt != snap.PublishedAt {
		t.Errorf("PublishedAt mismatch: got %d want %d", got.PublishedAt, snap.PublishedAt)
	}
	if got.TotalAmount != snap.TotalAmount {
		t.Errorf("TotalAmount mismatch: got %q want %q", got.TotalAmount, snap.TotalAmount)
	}

	// Per-wallet proof round-trip — case-insensitive lookup.
	for _, w := range []string{
		"0x1111111111111111111111111111111111111111",
		"0X1111111111111111111111111111111111111111", // upper-case prefix
	} {
		snapBack, proof, amount, err := s.GetWalletProof("cycle-rt", w)
		if err != nil {
			t.Fatalf("GetWalletProof(%s): %v", w, err)
		}
		if snapBack == nil {
			t.Fatalf("GetWalletProof(%s) returned nil snapshot", w)
		}
		if amount == nil || amount.Int64() != 50 {
			t.Errorf("GetWalletProof(%s): amount got %v want 50", w, amount)
		}
		if len(proof) != 3 {
			t.Errorf("GetWalletProof(%s): proof len got %d want 3", w, len(proof))
		}
		// Spot-check first sibling matches the fixture.
		if !bytes.Equal(proof[0], bytes.Repeat([]byte{0x01}, 32)) {
			t.Errorf("GetWalletProof(%s): first sibling mismatch", w)
		}
	}

	// Unknown wallet -> nil tuple, no error.
	snapBack, proof, amount, err := s.GetWalletProof("cycle-rt", "0x9999999999999999999999999999999999999999")
	if err != nil {
		t.Fatalf("GetWalletProof unknown wallet returned error: %v", err)
	}
	if snapBack != nil || proof != nil || amount != nil {
		t.Errorf("expected nil tuple for unknown wallet, got snap=%v proof=%v amount=%v", snapBack, proof, amount)
	}
}

// runSaveSnapshotIdempotent re-publishes the same cycle_id with a different
// proof set and confirms the second write replaces the first.
func runSaveSnapshotIdempotent(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	first, firstProofs := fixtureSnapshot("cycle-idem", 1000)
	if err := s.SaveSnapshot(first, firstProofs); err != nil {
		t.Fatalf("first SaveSnapshot: %v", err)
	}

	// Republish with one fewer wallet and a different root timestamp.
	second := &types.Snapshot{
		CycleID:     "cycle-idem",
		Root:        "0x" + strings.Repeat("cd", 32),
		TotalAmount: "0x32", // 50
		NodeCount:   1,
		PublishedAt: 2000,
		IPFSCID:     "QmHash",
	}
	secondProofs := map[string]SnapshotProof{
		"0x1111111111111111111111111111111111111111": {
			Wallet: "0x1111111111111111111111111111111111111111",
			Amount: big.NewInt(50),
			Proof:  [][]byte{bytes.Repeat([]byte{0xee}, 32)},
		},
	}
	if err := s.SaveSnapshot(second, secondProofs); err != nil {
		t.Fatalf("second SaveSnapshot: %v", err)
	}

	got, err := s.GetSnapshotByCycle("cycle-idem")
	if err != nil {
		t.Fatalf("GetSnapshotByCycle: %v", err)
	}
	if got == nil {
		t.Fatal("missing snapshot after re-publish")
	}
	if got.Root != second.Root {
		t.Errorf("Root not updated: got %q want %q", got.Root, second.Root)
	}
	if got.PublishedAt != 2000 {
		t.Errorf("PublishedAt not updated: got %d", got.PublishedAt)
	}
	if got.IPFSCID != "QmHash" {
		t.Errorf("IPFSCID not updated: got %q", got.IPFSCID)
	}
	if got.NodeCount != 1 {
		t.Errorf("NodeCount not updated: got %d", got.NodeCount)
	}

	// The wallet that existed in first but not second MUST be gone.
	gone, _, _, err := s.GetWalletProof("cycle-idem", "0x2222222222222222222222222222222222222222")
	if err != nil {
		t.Fatalf("GetWalletProof: %v", err)
	}
	if gone != nil {
		t.Error("dropped wallet still present after re-publish")
	}
}

// runGetLatestSnapshot writes two snapshots with different published_at
// values and asserts GetLatestSnapshot picks the larger one.
func runGetLatestSnapshot(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	// No snapshots -> nil, nil.
	got, err := s.GetLatestSnapshot()
	if err != nil {
		t.Fatalf("GetLatestSnapshot empty: %v", err)
	}
	if got != nil {
		t.Errorf("GetLatestSnapshot empty: expected nil, got %+v", got)
	}

	older, oP := fixtureSnapshot("cycle-old", 1000)
	newer, nP := fixtureSnapshot("cycle-new", 2000)
	if err := s.SaveSnapshot(older, oP); err != nil {
		t.Fatalf("save old: %v", err)
	}
	if err := s.SaveSnapshot(newer, nP); err != nil {
		t.Fatalf("save new: %v", err)
	}

	latest, err := s.GetLatestSnapshot()
	if err != nil {
		t.Fatalf("GetLatestSnapshot: %v", err)
	}
	if latest == nil {
		t.Fatal("GetLatestSnapshot returned nil after writes")
	}
	if latest.CycleID != "cycle-new" {
		t.Errorf("expected cycle-new, got %q", latest.CycleID)
	}
}

// runGetWalletProofMissing covers the "cycle exists but wallet doesn't" and
// "cycle doesn't exist" cases. Both must return (nil, nil, nil, nil) without
// error.
func runGetWalletProofMissing(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	// Cycle doesn't exist.
	snap, proof, amount, err := s.GetWalletProof("ghost-cycle", "0x1111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("GetWalletProof on missing cycle returned error: %v", err)
	}
	if snap != nil || proof != nil || amount != nil {
		t.Errorf("expected nil tuple for missing cycle, got %v %v %v", snap, proof, amount)
	}

	// Cycle exists but wallet doesn't.
	fix, fixProofs := fixtureSnapshot("cycle-partial", 5000)
	if err := s.SaveSnapshot(fix, fixProofs); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	snap, proof, amount, err = s.GetWalletProof("cycle-partial", "0xnope-not-an-address-but-still-text")
	if err != nil {
		t.Fatalf("GetWalletProof unknown wallet returned error: %v", err)
	}
	if snap != nil || proof != nil || amount != nil {
		t.Errorf("expected nil tuple for unknown wallet, got %v %v %v", snap, proof, amount)
	}
}
