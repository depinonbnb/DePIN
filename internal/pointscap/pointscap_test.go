package pointscap

import (
	"testing"
	"time"
)

func TestDisabledByDefault(t *testing.T) {
	l := NewLimiter(0, 0)
	if got := l.Allow("n", 999); got != 999 {
		t.Fatalf("disabled limiter should pass through, got %d", got)
	}
}

func TestCapsWithinWindow(t *testing.T) {
	l := NewLimiter(10, 10*time.Minute)
	base := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return base }

	// 3 + 3 + 3 = 9 allowed in full
	for i := 0; i < 3; i++ {
		if got := l.Allow("n", 3); got != 3 {
			t.Fatalf("award %d: got %d, want 3", i, got)
		}
	}
	// next 3 only has 1 remaining (9 -> 10)
	if got := l.Allow("n", 3); got != 1 {
		t.Fatalf("4th award should be capped to 1, got %d", got)
	}
	// fully capped now
	if got := l.Allow("n", 3); got != 0 {
		t.Fatalf("over-cap award should be 0, got %d", got)
	}
}

func TestWindowResets(t *testing.T) {
	l := NewLimiter(10, 10*time.Minute)
	base := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return base }

	if got := l.Allow("n", 10); got != 10 {
		t.Fatalf("first award should be 10, got %d", got)
	}
	if got := l.Allow("n", 5); got != 0 {
		t.Fatalf("should be capped, got %d", got)
	}

	// advance past the window — fresh budget
	l.now = func() time.Time { return base.Add(11 * time.Minute) }
	if got := l.Allow("n", 5); got != 5 {
		t.Fatalf("after window reset should allow 5, got %d", got)
	}
}

func TestPerNodeIndependent(t *testing.T) {
	l := NewLimiter(10, 10*time.Minute)
	base := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return base }

	l.Allow("a", 10) // exhaust a
	if got := l.Allow("a", 5); got != 0 {
		t.Fatalf("node a should be capped, got %d", got)
	}
	if got := l.Allow("b", 7); got != 7 {
		t.Fatalf("node b has its own budget, got %d", got)
	}
}
