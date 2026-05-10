// Package reward implements the Phase 4 Merkle snapshot rewards described in
// docs/adr/0008-merkle-snapshot-rewards.md.
//
// Off-chain backend computes who-earned-what, builds a Merkle tree of
// (wallet, amount) leaves, publishes the root, and hands operators a
// (cycle, amount, proof) tuple they submit to a Solidity Distributor on chain.
// The Distributor uses OpenZeppelin's MerkleProof.verify, which expects a
// *sorted-pair* tree where leaves are keccak256(abi.encodePacked(addr, uint))
// — concatenated bytes, NOT abi.encode (which would pad each arg to 32 bytes).
//
// merkle.go is the encoding + tree builder. snapshot.go is the cycle-level
// aggregation that consumes a Store and produces a publishable Cycle.
//
// Critical invariants (mismatching any of these silently produces unverifiable
// proofs — there is no on-chain error, claims simply revert):
//
//  1. Leaf encoding MUST be keccak256(abi.encodePacked(address, uint256)).
//     20 bytes (raw address) || 32 bytes (big-endian, zero-padded uint256).
//  2. Leaves MUST be sorted ascending by their address bytes (NOT lexicographic
//     hex strings).
//  3. At every internal node, hashPair(a, b) MUST sort the pair by byte order
//     before concatenating, so verifying with OpenZeppelin's
//     MerkleProof.verify(proof, root, leaf) — which sorts each step — agrees.
package reward

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Leaf is one (address, amount) pair before encoding. Amount must be
// non-negative and fit in 256 bits — Ethereum uint256.
type Leaf struct {
	Wallet common.Address
	Amount *big.Int
}

// EncodeLeaf returns keccak256(abi.encodePacked(address, uint256)).
//
// Layout: 20 bytes (raw address bytes) || 32 bytes (big-endian, zero-padded
// big.Int). This is the same byte sequence Solidity produces for
// abi.encodePacked(address, uint256), which is why the Distributor verifies
// proofs with the same encoding.
//
// Using abi.encode instead would pad address to 32 bytes (12-byte zero
// prefix) — a different leaf preimage and, therefore, an unverifiable proof.
func EncodeLeaf(l Leaf) []byte {
	buf := make([]byte, 0, 52)
	buf = append(buf, l.Wallet.Bytes()...) // 20 bytes
	buf = append(buf, padUint256(l.Amount)...)
	return crypto.Keccak256(buf)
}

// padUint256 returns the 32-byte big-endian representation of a non-negative
// big.Int. nil and negative inputs are treated as zero so the function is
// total — callers MUST validate inputs before constructing a real leaf.
func padUint256(n *big.Int) []byte {
	out := make([]byte, 32)
	if n == nil || n.Sign() < 0 {
		return out
	}
	b := n.Bytes()
	if len(b) > 32 {
		// Larger than uint256 — clip to the most-significant 32 bytes. In
		// practice we validate this in BuildTree; keep the helper safe.
		return b[len(b)-32:]
	}
	copy(out[32-len(b):], b)
	return out
}

// BuildTree constructs a sorted-leaf, sorted-pair Merkle tree over the input
// leaves and returns the root and a per-wallet proof map.
//
// Returns:
//   - root: 32-byte keccak256 root.
//   - proofs[wallet]: the sibling-hash path from that wallet's leaf to the
//     root. For a single-leaf tree the proof is empty (the leaf hash IS the
//     root). Each entry is 32 bytes; on-chain verification folds them via
//     hashPair(running, sibling) until equal to root.
//   - error: non-nil when the input is empty or contains an invalid amount
//     (negative, or exceeds uint256).
//
// Determinism: leaves are sorted ascending by raw address bytes (NOT hex
// strings) before tree construction, so re-running BuildTree on the same
// input produces the same root and the same proofs.
//
// Duplicate wallets: returning two leaves for the same wallet is a caller
// bug — BuildTree errors out. The cycle aggregator in snapshot.go enforces
// SUM(points) per wallet at the source, so this shouldn't happen in practice.
func BuildTree(leaves []Leaf) (root []byte, proofs map[common.Address][][]byte, err error) {
	if len(leaves) == 0 {
		return nil, nil, errors.New("reward: BuildTree requires at least one leaf")
	}

	// Validate amounts and reject negatives / overflowing uint256.
	for i, l := range leaves {
		if l.Amount == nil {
			return nil, nil, fmt.Errorf("reward: leaf %d has nil amount", i)
		}
		if l.Amount.Sign() < 0 {
			return nil, nil, fmt.Errorf("reward: leaf %d has negative amount %s", i, l.Amount)
		}
		// 2^256 overflow check: more than 32 bytes of magnitude.
		if len(l.Amount.Bytes()) > 32 {
			return nil, nil, fmt.Errorf("reward: leaf %d amount exceeds uint256", i)
		}
	}

	// Copy + sort by address bytes ascending (NOT hex string). Sorting in place
	// would mutate the caller's slice, which is surprising for a pure
	// "BuildTree" helper.
	sorted := make([]Leaf, len(leaves))
	copy(sorted, leaves)
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i].Wallet.Bytes(), sorted[j].Wallet.Bytes()) < 0
	})

	// Reject duplicate wallets — a SUM at the call site would have collapsed
	// them; if we see two leaves for the same address it indicates a caller
	// bug rather than a recoverable condition.
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Wallet == sorted[i-1].Wallet {
			return nil, nil, fmt.Errorf("reward: duplicate wallet %s in leaves", sorted[i].Wallet.Hex())
		}
	}

	// Compute the leaf hashes once. The proofs map points back to the original
	// (unsorted) wallets, so we record the leaf hashes by index in `sorted`
	// alongside the wallet -> sorted-index map.
	leafHashes := make([][]byte, len(sorted))
	walletIndex := make(map[common.Address]int, len(sorted))
	for i, l := range sorted {
		leafHashes[i] = EncodeLeaf(l)
		walletIndex[l.Wallet] = i
	}

	// Build the tree level by level, recording each level so we can later
	// extract per-leaf proofs. Each level is a slice of 32-byte hashes; the
	// last level (length 1) is the root.
	levels := [][][]byte{leafHashes}
	for {
		current := levels[len(levels)-1]
		if len(current) == 1 {
			break
		}
		next := make([][]byte, 0, (len(current)+1)/2)
		for i := 0; i < len(current); i += 2 {
			if i+1 == len(current) {
				// Odd node carries up unpaired (OpenZeppelin convention).
				next = append(next, current[i])
				continue
			}
			next = append(next, hashPair(current[i], current[i+1]))
		}
		levels = append(levels, next)
	}

	root = levels[len(levels)-1][0]

	// Extract proofs. For each leaf, walk the levels collecting siblings.
	proofs = make(map[common.Address][][]byte, len(sorted))
	for wallet, idx := range walletIndex {
		path := make([][]byte, 0, len(levels))
		i := idx
		for l := 0; l < len(levels)-1; l++ {
			level := levels[l]
			var siblingIdx int
			if i%2 == 0 {
				siblingIdx = i + 1
			} else {
				siblingIdx = i - 1
			}
			if siblingIdx < len(level) {
				path = append(path, append([]byte(nil), level[siblingIdx]...))
			}
			// else: this node was the lone-odd carry-up — no sibling at this
			// level, advance to the next.
			i /= 2
		}
		proofs[wallet] = path
	}

	return root, proofs, nil
}

// VerifyProof reproduces the on-chain check performed by OpenZeppelin's
// MerkleProof.verify: starting from EncodeLeaf, fold sibling hashes via
// hashPair (which sorts each pair) and confirm the final hash equals the
// stored root.
//
// Returns false on length / content mismatches. Non-erroring — errors are
// rejection signals, mirroring the on-chain semantics.
func VerifyProof(leaf Leaf, proof [][]byte, root []byte) bool {
	if len(root) != 32 {
		return false
	}
	current := EncodeLeaf(leaf)
	for _, sibling := range proof {
		if len(sibling) != 32 {
			return false
		}
		current = hashPair(current, sibling)
	}
	return bytes.Equal(current, root)
}

// hashPair returns keccak256(min(a,b) || max(a,b)). The byte-order sort makes
// the tree commutative at every node, which is what OpenZeppelin's standard
// MerkleProof.verify expects: it doesn't carry left/right bits with the proof.
func hashPair(a, b []byte) []byte {
	if bytes.Compare(a, b) < 0 {
		return crypto.Keccak256(a, b)
	}
	return crypto.Keccak256(b, a)
}

// HexProof renders a proof as a JSON-friendly array of "0x..."-prefixed
// hex strings. Used by the API layer to serialise per-wallet claim payloads.
func HexProof(proof [][]byte) []string {
	out := make([]string, len(proof))
	for i, p := range proof {
		out[i] = "0x" + hex.EncodeToString(p)
	}
	return out
}

// DecodeHexProof reverses HexProof. It tolerates the "0x" prefix and rejects
// any element that isn't 32 bytes when decoded — the on-chain code requires
// 32-byte siblings, so this is the right place to fail loud.
func DecodeHexProof(proof []string) ([][]byte, error) {
	out := make([][]byte, len(proof))
	for i, s := range proof {
		raw := s
		if len(raw) >= 2 && (raw[:2] == "0x" || raw[:2] == "0X") {
			raw = raw[2:]
		}
		b, err := hex.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("reward: decode proof[%d]: %w", i, err)
		}
		if len(b) != 32 {
			return nil, fmt.Errorf("reward: proof[%d] is %d bytes, want 32", i, len(b))
		}
		out[i] = b
	}
	return out, nil
}
