// Package rpc: quorum_fuzz_test.go fuzz-tests canonicalJSON, the helper
// used by canonicalAnswer to bucket per-endpoint replies by sorted-key form.
//
// canonicalJSON is unexported; same-package _test.go can call it directly.
//
// Properties:
//
//  1. Never panics for any input (valid JSON, malformed, binary garbage).
//  2. Idempotent on valid JSON — canonicalJSON(canonicalJSON(x)) ==
//     canonicalJSON(x) when x parses.
//  3. Key-order invariance — for any JSON OBJECT input, canonicalJSON
//     produces the same output regardless of source key order. We exercise
//     this by parsing the input, re-marshalling the parsed value (which
//     gives Go's nondeterministic key order), and asserting the second
//     canonicalisation matches the first.
package rpc

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzCanonicalJSON seeds the corpus with the same shapes canonicalAnswer
// is expected to encounter in production (block-data JSON, sync-status
// scalar, transaction-receipt object, malformed garbage) and lets the
// fuzzer mutate from there.
func FuzzCanonicalJSON(f *testing.F) {
	seeds := []string{
		// Empty / scalar valid JSON.
		`{}`,
		`[]`,
		`null`,
		`123`,
		`"abc"`,
		`true`,

		// Single-key + multi-key objects (key-order invariance fodder).
		`{"a":1}`,
		`{"b":1,"a":2}`,
		`{"a":1,"b":2,"c":3}`,
		`{"hash":"0xdeadbeef","number":"0x1"}`,

		// Nested.
		`{"a":{"b":1,"a":2}}`,
		`[{"b":1,"a":2},{"a":3}]`,

		// Arrays.
		`[1,2,3]`,
		`["a","b"]`,

		// Unparseable.
		`{`,
		`}}`,
		`not json`,
		``,

		// Binary-ish garbage (encoded as a Go literal; the fuzzer will
		// generate truly random bytes from this seed).
		"\x00\xff\x01\xfe",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		// Cap inputs to keep the fuzz runtime bounded. canonicalJSON walks
		// the whole tree; very large inputs add coverage slowly.
		if len(s) > 4096 {
			return
		}

		// Property 1: never panics.
		out := canonicalJSON(s)

		// Decide whether s is valid JSON. canonicalJSON's contract differs
		// for valid vs invalid inputs, so we branch the further checks.
		var parsed interface{}
		if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &parsed); err != nil {
			// Invalid JSON: canonicalJSON falls back to lower-cased trim;
			// we don't make further property assertions here. Skip.
			return
		}

		// Property 2: idempotence on valid JSON.
		out2 := canonicalJSON(out)
		if out != out2 {
			t.Fatalf("canonicalJSON not idempotent\n input: %q\n once:  %q\n twice: %q", s, out, out2)
		}

		// Property 3: key-order invariance for OBJECTS. We re-marshal the
		// parsed value via Go's encoder — which iterates map keys in
		// pseudo-random order — and verify the canonical form is stable.
		// For non-object inputs (arrays, scalars) this still holds but is
		// less interesting; we run it anyway because it's cheap and
		// exercises the array branch too.
		reMarshalled, err := json.Marshal(parsed)
		if err != nil {
			// json.Marshal of a successfully-Unmarshalled value can fail in
			// pathological cases (e.g. NaN floats); ignore them.
			return
		}
		alt := canonicalJSON(string(reMarshalled))
		if alt != out {
			t.Fatalf("canonicalJSON not key-order invariant\n input:  %q\n out:    %q\n re-out: %q", s, out, alt)
		}
	})
}
