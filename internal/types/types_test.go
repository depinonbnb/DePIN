package types

import "testing"

func TestNodeTypeRegistrationBonus(t *testing.T) {
	tests := []struct {
		nodeType NodeType
		expected uint64
	}{
		{BscArchive, 100},
		{BscFull, 50},
		{BscFast, 40},
		{OpbnbFull, 40},
		{OpbnbFast, 30},
		{NodeType("unknown"), 0},
	}

	for _, tt := range tests {
		got := tt.nodeType.RegistrationBonus()
		if got != tt.expected {
			t.Errorf("%s.RegistrationBonus() = %d, want %d", tt.nodeType, got, tt.expected)
		}
	}
}

func TestNodeTypePointsPerHour(t *testing.T) {
	tests := []struct {
		nodeType NodeType
		expected uint64
	}{
		{BscArchive, 10},
		{BscFull, 6},
		{BscFast, 4},
		{OpbnbFull, 4},
		{OpbnbFast, 3},
		{NodeType("unknown"), 0},
	}

	for _, tt := range tests {
		got := tt.nodeType.PointsPerHour()
		if got != tt.expected {
			t.Errorf("%s.PointsPerHour() = %d, want %d", tt.nodeType, got, tt.expected)
		}
	}
}

func TestNodeTypeMinUptimePercent(t *testing.T) {
	tests := []struct {
		nodeType NodeType
		expected uint8
	}{
		{BscArchive, 95},
		{BscFull, 95},
		{BscFast, 90},
		{OpbnbFull, 90},
		{OpbnbFast, 85},
		{NodeType("unknown"), 90}, // Default is 90
	}

	for _, tt := range tests {
		got := tt.nodeType.MinUptimePercent()
		if got != tt.expected {
			t.Errorf("%s.MinUptimePercent() = %d, want %d", tt.nodeType, got, tt.expected)
		}
	}
}

func TestCheatStatusValues(t *testing.T) {
	if StatusClean != "clean" {
		t.Error("StatusClean should be 'clean'")
	}
	if StatusWarning != "warning" {
		t.Error("StatusWarning should be 'warning'")
	}
	if StatusFlagged != "flagged" {
		t.Error("StatusFlagged should be 'flagged'")
	}
	if StatusBanned != "banned" {
		t.Error("StatusBanned should be 'banned'")
	}
}

func TestVerificationMethodValues(t *testing.T) {
	if LocalProver != "local-prover" {
		t.Error("LocalProver should be 'local-prover'")
	}
	if ExposedRPC != "exposed-rpc" {
		t.Error("ExposedRPC should be 'exposed-rpc'")
	}
}

func TestChallengeTypeValues(t *testing.T) {
	if BlockHash != "block-hash" {
		t.Error("BlockHash should be 'block-hash'")
	}
	if BlockData != "block-data" {
		t.Error("BlockData should be 'block-data'")
	}
	if StateBalance != "state-balance" {
		t.Error("StateBalance should be 'state-balance'")
	}
	if TxReceipt != "tx-receipt" {
		t.Error("TxReceipt should be 'tx-receipt'")
	}
	if SyncStatus != "sync-status" {
		t.Error("SyncStatus should be 'sync-status'")
	}
}

func TestLatencyConstants(t *testing.T) {
	if LatencyLocalNode != 100 {
		t.Errorf("LatencyLocalNode should be 100, got %d", LatencyLocalNode)
	}
	if LatencySuspiciousMin != 500 {
		t.Errorf("LatencySuspiciousMin should be 500, got %d", LatencySuspiciousMin)
	}
	if LatencyPublicRPC != 300 {
		t.Errorf("LatencyPublicRPC should be 300, got %d", LatencyPublicRPC)
	}
	if LatencyMaxAllowed != 5000 {
		t.Errorf("LatencyMaxAllowed should be 5000, got %d", LatencyMaxAllowed)
	}

	// Ensure thresholds are in the correct order. The suspicious-latency flag
	// sits ABOVE the public-RPC round-trip estimate: we only treat a response as
	// "probably relaying" when it's slower than even a public RPC would be, so a
	// real local node on normal hardware isn't false-flagged.
	if LatencyLocalNode >= LatencyPublicRPC {
		t.Error("LatencyLocalNode should be less than LatencyPublicRPC")
	}
	if LatencyPublicRPC >= LatencySuspiciousMin {
		t.Error("LatencyPublicRPC should be less than LatencySuspiciousMin")
	}
	if LatencySuspiciousMin >= LatencyMaxAllowed {
		t.Error("LatencySuspiciousMin should be less than LatencyMaxAllowed")
	}
}
