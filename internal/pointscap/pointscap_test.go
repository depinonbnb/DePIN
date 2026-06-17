package pointscap

import (
	"testing"
	"time"
)

func TestDisabledByDefault(t *testing.T) {
	l := NewLimiter(10 * time.Minute) // not enabled
	if got := l.Allow("n", 999, 10); got != 999 {
		t.Fatalf("disabled limiter should pass through, got %d", got)
	}
}

func TestCapsWithinWindow(t *testing.T) {
	l := NewLimiter(10 * time.Minute)
	l.Enable(10 * time.Minute)
	base := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return base }

	// 3 + 3 + 3 = 9 allowed in full against a cap of 10
	for i := 0; i < 3; i++ {
		if got := l.Allow("n", 3, 10); got != 3 {
			t.Fatalf("award %d: got %d, want 3", i, got)
		}
	}
	if got := l.Allow("n", 3, 10); got != 1 {
		t.Fatalf("4th award should be capped to 1, got %d", got)
	}
	if got := l.Allow("n", 3, 10); got != 0 {
		t.Fatalf("over-cap award should be 0, got %d", got)
	}
}

func TestPerTierCap(t *testing.T) {
	l := NewLimiter(10 * time.Minute)
	l.Enable(10 * time.Minute)
	base := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return base }

	// node "a" has a cap of 20, node "b" a cap of 6 — independent budgets.
	if got := l.Allow("a", 20, 20); got != 20 {
		t.Fatalf("a should get full 20, got %d", got)
	}
	if got := l.Allow("b", 10, 6); got != 6 {
		t.Fatalf("b capped at 6, got %d", got)
	}
}

func TestWindowResets(t *testing.T) {
	l := NewLimiter(10 * time.Minute)
	l.Enable(10 * time.Minute)
	base := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return base }

	if got := l.Allow("n", 10, 10); got != 10 {
		t.Fatalf("first award should be 10, got %d", got)
	}
	if got := l.Allow("n", 5, 10); got != 0 {
		t.Fatalf("should be capped, got %d", got)
	}
	l.now = func() time.Time { return base.Add(11 * time.Minute) }
	if got := l.Allow("n", 5, 10); got != 5 {
		t.Fatalf("after window reset should allow 5, got %d", got)
	}
}
