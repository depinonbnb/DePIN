package memory

import (
	"testing"
	"time"

	"github.com/depinonbnb/depin/internal/types"
)

// TestRecordVerificationResultHonorsSkipPointsAward proves the store withholds
// challenge points when the token gate sets SkipPointsAward, while still
// recording the pass (counter + last-verified).
func TestRecordVerificationResultHonorsSkipPointsAward(t *testing.T) {
	s := NewMemory()
	defer s.Close()

	node := s.RegisterNode("0xabc", types.BscFull, types.ExposedRPC, "http://x", "")
	base := s.GetNode(node.ID).TotalPoints

	// Passed but gated: the pass counts, the points are withheld.
	s.RecordVerificationResult(&types.VerificationResult{
		NodeID:          node.ID,
		Passed:          true,
		SkipPointsAward: true,
		Timestamp:       time.Now().UnixMilli(),
	})
	got := s.GetNode(node.ID)
	if got.TotalPoints != base {
		t.Fatalf("gated pass changed points: got %d, want %d (unchanged)", got.TotalPoints, base)
	}
	if got.TotalChallengesPassed != 1 {
		t.Fatalf("gated pass not counted: got %d passes, want 1", got.TotalChallengesPassed)
	}

	// Passed and allowed: points are credited.
	s.RecordVerificationResult(&types.VerificationResult{
		NodeID:    node.ID,
		Passed:    true,
		Timestamp: time.Now().UnixMilli(),
	})
	got = s.GetNode(node.ID)
	if got.TotalPoints <= base {
		t.Fatalf("ungated pass did not credit points: got %d, want > %d", got.TotalPoints, base)
	}
	if got.TotalChallengesPassed != 2 {
		t.Fatalf("expected 2 passes recorded, got %d", got.TotalChallengesPassed)
	}
}
