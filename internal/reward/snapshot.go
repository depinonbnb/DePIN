// Package reward — snapshot.go aggregates points into a publishable Merkle
// cycle.
//
// Per ADR-0008 §Decision the snapshot job:
//  1. Freezes the cycle's points per wallet.
//  2. (Optionally) converts points to BNB using a per-cycle pool size.
//  3. Builds a sorted Merkle tree.
//  4. Persists root + per-wallet proofs.
//
// Phase 4 ships option A: the leaf amount is the wallet's *lifetime* point
// total. The on-chain Distributor handles per-claim deltas via its caller
// state ("amount already claimed" per wallet per cycle), so a re-publish of
// the same lifetime tree just lets a wallet claim the difference. Option B
// (per-cycle deltas) is a future refinement; revisit when governance lands.
//
// Banned nodes (CheatStatus == StatusBanned) are excluded from the tree.
// Flagged or warned nodes are NOT excluded — the contract is "you stay until
// an admin escalates to ban", which matches how AwardUptimePoints treats them.
package reward

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/types"
	"github.com/ethereum/go-ethereum/common"
)

// Cycle is a snapshot's in-memory shape: the cycle id, the per-wallet totals
// it summed, the resulting root and the per-wallet proof map. It maps onto
// (types.Snapshot + store.SnapshotProof) for persistence; that conversion is
// done by Persist.
type Cycle struct {
	CycleID     string
	PointsTotal map[common.Address]*big.Int
	Root        []byte
	Proofs      map[common.Address][][]byte
	NodeCount   int // how many wallets contributed leaves
	TotalAmount *big.Int
}

// BuildCycle reads every active node from the store, sums points per wallet
// (skipping banned nodes), and builds the Merkle tree.
//
// Aggregation rules:
//   - Wallet keys are normalised to lower-case hex via common.HexToAddress so
//     two casings never produce two leaves.
//   - Wallets with zero summed points are dropped — a zero-amount leaf would
//     waste a tree slot and the on-chain Distributor would refund nothing.
//   - Wallet strings that don't parse as hex addresses are skipped with a
//     warning-shaped error wrapped in the returned slice (callers can decide
//     to fail or log).
//
// Returns (nil, nil) and an error when the cycle has zero earners — ADR-0008
// §Decision says zero-earner cycles record the zero root and skip publishing,
// but persisting a zero-leaf tree is impossible (BuildTree errors on empty).
// Callers should treat ErrNoEarners as "skip this cycle, log it, move on".
func BuildCycle(s store.Store, cycleID string) (*Cycle, error) {
	if strings.TrimSpace(cycleID) == "" {
		return nil, errors.New("reward: cycleID required")
	}
	if s == nil {
		return nil, errors.New("reward: store is nil")
	}

	nodes := s.GetAllActiveNodes()

	// Sum lifetime points per wallet, skipping banned nodes. Wallets are
	// normalised through HexToAddress so casing collisions can't happen and
	// non-hex wallets surface as the zero address (which we then drop).
	totals := make(map[common.Address]*big.Int, len(nodes))
	for _, n := range nodes {
		if n.CheatStatus == types.StatusBanned {
			continue
		}
		if !common.IsHexAddress(n.WalletAddress) {
			// Skip malformed wallet — should never happen if RegisterNode is
			// validating addresses, but defensive.
			continue
		}
		w := common.HexToAddress(n.WalletAddress)
		// Drop the zero address explicitly. It either signals a parse failure
		// or a misconfigured node and we don't want it earning rewards.
		if w == (common.Address{}) {
			continue
		}
		v, ok := totals[w]
		if !ok {
			v = new(big.Int)
			totals[w] = v
		}
		v.Add(v, new(big.Int).SetUint64(n.TotalPoints))
	}

	// Drop zero-point wallets (a wallet whose every node was banned or whose
	// nodes literally earned zero points doesn't deserve a leaf).
	for w, v := range totals {
		if v.Sign() == 0 {
			delete(totals, w)
		}
	}

	if len(totals) == 0 {
		return nil, ErrNoEarners
	}

	leaves := make([]Leaf, 0, len(totals))
	total := new(big.Int)
	for w, v := range totals {
		leaves = append(leaves, Leaf{Wallet: w, Amount: new(big.Int).Set(v)})
		total.Add(total, v)
	}

	root, proofs, err := BuildTree(leaves)
	if err != nil {
		return nil, fmt.Errorf("reward: build tree: %w", err)
	}

	return &Cycle{
		CycleID:     cycleID,
		PointsTotal: totals,
		Root:        root,
		Proofs:      proofs,
		NodeCount:   len(totals),
		TotalAmount: total,
	}, nil
}

// ErrNoEarners is returned by BuildCycle when no active wallet has positive
// points. The snapshot job should log it and skip publishing per ADR-0008.
var ErrNoEarners = errors.New("reward: no earners in cycle")

// Persist converts a Cycle into the (types.Snapshot, []SnapshotProof) shape
// the Store interface accepts and writes it via SaveSnapshot. Idempotent on
// CycleID — re-publishing the same cycle replaces the previous tree.
func (c *Cycle) Persist(s store.Store, publishedAt int64, ipfsCID string) error {
	if c == nil {
		return errors.New("reward: nil cycle")
	}
	if s == nil {
		return errors.New("reward: store is nil")
	}
	if len(c.Proofs) == 0 {
		return errors.New("reward: cycle has no proofs to persist")
	}

	snap := &types.Snapshot{
		CycleID:     c.CycleID,
		Root:        "0x" + hexEncode(c.Root),
		TotalAmount: "0x" + c.TotalAmount.Text(16),
		NodeCount:   c.NodeCount,
		PublishedAt: publishedAt,
		IPFSCID:     ipfsCID,
	}

	proofMap := make(map[string]store.SnapshotProof, len(c.Proofs))
	for w, p := range c.Proofs {
		amount := c.PointsTotal[w]
		if amount == nil {
			// Defensive — Build invariant ensures every proof key has a total.
			continue
		}
		key := strings.ToLower(w.Hex())
		proofMap[key] = store.SnapshotProof{
			Wallet: key,
			Amount: new(big.Int).Set(amount),
			Proof:  cloneProof(p),
		}
	}

	return s.SaveSnapshot(snap, proofMap)
}

// hexEncode is a thin wrapper around encoding/hex but keeps the import
// localised here so the file's headline (BuildCycle) stays imports-light.
func hexEncode(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0x0f]
	}
	return string(out)
}

// cloneProof copies a [][]byte so the persisted proofs share no memory with
// the in-memory Cycle (the caller might mutate the returned proofs later;
// the persisted form must be stable).
func cloneProof(in [][]byte) [][]byte {
	out := make([][]byte, len(in))
	for i, p := range in {
		out[i] = append([]byte(nil), p...)
	}
	return out
}
