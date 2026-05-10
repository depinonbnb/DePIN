// Package memory: memory_snapshots.go implements the ADR-0008 snapshot
// methods of store.Store for the in-memory backend. The bodies live here so
// memory.go stays under the project's 500-LOC cap.
package memory

import (
	"math/big"
	"strings"

	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/types"
)

// snapshotEntry is the in-memory shape of one published cycle. proofs is
// keyed by lower-case wallet hex so reads work regardless of input casing.
type snapshotEntry struct {
	meta   types.Snapshot
	proofs map[string]store.SnapshotProof
}

// SaveSnapshot persists a published cycle. Idempotent on CycleID — replays
// just overwrite the prior entry, including its proof map. Wallets are
// normalised to lower-case so reads work regardless of input casing. We
// deep-copy proof bytes so the caller is free to mutate the input map after
// the call returns.
func (s *MemoryStore) SaveSnapshot(snap *types.Snapshot, proofs map[string]store.SnapshotProof) error {
	if snap == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	clonedProofs := make(map[string]store.SnapshotProof, len(proofs))
	for w, p := range proofs {
		key := strings.ToLower(w)
		amount := new(big.Int)
		if p.Amount != nil {
			amount.Set(p.Amount)
		}
		path := make([][]byte, len(p.Proof))
		for i, sib := range p.Proof {
			path[i] = append([]byte(nil), sib...)
		}
		clonedProofs[key] = store.SnapshotProof{
			Wallet: key,
			Amount: amount,
			Proof:  path,
		}
	}

	s.snapshots[snap.CycleID] = &snapshotEntry{
		meta:   *snap,
		proofs: clonedProofs,
	}
	return nil
}

// GetLatestSnapshot returns the most recently published cycle, or nil if no
// snapshots exist. Linear scan — fine for the in-memory backend's scale.
func (s *MemoryStore) GetLatestSnapshot() (*types.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.snapshots) == 0 {
		return nil, nil
	}
	var latest *snapshotEntry
	for _, e := range s.snapshots {
		if latest == nil || e.meta.PublishedAt > latest.meta.PublishedAt {
			latest = e
		}
	}
	if latest == nil {
		return nil, nil
	}
	out := latest.meta
	return &out, nil
}

// GetSnapshotByCycle returns the snapshot for a given cycle id, or nil if not
// found.
func (s *MemoryStore) GetSnapshotByCycle(cycleID string) (*types.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.snapshots[cycleID]
	if !ok {
		return nil, nil
	}
	out := e.meta
	return &out, nil
}

// GetWalletProof returns the (snapshot, proof, amount) tuple for a wallet in
// a cycle. Wallets are looked up in lower-case to match SaveSnapshot's
// normalisation. Returns (nil, nil, nil, nil) if the cycle exists but the
// wallet has no proof, or if the cycle does not exist.
func (s *MemoryStore) GetWalletProof(cycleID string, wallet string) (*types.Snapshot, [][]byte, *big.Int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.snapshots[cycleID]
	if !ok {
		return nil, nil, nil, nil
	}
	p, ok := e.proofs[strings.ToLower(wallet)]
	if !ok {
		return nil, nil, nil, nil
	}
	out := e.meta
	pathCopy := make([][]byte, len(p.Proof))
	for i, sib := range p.Proof {
		pathCopy[i] = append([]byte(nil), sib...)
	}
	amount := new(big.Int).Set(p.Amount)
	return &out, pathCopy, amount, nil
}
