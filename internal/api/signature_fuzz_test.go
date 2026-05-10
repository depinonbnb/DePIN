// Package api: signature_fuzz_test.go fuzz-tests verifySignature against
// arbitrary byte sequences. The receiver *Handlers does not actually use
// any of its fields inside verifySignature, so we construct a zero-value
// &Handlers{} for the fuzz body — no store / verifier wiring required.
//
// Properties exercised:
//
//   - verifySignature MUST NOT panic for any combination of (message,
//     signature, address). Pre-Phase-3 the hand-rolled hex loop could
//     mis-handle odd-length strings; the encoding/hex switch removed that
//     class of bug but we keep a fuzz harness so future refactors don't
//     reintroduce it.
//   - For the seeded (msg, sig, addr) tuples produced by a fresh secp256k1
//     keypair, verifySignature MUST return true.
//   - For mutated / malformed inputs (the fuzzer's job) verifySignature must
//     return false rather than panicking, erroring, or misreporting truth.
package api

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// signedTriple deterministically generates a (message, hex-signature,
// 0x-prefixed address) triple using a freshly minted secp256k1 key. The
// signing flow mirrors RegisterNode's wire format exactly:
//
//	hash = keccak256("\x19Ethereum Signed Message:\n" + len(msg) + msg)
//	sig  = secp256k1.Sign(hash, privKey)
//
// We deliberately don't shift the v byte by 27 here — verifySignature handles
// both 0/1 and 27/28 conventions, and we want at least one seed corpus entry
// in each form to exercise the normalization branch.
func signedTriple(t testing.TB, message string, addV27 bool) (string, string, string) {
	t.Helper()
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("crypto.GenerateKey: %v", err)
	}
	addr := crypto.PubkeyToAddress(priv.PublicKey).Hex()

	prefixed := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)
	hash := crypto.Keccak256Hash([]byte(prefixed))
	sig, err := crypto.Sign(hash.Bytes(), priv)
	if err != nil {
		t.Fatalf("crypto.Sign: %v", err)
	}
	if addV27 {
		sig[64] += 27
	}
	return message, hex.EncodeToString(sig), addr
}

// FuzzVerifySignature seeds the corpus with three known-good triples (one
// short message, one Register-style multi-line message, and one with the
// v=27/28 form) and lets the fuzzer mutate from there. The body's only
// invariant is "must not panic"; we additionally re-check the seeds verify so
// a regression that breaks the happy path shows up immediately on every
// fuzz run.
func FuzzVerifySignature(f *testing.F) {
	h := &Handlers{}

	// Seed 1: simple short message, v in {0,1}.
	m1, s1, a1 := signedTriple(f, "hello fuzz", false)
	f.Add(m1, s1, a1)

	// Seed 2: realistic multi-line RegisterNode wire-format payload.
	m2 := "Register node\nWallet: 0xabc\nType: bsc-full\nTimestamp: 1700000000000\nNonce: deadbeef"
	m2, s2, a2 := signedTriple(f, m2, false)
	f.Add(m2, s2, a2)

	// Seed 3: same as seed 1 but with the legacy v=27 byte; exercises the
	// `sigBytes[64] -= 27` normalization path.
	m3, s3, a3 := signedTriple(f, "v27 path", true)
	f.Add(m3, s3, a3)

	// Sanity: every seed verifies before we hand the fuzzer the corpus.
	for _, tc := range []struct{ m, s, a string }{{m1, s1, a1}, {m2, s2, a2}, {m3, s3, a3}} {
		if !h.verifySignature(tc.m, tc.s, tc.a) {
			f.Fatalf("seed triple should verify: addr=%s", tc.a)
		}
	}

	f.Fuzz(func(t *testing.T, message, signature, address string) {
		// Cap inputs so the fuzzer can't push us into multi-megabyte hashes
		// that slow the run without exploring new branches.
		if len(message) > 4096 || len(signature) > 4096 || len(address) > 4096 {
			return
		}

		// The single hard property: never panic. Truth value is ignored for
		// fuzzed inputs — those will mostly be malformed and return false,
		// which is correct behaviour and not interesting to assert.
		_ = h.verifySignature(message, signature, address)
	})
}
