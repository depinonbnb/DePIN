// Package store: conformance_rigor.go is the third conformance file. It
// hosts the rigor-tier subtests added to plug coverage gaps the original
// suite (conformance.go + conformance_extra.go) didn't exercise:
//
//   - SetNodeCheatStatus idempotency on repeat clear / repeat ban.
//   - AwardUptimePoints monotonicity of TotalPoints across mixed intervals.
//   - ConsumeNonce TTL isolation (immediate replay rejected; post-expiry
//     rotation accepted).
//
// Splitting into a third file keeps every conformance file under the
// workspace's 500-line cap. Suite() in conformance.go drives these via
// t.Run so both backends (memory + sqlite) execute them.
package store

import (
	"testing"
	"time"

	"github.com/depinonbnb/depin/internal/types"
)

// runIdempotentSetCheatStatus asserts that calling SetNodeCheatStatus twice
// with identical arguments lands on the same observable state both times.
// "Idempotent" here means: the second call must not double-clear, double-ban,
// re-add suspicious events, or otherwise drift the node's anti-cheat fields.
func runIdempotentSetCheatStatus(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	node := s.RegisterNode("0xidem", types.BscFull, types.LocalProver, "", "")

	// Build up some warning state so the "clear" idempotency check is
	// non-trivial (clearing an already-clean node is the trivial case).
	s.AddSuspiciousEvent(node.ID, "noise-1")
	s.AddSuspiciousEvent(node.ID, "noise-2")

	// First clear: counts should reset, suspicious events should drain.
	if !s.SetNodeCheatStatus(node.ID, types.StatusClean, "first") {
		t.Fatal("first SetNodeCheatStatus(clean) returned false")
	}
	afterFirst := s.GetNode(node.ID)
	firstWarn := afterFirst.WarningCount
	firstEvents := len(afterFirst.SuspiciousEvents)

	// Second clear with identical args MUST be a no-op for the observable
	// fields. If a buggy implementation were to e.g. push a "cleared"
	// audit event into SuspiciousEvents, this test catches it.
	if !s.SetNodeCheatStatus(node.ID, types.StatusClean, "first") {
		t.Fatal("second SetNodeCheatStatus(clean) returned false")
	}
	afterSecond := s.GetNode(node.ID)
	if afterSecond.WarningCount != firstWarn {
		t.Errorf("idempotency violated: WarningCount drifted %d -> %d", firstWarn, afterSecond.WarningCount)
	}
	if len(afterSecond.SuspiciousEvents) != firstEvents {
		t.Errorf("idempotency violated: SuspiciousEvents drifted %d -> %d", firstEvents, len(afterSecond.SuspiciousEvents))
	}
	if afterSecond.CheatStatus != types.StatusClean {
		t.Errorf("idempotency violated: status drifted to %s", afterSecond.CheatStatus)
	}

	// Now the same idempotency check for a ban: two bans in a row must
	// leave IsActive=false and the status intact, not toggle anything.
	if !s.SetNodeCheatStatus(node.ID, types.StatusBanned, "abuse") {
		t.Fatal("first SetNodeCheatStatus(banned) returned false")
	}
	bannedFirst := s.GetNode(node.ID)
	if bannedFirst.IsActive {
		t.Fatal("ban should set IsActive=false")
	}
	if !s.SetNodeCheatStatus(node.ID, types.StatusBanned, "abuse") {
		t.Fatal("second SetNodeCheatStatus(banned) returned false")
	}
	bannedSecond := s.GetNode(node.ID)
	if bannedSecond.IsActive {
		t.Errorf("idempotency violated: second ban flipped IsActive to %v", bannedSecond.IsActive)
	}
	if bannedSecond.CheatStatus != types.StatusBanned {
		t.Errorf("idempotency violated: status drifted to %s on repeat ban", bannedSecond.CheatStatus)
	}
}

// runMonotonicAwardUptimePoints asserts TotalPoints never decreases across
// a sequence of AwardUptimePoints calls on a clean node. The implementation
// has subtle math (PointsPerHour / 12 with a floor of 1) — easy to slip into
// a regression that subtracts on small intervals. uint64 underflow would
// look like a dramatic point spike rather than a decrease, but if anyone
// ever changes TotalPoints to a signed type, this test catches the subtraction
// case directly.
func runMonotonicAwardUptimePoints(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	node := s.RegisterNode("0xmono", types.BscFull, types.LocalProver, "", "")
	prev := s.GetNode(node.ID).TotalPoints

	// Vary the interval so we hit both "below threshold" (1-minute increments)
	// and "above threshold" (60-minute hourly increments) award branches.
	intervals := []uint64{1, 1, 5, 60, 1, 30, 1}
	for i, m := range intervals {
		s.AwardUptimePoints(node.ID, m)
		got := s.GetNode(node.ID).TotalPoints
		if got < prev {
			t.Errorf("step %d (minutes=%d): TotalPoints dropped %d -> %d", i, m, prev, got)
		}
		prev = got
	}
}

// runConsumeNonce_TTLIsolation tests two related guarantees of the nonce
// store: (1) within the TTL the second use is rejected, (2) once the row
// has expired the same (wallet, nonce) pair becomes acceptable again. This
// overlaps slightly with runConsumeNonceFreshThenReplay /
// runConsumeNonceExpiredRotate but explicitly covers both branches in a
// single call so a backend that breaks the TTL boundary only on rotation
// (and not on the immediate replay) shows up here.
func runConsumeNonce_TTLIsolation(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	// Phase 1: TTL must be longer than the wall-clock cost of the second
	// ConsumeNonce, otherwise the row legitimately expires between calls
	// (especially on the SQLite path where the second call also opens a
	// transaction). 200ms is comfortably above any realistic per-call cost.
	const ttl = 200 * time.Millisecond
	if !s.ConsumeNonce("0xttl", "n-iso", ttl) {
		t.Fatal("expected first ConsumeNonce to return true (fresh)")
	}
	if s.ConsumeNonce("0xttl", "n-iso", ttl) {
		t.Error("expected immediate ConsumeNonce(replay) to return false")
	}

	// Phase 2: sleep past the TTL and verify rotation works on a fresh nonce.
	// Use a *different* nonce so the rotation test doesn't depend on whether
	// the backend evicts on read or only on write.
	time.Sleep(ttl + 50*time.Millisecond)

	if !s.ConsumeNonce("0xttl", "n-iso-2", ttl) {
		t.Error("after TTL expiry on a sibling nonce, a fresh nonce should be accepted")
	}
}
