package memory

import (
	"testing"
	"time"

	"github.com/depinonbnb/depin/internal/types"
)

func TestFlaggingIsRecoverable(t *testing.T) {
	s := NewMemory()
	defer s.Close()
	node := s.RegisterNode("0xrec", types.BscFull, types.ExposedRPC, "http://x", "")

	suspicious := func() *types.VerificationResult {
		return &types.VerificationResult{
			NodeID: node.ID, Passed: true, Suspicious: true,
			SuspiciousNote: "High latency", Timestamp: time.Now().UnixMilli(),
		}
	}
	cleanPass := func() *types.VerificationResult {
		return &types.VerificationResult{NodeID: node.ID, Passed: true, Timestamp: time.Now().UnixMilli()}
	}

	// 5 suspicious answers -> flagged.
	for i := 0; i < 5; i++ {
		s.RecordVerificationResult(suspicious())
	}
	if got := s.GetNode(node.ID).CheatStatus; got != types.StatusFlagged {
		t.Fatalf("after 5 suspicious, want flagged, got %s", got)
	}

	// Clean, fast passes recover it back to clean.
	for i := 0; i < 5; i++ {
		s.RecordVerificationResult(cleanPass())
	}
	n := s.GetNode(node.ID)
	if n.CheatStatus != types.StatusClean {
		t.Fatalf("after clean passes, want clean, got %s (warnings=%d)", n.CheatStatus, n.WarningCount)
	}
	if n.WarningCount != 0 {
		t.Fatalf("warning_count should be 0 after recovery, got %d", n.WarningCount)
	}
}

func TestBannedDoesNotAutoRecover(t *testing.T) {
	s := NewMemory()
	defer s.Close()
	node := s.RegisterNode("0xban", types.BscFull, types.ExposedRPC, "http://x", "")

	s.SetNodeCheatStatus(node.ID, types.StatusBanned, "manual ban")
	s.RecordVerificationResult(&types.VerificationResult{
		NodeID: node.ID, Passed: true, Timestamp: time.Now().UnixMilli(),
	})
	if got := s.GetNode(node.ID).CheatStatus; got != types.StatusBanned {
		t.Fatalf("banned node must not auto-recover, got %s", got)
	}
}
