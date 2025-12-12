package store

import (
	"testing"

	"github.com/depinonbnb/depin/internal/types"
)

func TestNewStore(t *testing.T) {
	s := NewStore()
	if s == nil {
		t.Fatal("NewStore returned nil")
	}
	if s.nodes == nil {
		t.Error("nodes map not initialized")
	}
	if s.nodesByWallet == nil {
		t.Error("nodesByWallet map not initialized")
	}
}

func TestRegisterNode(t *testing.T) {
	s := NewStore()

	node := s.RegisterNode(
		"0x1234567890abcdef",
		types.BscArchive,
		types.LocalProver,
		"",
		"",
	)

	if node == nil {
		t.Fatal("RegisterNode returned nil")
	}

	if node.ID == "" {
		t.Error("node ID should not be empty")
	}

	if node.WalletAddress != "0x1234567890abcdef" {
		t.Errorf("wallet address mismatch: got %s", node.WalletAddress)
	}

	if node.NodeType != types.BscArchive {
		t.Errorf("node type mismatch: got %s", node.NodeType)
	}

	if node.TotalPoints != 100 {
		t.Errorf("BSC Archive should get 100 registration bonus, got %d", node.TotalPoints)
	}

	if !node.IsActive {
		t.Error("new node should be active")
	}

	if node.CheatStatus != types.StatusClean {
		t.Errorf("new node should have clean status, got %s", node.CheatStatus)
	}
}

func TestRegisterNodeBonusPoints(t *testing.T) {
	s := NewStore()

	tests := []struct {
		nodeType      types.NodeType
		expectedBonus uint64
	}{
		{types.BscArchive, 100},
		{types.BscFull, 50},
		{types.BscFast, 40},
		{types.OpbnbFull, 40},
		{types.OpbnbFast, 30},
	}

	for _, tt := range tests {
		node := s.RegisterNode("0xtest", tt.nodeType, types.LocalProver, "", "")
		if node.TotalPoints != tt.expectedBonus {
			t.Errorf("%s: expected %d bonus points, got %d", tt.nodeType, tt.expectedBonus, node.TotalPoints)
		}
	}
}

func TestGetNode(t *testing.T) {
	s := NewStore()

	// Get non-existent node
	if s.GetNode("nonexistent") != nil {
		t.Error("should return nil for non-existent node")
	}

	// Register and get node
	registered := s.RegisterNode("0xtest", types.BscFull, types.LocalProver, "", "")
	retrieved := s.GetNode(registered.ID)

	if retrieved == nil {
		t.Fatal("GetNode returned nil for existing node")
	}

	if retrieved.ID != registered.ID {
		t.Errorf("ID mismatch: got %s, want %s", retrieved.ID, registered.ID)
	}
}

func TestGetNodesByWallet(t *testing.T) {
	s := NewStore()

	wallet := "0xmywallet"

	// Register multiple nodes for same wallet
	s.RegisterNode(wallet, types.BscFull, types.LocalProver, "", "")
	s.RegisterNode(wallet, types.BscArchive, types.LocalProver, "", "")
	s.RegisterNode("0xotherwallet", types.OpbnbFull, types.LocalProver, "", "")

	nodes := s.GetNodesByWallet(wallet)
	if len(nodes) != 2 {
		t.Errorf("expected 2 nodes for wallet, got %d", len(nodes))
	}

	// Check other wallet
	otherNodes := s.GetNodesByWallet("0xotherwallet")
	if len(otherNodes) != 1 {
		t.Errorf("expected 1 node for other wallet, got %d", len(otherNodes))
	}
}

func TestGetAllActiveNodes(t *testing.T) {
	s := NewStore()

	s.RegisterNode("0x1", types.BscFull, types.LocalProver, "", "")
	s.RegisterNode("0x2", types.BscArchive, types.LocalProver, "", "")

	nodes := s.GetAllActiveNodes()
	if len(nodes) != 2 {
		t.Errorf("expected 2 active nodes, got %d", len(nodes))
	}
}

func TestRecordVerificationResult(t *testing.T) {
	s := NewStore()

	node := s.RegisterNode("0xtest", types.BscFull, types.LocalProver, "", "")

	// Record passed verification
	s.RecordVerificationResult(&types.VerificationResult{
		ChallengeID:    "challenge1",
		NodeID:         node.ID,
		Passed:         true,
		ResponseTimeMs: 50,
		Timestamp:      1000,
	})

	updated := s.GetNode(node.ID)
	if updated.TotalChallengesPassed != 1 {
		t.Errorf("expected 1 passed challenge, got %d", updated.TotalChallengesPassed)
	}

	// Record failed verification
	s.RecordVerificationResult(&types.VerificationResult{
		ChallengeID:    "challenge2",
		NodeID:         node.ID,
		Passed:         false,
		ResponseTimeMs: 100,
		Timestamp:      2000,
	})

	updated = s.GetNode(node.ID)
	if updated.TotalChallengesFailed != 1 {
		t.Errorf("expected 1 failed challenge, got %d", updated.TotalChallengesFailed)
	}
}

func TestSuspiciousActivityTracking(t *testing.T) {
	s := NewStore()

	node := s.RegisterNode("0xtest", types.BscFull, types.LocalProver, "", "")

	// Record suspicious verification
	s.RecordVerificationResult(&types.VerificationResult{
		ChallengeID:    "c1",
		NodeID:         node.ID,
		Passed:         true,
		Suspicious:     true,
		SuspiciousNote: "High latency detected",
		Timestamp:      1000,
	})

	updated := s.GetNode(node.ID)
	if updated.WarningCount != 1 {
		t.Errorf("expected warning count 1, got %d", updated.WarningCount)
	}

	// Second suspicious event should trigger warning status
	s.RecordVerificationResult(&types.VerificationResult{
		ChallengeID:    "c2",
		NodeID:         node.ID,
		Passed:         true,
		Suspicious:     true,
		SuspiciousNote: "Another suspicious event",
		Timestamp:      2000,
	})

	updated = s.GetNode(node.ID)
	if updated.CheatStatus != types.StatusWarning {
		t.Errorf("expected warning status after 2 suspicious events, got %s", updated.CheatStatus)
	}
}

func TestFlaggedAfterMultipleSuspicious(t *testing.T) {
	s := NewStore()

	node := s.RegisterNode("0xtest", types.BscFull, types.LocalProver, "", "")

	// Record 5 suspicious events
	for i := 0; i < 5; i++ {
		s.RecordVerificationResult(&types.VerificationResult{
			ChallengeID:    "c",
			NodeID:         node.ID,
			Passed:         true,
			Suspicious:     true,
			SuspiciousNote: "Suspicious",
			Timestamp:      int64(i * 1000),
		})
	}

	updated := s.GetNode(node.ID)
	if updated.CheatStatus != types.StatusFlagged {
		t.Errorf("expected flagged status after 5 suspicious events, got %s", updated.CheatStatus)
	}
}

func TestGetWalletStats(t *testing.T) {
	s := NewStore()

	wallet := "0xmywallet"

	// Non-existent wallet
	if s.GetWalletStats("0xnonexistent") != nil {
		t.Error("should return nil for non-existent wallet")
	}

	// Register nodes
	s.RegisterNode(wallet, types.BscArchive, types.LocalProver, "", "") // 100 pts
	s.RegisterNode(wallet, types.BscFull, types.LocalProver, "", "")    // 50 pts

	stats := s.GetWalletStats(wallet)
	if stats == nil {
		t.Fatal("GetWalletStats returned nil")
	}

	if stats.TotalPoints != 150 {
		t.Errorf("expected 150 total points, got %d", stats.TotalPoints)
	}

	if stats.TotalNodes != 2 {
		t.Errorf("expected 2 total nodes, got %d", stats.TotalNodes)
	}

	if stats.ActiveNodes != 2 {
		t.Errorf("expected 2 active nodes, got %d", stats.ActiveNodes)
	}
}

func TestAwardUptimePoints(t *testing.T) {
	s := NewStore()

	node := s.RegisterNode("0xtest", types.BscFull, types.LocalProver, "", "")
	initialPoints := node.TotalPoints

	s.AwardUptimePoints(node.ID, 5)

	updated := s.GetNode(node.ID)
	if updated.TotalUptimeMinutes != 5 {
		t.Errorf("expected 5 uptime minutes, got %d", updated.TotalUptimeMinutes)
	}

	// BSC Full gets 6 points/hour = 0.5 per 5 min, but minimum is 1
	expectedPoints := initialPoints + 1
	if updated.TotalPoints < expectedPoints {
		t.Errorf("expected at least %d points after uptime, got %d", expectedPoints, updated.TotalPoints)
	}
}

func TestAwardUptimePointsNotForFlagged(t *testing.T) {
	s := NewStore()

	node := s.RegisterNode("0xtest", types.BscFull, types.LocalProver, "", "")
	initialPoints := node.TotalPoints

	// Flag the node
	s.SetNodeCheatStatus(node.ID, types.StatusFlagged, "test")

	// Try to award points
	s.AwardUptimePoints(node.ID, 5)

	updated := s.GetNode(node.ID)
	if updated.TotalPoints != initialPoints {
		t.Error("flagged nodes should not receive uptime points")
	}
}

func TestSetNodeCheatStatus(t *testing.T) {
	s := NewStore()

	node := s.RegisterNode("0xtest", types.BscFull, types.LocalProver, "", "")

	// Add some warnings first
	s.AddSuspiciousEvent(node.ID, "test1")
	s.AddSuspiciousEvent(node.ID, "test2")

	// Clear status
	s.SetNodeCheatStatus(node.ID, types.StatusClean, "cleared")

	updated := s.GetNode(node.ID)
	if updated.CheatStatus != types.StatusClean {
		t.Errorf("expected clean status, got %s", updated.CheatStatus)
	}
	if updated.WarningCount != 0 {
		t.Error("warning count should be reset when cleared")
	}
	if len(updated.SuspiciousEvents) != 0 {
		t.Error("suspicious events should be cleared")
	}

	// Ban node
	s.SetNodeCheatStatus(node.ID, types.StatusBanned, "cheating confirmed")

	updated = s.GetNode(node.ID)
	if updated.IsActive {
		t.Error("banned node should be deactivated")
	}
}

func TestGetFlaggedNodes(t *testing.T) {
	s := NewStore()

	s.RegisterNode("0x1", types.BscFull, types.LocalProver, "", "")
	node2 := s.RegisterNode("0x2", types.BscFull, types.LocalProver, "", "")
	node3 := s.RegisterNode("0x3", types.BscFull, types.LocalProver, "", "")

	s.SetNodeCheatStatus(node2.ID, types.StatusWarning, "suspicious")
	s.SetNodeCheatStatus(node3.ID, types.StatusFlagged, "needs review")

	flagged := s.GetFlaggedNodes()
	if len(flagged) != 2 {
		t.Errorf("expected 2 flagged nodes, got %d", len(flagged))
	}
}

func TestGetNodeStats(t *testing.T) {
	s := NewStore()

	// Non-existent node
	if s.GetNodeStats("nonexistent") != nil {
		t.Error("should return nil for non-existent node")
	}

	node := s.RegisterNode("0xtest", types.BscArchive, types.LocalProver, "", "")

	stats := s.GetNodeStats(node.ID)
	if stats == nil {
		t.Fatal("GetNodeStats returned nil")
	}

	if stats.NodeID != node.ID {
		t.Errorf("node ID mismatch: got %s", stats.NodeID)
	}

	if stats.TotalPoints != 100 {
		t.Errorf("expected 100 points, got %d", stats.TotalPoints)
	}

	if stats.CheatStatus != types.StatusClean {
		t.Errorf("expected clean status, got %s", stats.CheatStatus)
	}
}
