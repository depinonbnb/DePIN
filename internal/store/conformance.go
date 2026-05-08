// Package store: conformance.go is the cross-implementation behavioural
// contract that every store.Store must satisfy. It is intentionally NOT a
// _test.go file so sub-packages (store/memory, store/sqlite) can import and
// run it from their own *_test.go entry points without depending on a
// shared test-only package layout.
//
// Any new method on Store should grow a corresponding subtest here. Treat
// this file as the source of truth for "what does this store guarantee?".
//
// The subtest bodies for verification, heartbeat, stats, and admin paths live
// in conformance_extra.go to keep individual files under the 500-line limit
// imposed by the workspace's CLAUDE.md rules.
package store

import (
	"testing"

	"github.com/depinonbnb/depin/internal/types"
)

// Suite runs the full conformance test suite against any Store implementation.
// mk must return a fresh, empty Store every call. Each subtest invokes mk so
// individual test cases stay isolated from one another.
//
// The mk factory accepts testing.TB so it can serve both *testing.T and
// *testing.B contexts; subtests cast back to *testing.T when the test body
// needs t-only methods (Run, Helper, etc.).
func Suite(t *testing.T, mk func(t testing.TB) Store) {
	t.Helper()

	// Lifecycle / registration / lookup ------------------------------------
	t.Run("RegisterNode", func(t *testing.T) { runRegisterNode(t, mk) })
	t.Run("RegisterNodeBonusPerType", func(t *testing.T) { runRegisterNodeBonusPerType(t, mk) })
	t.Run("GetNode", func(t *testing.T) { runGetNode(t, mk) })
	t.Run("GetNodesByWallet", func(t *testing.T) { runGetNodesByWallet(t, mk) })
	t.Run("GetAllActiveNodes", func(t *testing.T) { runGetAllActiveNodes(t, mk) })
	t.Run("UpdateNode", func(t *testing.T) { runUpdateNode(t, mk) })

	// Verification / suspicious-event escalation ---------------------------
	t.Run("RecordVerificationResult_PassedAndFailed", func(t *testing.T) { runRecordVerificationPassedFailed(t, mk) })
	t.Run("RecordVerificationResult_SuspiciousEscalation", func(t *testing.T) { runRecordVerificationEscalation(t, mk) })
	t.Run("RecordVerificationResult_FlaggedAtFiveEvents", func(t *testing.T) { runRecordVerificationFlagged(t, mk) })
	t.Run("RecordVerificationResult_MissingNode", func(t *testing.T) { runRecordVerificationMissingNode(t, mk) })
	t.Run("GetVerificationHistory_LimitAndOrder", func(t *testing.T) { runGetVerificationHistory(t, mk) })

	// Heartbeats / stats ---------------------------------------------------
	t.Run("RecordHeartbeat_GetHeartbeats", func(t *testing.T) { runHeartbeats(t, mk) })
	t.Run("GetNodeStats", func(t *testing.T) { runGetNodeStats(t, mk) })
	t.Run("GetWalletStats", func(t *testing.T) { runGetWalletStats(t, mk) })

	// Uptime / cheat / admin -----------------------------------------------
	t.Run("AwardUptimePoints_AwardsAndAccumulates", func(t *testing.T) { runAwardUptimePoints(t, mk) })
	t.Run("AwardUptimePoints_NoOpForFlaggedAndBanned", func(t *testing.T) { runAwardUptimeFlaggedNoop(t, mk) })
	t.Run("AddSuspiciousEvent_CapAndEscalation", func(t *testing.T) { runAddSuspiciousEvent(t, mk) })
	t.Run("AddSuspiciousEvent_MissingNode", func(t *testing.T) { runAddSuspiciousEventMissing(t, mk) })
	t.Run("GetFlaggedNodes", func(t *testing.T) { runGetFlaggedNodes(t, mk) })
	t.Run("SetNodeCheatStatus_ClearAndBan", func(t *testing.T) { runSetNodeCheatStatus(t, mk) })

	// Lifecycle ------------------------------------------------------------
	t.Run("Close_Idempotent", func(t *testing.T) { runCloseIdempotent(t, mk) })
}

// mustClose closes the store and reports any error. It exists because every
// subtest needs the same defer block; centralising it keeps the suite tidy.
func mustClose(t testing.TB, s Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Errorf("store Close returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Registration / lookup subtests
// ---------------------------------------------------------------------------

func runRegisterNode(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	node := s.RegisterNode("0x1234567890abcdef", types.BscArchive, types.LocalProver, "", "")
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
	if node.VerificationMethod != types.LocalProver {
		t.Errorf("verification method mismatch: got %s", node.VerificationMethod)
	}
	if node.TotalPoints != 100 {
		t.Errorf("BSC Archive registration bonus should be 100, got %d", node.TotalPoints)
	}
	if !node.IsActive {
		t.Error("new node should be active")
	}
	if node.CheatStatus != types.StatusClean {
		t.Errorf("new node should have clean status, got %s", node.CheatStatus)
	}
	if node.WarningCount != 0 {
		t.Errorf("new node warning count should be 0, got %d", node.WarningCount)
	}
	if node.RegisteredAt == 0 {
		t.Error("registered_at should be set")
	}
}

func runRegisterNodeBonusPerType(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	tests := []struct {
		nodeType types.NodeType
		expected uint64
	}{
		{types.BscArchive, 100},
		{types.BscFull, 50},
		{types.BscFast, 40},
		{types.OpbnbFull, 40},
		{types.OpbnbFast, 30},
	}
	for _, tt := range tests {
		node := s.RegisterNode("0xtest", tt.nodeType, types.LocalProver, "", "")
		if node == nil {
			t.Fatalf("RegisterNode returned nil for %s", tt.nodeType)
		}
		if node.TotalPoints != tt.expected {
			t.Errorf("%s: expected %d bonus points, got %d", tt.nodeType, tt.expected, node.TotalPoints)
		}
	}
}

func runGetNode(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	if got := s.GetNode("nonexistent"); got != nil {
		t.Errorf("expected nil for missing node, got %+v", got)
	}

	registered := s.RegisterNode("0xtest", types.BscFull, types.LocalProver, "rpc.example", "secret-token")
	retrieved := s.GetNode(registered.ID)
	if retrieved == nil {
		t.Fatal("GetNode returned nil for an existing node")
	}
	if retrieved.ID != registered.ID {
		t.Errorf("ID mismatch: got %s, want %s", retrieved.ID, registered.ID)
	}
	if retrieved.RPCEndpoint != "rpc.example" {
		t.Errorf("RPC endpoint mismatch: got %q", retrieved.RPCEndpoint)
	}
	// SuspiciousEvents must be a non-nil slice for both backends so callers
	// can range over it without nil-checks.
	if retrieved.SuspiciousEvents == nil {
		t.Error("expected SuspiciousEvents to be a non-nil empty slice")
	}
}

func runGetNodesByWallet(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	wallet := "0xmywallet"
	s.RegisterNode(wallet, types.BscFull, types.LocalProver, "", "")
	s.RegisterNode(wallet, types.BscArchive, types.LocalProver, "", "")
	s.RegisterNode("0xother", types.OpbnbFull, types.LocalProver, "", "")

	nodes := s.GetNodesByWallet(wallet)
	if len(nodes) != 2 {
		t.Errorf("expected 2 nodes for wallet, got %d", len(nodes))
	}

	other := s.GetNodesByWallet("0xother")
	if len(other) != 1 {
		t.Errorf("expected 1 node for other wallet, got %d", len(other))
	}

	empty := s.GetNodesByWallet("0xnoone")
	if len(empty) != 0 {
		t.Errorf("expected empty slice for unknown wallet, got %d entries", len(empty))
	}
}

func runGetAllActiveNodes(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	s.RegisterNode("0x1", types.BscFull, types.LocalProver, "", "")
	s.RegisterNode("0x2", types.BscArchive, types.LocalProver, "", "")
	banned := s.RegisterNode("0x3", types.OpbnbFull, types.LocalProver, "", "")
	if !s.SetNodeCheatStatus(banned.ID, types.StatusBanned, "test") {
		t.Fatal("SetNodeCheatStatus should succeed for existing node")
	}

	nodes := s.GetAllActiveNodes()
	if len(nodes) != 2 {
		t.Errorf("expected 2 active nodes (banned excluded), got %d", len(nodes))
	}
	for _, n := range nodes {
		if !n.IsActive {
			t.Errorf("inactive node %s returned by GetAllActiveNodes", n.ID)
		}
		if n.ID == banned.ID {
			t.Errorf("banned node %s leaked into active list", banned.ID)
		}
	}
}

func runUpdateNode(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	node := s.RegisterNode("0xupdater", types.BscFull, types.LocalProver, "", "")
	updated := s.UpdateNode(node.ID, func(n *types.NodeRegistration) {
		n.TotalUptimeMinutes = 42
		n.RPCEndpoint = "rewritten-endpoint"
	})
	if updated == nil {
		t.Fatal("UpdateNode returned nil for existing node")
	}
	if updated.TotalUptimeMinutes != 42 {
		t.Errorf("mutator change not applied: got %d", updated.TotalUptimeMinutes)
	}

	// Reload via GetNode to confirm the mutation was persisted (catches
	// implementations that mutate a copy without writing back).
	reread := s.GetNode(node.ID)
	if reread == nil {
		t.Fatal("GetNode returned nil after UpdateNode")
	}
	if reread.TotalUptimeMinutes != 42 {
		t.Errorf("UpdateNode did not persist; got %d", reread.TotalUptimeMinutes)
	}
	if reread.RPCEndpoint != "rewritten-endpoint" {
		t.Errorf("UpdateNode did not persist endpoint; got %q", reread.RPCEndpoint)
	}

	if got := s.UpdateNode("nonexistent", func(*types.NodeRegistration) {}); got != nil {
		t.Errorf("UpdateNode on missing node should return nil, got %+v", got)
	}
}

func runCloseIdempotent(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	// First close as the test would normally do; second close must not error.
	if err := s.Close(); err != nil {
		t.Errorf("first Close returned error: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close returned error: %v", err)
	}
}
