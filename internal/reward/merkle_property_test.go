// Package reward: merkle_property_test.go uses testing/quick to drive the
// Merkle tree builder over randomly generated leaf slices.
//
// The deterministic table tests in merkle_test.go pin specific tree shapes
// (1, 2, 3, 5, 7, 10, 16 leaves). This file complements them with random
// shapes / amounts: any combination of inputs that BuildTree accepts must
// produce proofs that round-trip through VerifyProof, and perturbing any
// amount must invalidate that wallet's proof.
//
// Reproducibility: the random source is seeded with a fixed integer (1) so
// every run picks the same leaf slices. testing/quick.Config.Rand carries
// the seed through every Generate call.
package reward

import (
	"bytes"
	"math/big"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"

	"github.com/ethereum/go-ethereum/common"
)

// quickConfig builds the same testing/quick configuration every property
// test uses: 50 cases per check, fixed RNG seed. Each property re-derives a
// fresh *rand.Rand so iteration order doesn't carry between properties.
func quickConfig() *quick.Config {
	return &quick.Config{
		MaxCount: 50,
		Rand:     rand.New(rand.NewSource(1)),
	}
}

// leafSlice is a wrapper around []Leaf with a custom Generate so we control
// the number of leaves (1..32) and ensure addresses are unique. Without
// uniqueness BuildTree would error out on duplicates and the property
// would degenerate.
type leafSlice []Leaf

// Generate satisfies testing/quick.Generator. It builds 1..32 leaves with
// addresses spread across the byte space and amounts drawn from
// [1, 2^200) so we exercise the multi-byte big-endian padding path.
func (leafSlice) Generate(rnd *rand.Rand, _ int) reflect.Value {
	n := rnd.Intn(32) + 1 // [1, 32]
	used := make(map[common.Address]struct{}, n)
	out := make(leafSlice, 0, n)
	for len(out) < n {
		var a common.Address
		// Fill 20 bytes of pseudo-random address data.
		for i := range a {
			a[i] = byte(rnd.Intn(256))
		}
		if _, dup := used[a]; dup {
			continue
		}
		used[a] = struct{}{}

		// Random amount in [1, 2^200). 2^200 fits in 25 bytes — well under
		// uint256 — but big enough to land outside int64.
		bits := rnd.Intn(200) + 1
		amount := new(big.Int).Lsh(big.NewInt(1), uint(bits))
		amount.Sub(amount, big.NewInt(int64(rnd.Intn(1024)+1)))
		if amount.Sign() <= 0 {
			amount = big.NewInt(int64(rnd.Intn(1_000_000) + 1))
		}

		out = append(out, Leaf{Wallet: a, Amount: amount})
	}
	return reflect.ValueOf(out)
}

// TestBuildTree_PropertyAllProofsVerify asserts that every wallet's proof,
// produced by BuildTree, verifies against the same root via VerifyProof.
// This is the core Merkle invariant — if it ever fails on-chain claims
// would silently revert.
func TestBuildTree_PropertyAllProofsVerify(t *testing.T) {
	prop := func(in leafSlice) bool {
		root, proofs, err := BuildTree([]Leaf(in))
		if err != nil {
			return false
		}
		for _, l := range in {
			proof, ok := proofs[l.Wallet]
			if !ok {
				return false
			}
			if !VerifyProof(l, proof, root) {
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, quickConfig()); err != nil {
		t.Errorf("AllProofsVerify property failed: %v", err)
	}
}

// TestBuildTree_PropertyPerturbedAmountFails asserts that bumping any leaf's
// amount by 1 invalidates that leaf's proof. This is what protects the
// Distributor from "claim more than I earned" attacks: a working proof for
// (wallet, X) MUST NOT also work for (wallet, X+1).
func TestBuildTree_PropertyPerturbedAmountFails(t *testing.T) {
	prop := func(in leafSlice) bool {
		root, proofs, err := BuildTree([]Leaf(in))
		if err != nil {
			return false
		}
		// Pick the first leaf — every leaf must satisfy the invariant, so
		// "first" is enough for property-checking. (Iterating over all of
		// them on every quick.Check sample multiplies cost without adding
		// signal that wasn't already covered by AllProofsVerify above.)
		l := in[0]
		perturbed := Leaf{
			Wallet: l.Wallet,
			Amount: new(big.Int).Add(l.Amount, big.NewInt(1)),
		}
		// VerifyProof MUST reject the perturbed leaf. If it accepts, the
		// tree is broken and we return false to fail the property.
		return !VerifyProof(perturbed, proofs[l.Wallet], root)
	}
	if err := quick.Check(prop, quickConfig()); err != nil {
		t.Errorf("PerturbedAmountFails property failed: %v", err)
	}
}

// TestEncodeLeaf_PropertyShape asserts the EncodeLeaf output is always 32
// bytes regardless of address contents or amount magnitude.
func TestEncodeLeaf_PropertyShape(t *testing.T) {
	prop := func(seed int64, bits uint8) bool {
		// Build deterministic inputs from the quick-generated seed/bits.
		rnd := rand.New(rand.NewSource(seed))
		var a common.Address
		for i := range a {
			a[i] = byte(rnd.Intn(256))
		}
		b := uint(bits) % 256
		amount := new(big.Int).Lsh(big.NewInt(1), b)
		out := EncodeLeaf(Leaf{Wallet: a, Amount: amount})
		return len(out) == 32
	}
	if err := quick.Check(prop, &quick.Config{
		MaxCount: 200,
		Rand:     rand.New(rand.NewSource(1)),
	}); err != nil {
		t.Errorf("EncodeLeaf shape property failed: %v", err)
	}
}

// TestBuildTree_PropertyOrderInvariant asserts that shuffling the input
// leaves yields IDENTICAL root and proofs. BuildTree sorts internally; this
// is the property that guarantees re-running snapshot.go on the same data
// converges to the same on-chain root regardless of caller iteration order.
func TestBuildTree_PropertyOrderInvariant(t *testing.T) {
	prop := func(in leafSlice) bool {
		root1, proofs1, err := BuildTree([]Leaf(in))
		if err != nil {
			return false
		}

		// Shuffle a copy and rebuild.
		shuffled := make([]Leaf, len(in))
		copy(shuffled, in)
		rnd := rand.New(rand.NewSource(42))
		rnd.Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})

		root2, proofs2, err := BuildTree(shuffled)
		if err != nil {
			return false
		}

		if !bytes.Equal(root1, root2) {
			return false
		}
		if len(proofs1) != len(proofs2) {
			return false
		}
		for wallet, p1 := range proofs1 {
			p2, ok := proofs2[wallet]
			if !ok {
				return false
			}
			if len(p1) != len(p2) {
				return false
			}
			for i := range p1 {
				if !bytes.Equal(p1[i], p2[i]) {
					return false
				}
			}
		}
		return true
	}
	if err := quick.Check(prop, quickConfig()); err != nil {
		t.Errorf("OrderInvariant property failed: %v", err)
	}
}
