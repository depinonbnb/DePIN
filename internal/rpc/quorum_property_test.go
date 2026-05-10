// Package rpc: quorum_property_test.go is a small property suite over the
// QuorumClient majority logic and canonicalAnswer's hex case-insensitivity.
//
// These complement the table-driven tests in quorum_test.go: instead of
// hand-listing 1-of-1, 2-of-3, 3-of-3 etc., we sweep n in 1..7 and k in
// 0..n and check the strict-majority rule on every (n, k) pair.
package rpc

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/depinonbnb/depin/internal/types"
)

// This file reuses fakeRPCServer from quorum_test.go (same package). No
// new helper is needed — fakeRPCServer already serves a JSON-RPC response
// keyed on the requested method, and BlockHash challenges round-trip
// through the eth_getBlockByNumber branch.

// TestQuorumMajority_Property sweeps the (n, k) majority space:
//
//	for n in 1..7:
//	    for k in 0..n:
//	        spin n servers; k say "0xABC", n-k say "0xDEF"
//	        if k >= n/2+1: expect majority "0xABC"
//	        else:           expect QuorumNoMajority
//
// This catches off-by-one errors in the majority threshold (`floor(N/2)+1`)
// without relying on a hand-maintained truth table.
func TestQuorumMajority_Property(t *testing.T) {
	const winner = "0xABCABC"
	const loser = "0xDEFDEF"

	for n := 1; n <= 7; n++ {
		for k := 0; k <= n; k++ {
			// Build n servers: first k return `winner`, remaining n-k return
			// `loser`. The seed corpus deliberately uses the same answer
			// across the "winner" group so canonicalAnswer collapses them
			// into a single bucket; ditto for the loser group.
			urls := make([]string, 0, n)
			for i := 0; i < n; i++ {
				if i < k {
					urls = append(urls, fakeRPCServer(t, winner).URL)
				} else {
					urls = append(urls, fakeRPCServer(t, loser).URL)
				}
			}

			q := NewQuorum(urls)
			bn := uint64(1)
			ch := &types.Challenge{
				ID: "ch", NodeID: "node",
				ChallengeType: types.BlockHash,
				Params:        types.ChallengeParams{BlockNumber: &bn},
			}
			resp := q.ExecuteChallenge(ch)

			majority := n/2 + 1

			// The winner group passes iff k >= majority. Loser group passes
			// iff (n-k) >= majority. Both groups can't pass simultaneously
			// (one or the other always fails the strict-majority rule).
			switch {
			case k >= majority:
				if !resp.Success {
					t.Errorf("n=%d k=%d: expected majority winner, got error=%q", n, k, resp.Error)
				} else if !strings.EqualFold(resp.Data, winner) {
					t.Errorf("n=%d k=%d: expected winner %q, got %q", n, k, winner, resp.Data)
				}
			case (n-k) >= majority:
				if !resp.Success {
					t.Errorf("n=%d k=%d: expected majority loser, got error=%q", n, k, resp.Error)
				} else if !strings.EqualFold(resp.Data, loser) {
					t.Errorf("n=%d k=%d: expected loser %q, got %q", n, k, loser, resp.Data)
				}
			default:
				if resp.Success {
					t.Errorf("n=%d k=%d: expected no-majority, got success data=%q", n, k, resp.Data)
				} else if resp.Error != QuorumNoMajority {
					t.Errorf("n=%d k=%d: expected error %q, got %q", n, k, QuorumNoMajority, resp.Error)
				}
			}
		}
	}
}

// TestCanonicalAnswer_HexCaseInsensitivity asserts that for the BlockHash
// challenge type, canonicalAnswer collapses any case mix of the same
// underlying bytes onto the same canonical string. This is the property
// that lets two endpoints — one returning "0xDEADBEEF" and another
// returning "0xdeadbeef" — bucket together in the majority logic.
//
// We don't use testing/quick.Check directly because the input we want is
// a random byte slice (not a primitive int) — the loop here is the moral
// equivalent: 200 randomized samples, each tested for case insensitivity.
func TestCanonicalAnswer_HexCaseInsensitivity(t *testing.T) {
	for i := 0; i < 200; i++ {
		// Build a 32-byte slice from i so the corpus is deterministic.
		buf := make([]byte, 32)
		for j := range buf {
			buf[j] = byte((i*7 + j*13) & 0xff)
		}
		hexStr := hex.EncodeToString(buf)

		lower := "0x" + strings.ToLower(hexStr)
		upper := "0x" + strings.ToUpper(hexStr)
		mixed := "0x" + strings.ToUpper(hexStr[:8]) + strings.ToLower(hexStr[8:])

		l := canonicalAnswer(types.BlockHash, lower)
		u := canonicalAnswer(types.BlockHash, upper)
		m := canonicalAnswer(types.BlockHash, mixed)

		if l != u {
			t.Errorf("iter %d: lower vs upper mismatch\n lower: %q\n upper: %q", i, l, u)
		}
		if l != m {
			t.Errorf("iter %d: lower vs mixed mismatch\n lower: %q\n mixed: %q", i, l, m)
		}
	}
}
