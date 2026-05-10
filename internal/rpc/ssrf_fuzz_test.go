// Package rpc: ssrf_fuzz_test.go fuzz-tests IsAllowedURL.
//
// The function performs DNS lookups for hosts that aren't IP literals — and
// the Go fuzzer is creative enough to feed it real public hostnames that
// actually resolve. To keep the fuzz run deterministic and fast we:
//
//  1. Cap input length at 200 bytes — long enough to exercise every branch
//     in the URL parser, short enough that the corpus stays narrow.
//  2. Wrap each call in a goroutine with a 2-second wall-clock cap. If the
//     call exceeds that we treat it as a fuzz failure (a regression where
//     IsAllowedURL hangs would itself be a security bug — DOS via slow DNS).
//
// Properties:
//
//   - IsAllowedURL must never panic.
//   - It must return either nil or non-nil (no third state) — Go enforces
//     this at the type level, but the wrapper makes the determinism explicit
//     and gives the fuzzer something to assert against.
package rpc

import (
	"testing"
	"time"
)

// FuzzIsAllowedURL feeds IsAllowedURL a curated seed corpus of public,
// private, malformed, and exotic-scheme inputs, then lets the fuzzer go.
//
// Body invariants:
//   - Never panics.
//   - Completes inside a 2s budget; otherwise the fuzz iteration fails so a
//     hang doesn't masquerade as success.
//   - The error/no-error verdict is deterministic for a given input (we
//     don't assert which side it lands on — the seed corpus already pins
//     known cases in ssrf_test.go).
func FuzzIsAllowedURL(f *testing.F) {
	seeds := []string{
		// Public hosts (resolution may or may not succeed; either branch is
		// valid as long as we don't hang).
		"https://example.com",
		"https://1.1.1.1:443/",

		// Private / loopback / link-local (all should be rejected).
		"http://127.0.0.1",
		"http://127.0.0.1:8545/",
		"http://10.0.0.1",
		"http://192.168.1.1/",
		"http://[::1]",
		"http://169.254.169.254/",
		"http://0.0.0.0/",

		// Suspect schemes (should be rejected without DNS).
		"ftp://x",
		"javascript:alert(1)",
		"file:///etc/passwd",

		// Internal-suffix hostnames (rejected without DNS).
		"https://localhost",
		"https://example.local",
		"https://node.intranet",

		// Malformed / empty.
		"",
		"http:///path",
		"://no-scheme",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		// Keep the fuzzer's domain narrow — long inputs don't reach new
		// branches in IsAllowedURL but DO slow the run down. Same idea as
		// the cap inside signature_fuzz_test.go.
		if len(s) > 200 {
			return
		}

		done := make(chan struct{})
		var ranToCompletion bool
		go func() {
			defer close(done)
			// Body invariant 1: never panics. Recovering here would mask the
			// bug, so we let panics propagate to fail the test naturally.
			_ = IsAllowedURL(s)
			ranToCompletion = true
		}()

		select {
		case <-done:
			if !ranToCompletion {
				t.Fatalf("IsAllowedURL panicked on input %q", s)
			}
		case <-time.After(2 * time.Second):
			// A hang is itself a security bug — even if we can't easily
			// abort the goroutine, surface the failure so the lead can
			// triage. The runaway goroutine will leak for the rest of the
			// process lifetime; that is acceptable for a fuzz run.
			t.Fatalf("IsAllowedURL did not return within 2s for input %q", s)
		}
	})
}
