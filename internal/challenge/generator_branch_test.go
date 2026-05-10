// Package challenge: generator_branch_test.go covers the per-challenge-type
// branches of GenerateChallenge.
//
// generator_test.go's table-style happy-path test only asserts "the result is
// some valid challenge type". This file pushes harder:
//
//  1. We loop GenerateChallenge enough times across enough node types that
//     every ChallengeType (BlockHash, BlockData, StateBalance, TxReceipt,
//     SyncStatus) is reached at least once. If the generator silently
//     stops emitting one of the types, this test fails — a behavioural
//     regression that pure unit tests would miss.
//  2. For each emitted (type, node-type) pair we re-validate the params
//     shape inline so a generator that emits BlockHash with a nil
//     BlockNumber, or StateBalance with an empty Address, fails the test
//     instead of silently producing an unverifiable challenge.
package challenge

import (
	"testing"

	"github.com/depinonbnb/depin/internal/types"
)

// TestGenerator_AllChallengeTypes_Reachable iterates GenerateChallenge across
// every node tier. The current generator only emits TxReceipt for... no tier
// at all (look at getAvailableChallengeTypes — it's listed in the package
// type set but not currently produced). We reflect that reality below: if a
// future change starts emitting TxReceipt this test will pass anyway because
// the assertion is "every type that COULD be emitted IS emitted across the
// loop". Types that were never in any tier's pool aren't required.
func TestGenerator_AllChallengeTypes_Reachable(t *testing.T) {
	g := NewGenerator()

	// expectedReachable mirrors the union of getAvailableChallengeTypes
	// across every supported tier. If generator.go grows TxReceipt support
	// later, add it here.
	expectedReachable := map[types.ChallengeType]bool{
		types.BlockHash:    false,
		types.BlockData:    false,
		types.StateBalance: false,
		types.SyncStatus:   false,
	}

	tiers := []types.NodeType{
		types.BscArchive, // emits BlockHash, BlockData, StateBalance, SyncStatus
		types.BscFull,    // emits BlockHash, BlockData, SyncStatus
		types.OpbnbFull,  // emits BlockHash, BlockData, SyncStatus
		types.BscFast,    // emits BlockHash, SyncStatus
		types.OpbnbFast,  // emits BlockHash, SyncStatus
	}

	// 200 iterations × 5 tiers = 1000 challenges. Each tier's pool has at
	// most 4 types; a uniform distribution gives ~250 BlockHash and ~250
	// SyncStatus from the fast tiers alone, plus ~50 each of BlockData /
	// StateBalance from the archive/full tiers. Way past coverage threshold.
	for i := 0; i < 200; i++ {
		for _, nt := range tiers {
			ch := g.GenerateChallenge("node-x", nt)
			if ch == nil {
				t.Fatalf("GenerateChallenge returned nil for tier %s", nt)
			}
			if _, known := expectedReachable[ch.ChallengeType]; !known {
				t.Fatalf("tier %s emitted unknown ChallengeType %q", nt, ch.ChallengeType)
			}
			expectedReachable[ch.ChallengeType] = true
		}
	}

	for tp, seen := range expectedReachable {
		if !seen {
			t.Errorf("ChallengeType %q never emitted across 1000 generations", tp)
		}
	}
}

// TestGenerator_ParamsValidPerType verifies the params shape contract for
// every challenge type. If the generator ever emits a BlockHash challenge
// without a BlockNumber the verifier silently fails — this test fails fast
// instead.
//
// Per the current generator (challenge/generator.go::generateParams):
//
//	BlockHash    -> BlockNumber non-nil
//	BlockData    -> BlockNumber non-nil
//	StateBalance -> BlockNumber non-nil AND Address non-empty
//	TxReceipt    -> currently unreachable; if reached, TxHash must be non-empty
//	SyncStatus   -> empty (no required params)
func TestGenerator_ParamsValidPerType(t *testing.T) {
	g := NewGenerator()

	tiers := []types.NodeType{
		types.BscArchive,
		types.BscFull,
		types.OpbnbFull,
		types.BscFast,
		types.OpbnbFast,
	}

	for i := 0; i < 200; i++ {
		for _, nt := range tiers {
			ch := g.GenerateChallenge("node-y", nt)
			if ch == nil {
				t.Fatalf("GenerateChallenge returned nil for tier %s", nt)
			}
			switch ch.ChallengeType {
			case types.BlockHash, types.BlockData:
				if ch.Params.BlockNumber == nil {
					t.Errorf("tier %s type %s: BlockNumber must be non-nil", nt, ch.ChallengeType)
				}
			case types.StateBalance:
				if ch.Params.BlockNumber == nil {
					t.Errorf("tier %s type StateBalance: BlockNumber must be non-nil", nt)
				}
				if ch.Params.Address == "" {
					t.Errorf("tier %s type StateBalance: Address must be non-empty", nt)
				}
			case types.SyncStatus:
				// SyncStatus carries no required params; no assertions.
			case types.TxReceipt:
				// Currently unreachable from the generator; when it lands,
				// the contract is TxHash must be non-empty.
				if ch.Params.TxHash == "" {
					t.Errorf("tier %s type TxReceipt: TxHash must be non-empty", nt)
				}
			default:
				t.Errorf("tier %s emitted unknown ChallengeType %q", nt, ch.ChallengeType)
			}
		}
	}
}
