// Package store: conformance_extra.go is a continuation of conformance.go.
// It hosts the longer subtest bodies for verification, heartbeats, stats,
// uptime, suspicious-event handling, flagging, and admin overrides so that
// no single conformance file exceeds the workspace's 500-line limit.
package store

import (
	"testing"
	"time"

	"github.com/depinonbnb/depin/internal/types"
)

// ---------------------------------------------------------------------------
// Verification subtests
// ---------------------------------------------------------------------------

func runRecordVerificationPassedFailed(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	node := s.RegisterNode("0xtest", types.BscFull, types.LocalProver, "", "")

	s.RecordVerificationResult(&types.VerificationResult{
		ChallengeID:    "c1",
		NodeID:         node.ID,
		Passed:         true,
		ResponseTimeMs: 50,
		Timestamp:      1000,
	})
	afterPass := s.GetNode(node.ID)
	if afterPass.TotalChallengesPassed != 1 {
		t.Errorf("expected 1 passed challenge, got %d", afterPass.TotalChallengesPassed)
	}
	if afterPass.LastVerifiedAt != 1000 {
		t.Errorf("expected LastVerifiedAt=1000, got %d", afterPass.LastVerifiedAt)
	}

	s.RecordVerificationResult(&types.VerificationResult{
		ChallengeID:    "c2",
		NodeID:         node.ID,
		Passed:         false,
		ResponseTimeMs: 100,
		FailureReason:  "wrong answer",
		Timestamp:      2000,
	})
	afterFail := s.GetNode(node.ID)
	if afterFail.TotalChallengesFailed != 1 {
		t.Errorf("expected 1 failed challenge, got %d", afterFail.TotalChallengesFailed)
	}
	if afterFail.LastVerifiedAt != 2000 {
		t.Errorf("expected LastVerifiedAt=2000, got %d", afterFail.LastVerifiedAt)
	}
}

func runRecordVerificationEscalation(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	node := s.RegisterNode("0xtest", types.BscFull, types.LocalProver, "", "")

	// One suspicious event: warning_count == 1, status still Clean.
	s.RecordVerificationResult(&types.VerificationResult{
		ChallengeID:    "c1",
		NodeID:         node.ID,
		Passed:         true,
		Suspicious:     true,
		SuspiciousNote: "High latency detected",
		Timestamp:      1000,
	})
	afterOne := s.GetNode(node.ID)
	if afterOne.WarningCount != 1 {
		t.Errorf("expected warning_count=1, got %d", afterOne.WarningCount)
	}
	if afterOne.CheatStatus != types.StatusClean {
		t.Errorf("expected status clean after one event, got %s", afterOne.CheatStatus)
	}

	// Second suspicious event: now Warning.
	s.RecordVerificationResult(&types.VerificationResult{
		ChallengeID:    "c2",
		NodeID:         node.ID,
		Passed:         true,
		Suspicious:     true,
		SuspiciousNote: "Another suspicious event",
		Timestamp:      2000,
	})
	afterTwo := s.GetNode(node.ID)
	if afterTwo.CheatStatus != types.StatusWarning {
		t.Errorf("expected warning status after 2 events, got %s", afterTwo.CheatStatus)
	}
	if len(afterTwo.SuspiciousEvents) != 2 {
		t.Errorf("expected 2 suspicious events on node, got %d", len(afterTwo.SuspiciousEvents))
	}
}

func runRecordVerificationFlagged(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	node := s.RegisterNode("0xtest", types.BscFull, types.LocalProver, "", "")

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
	got := s.GetNode(node.ID)
	if got.CheatStatus != types.StatusFlagged {
		t.Errorf("expected flagged status after 5 suspicious events, got %s", got.CheatStatus)
	}
	if got.WarningCount != 5 {
		t.Errorf("expected warning_count=5, got %d", got.WarningCount)
	}
}

func runRecordVerificationMissingNode(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	// Per persist-coder2 note: RecordVerificationResult must silently no-op
	// when the node is gone, mirroring the in-memory store. No panic.
	s.RecordVerificationResult(&types.VerificationResult{
		ChallengeID: "c-orphan",
		NodeID:      "ghost-node",
		Passed:      true,
		Timestamp:   1234,
	})
	hist := s.GetVerificationHistory("ghost-node", 10)
	if len(hist) != 0 {
		t.Errorf("expected no history for missing node, got %d entries", len(hist))
	}
}

func runGetVerificationHistory(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	node := s.RegisterNode("0xhist", types.BscFull, types.LocalProver, "", "")

	for i := 0; i < 5; i++ {
		s.RecordVerificationResult(&types.VerificationResult{
			ChallengeID:    "c",
			NodeID:         node.ID,
			Passed:         true,
			ResponseTimeMs: uint64(i),
			Timestamp:      int64(i + 1),
		})
	}

	hist := s.GetVerificationHistory(node.ID, 3)
	if len(hist) != 3 {
		t.Fatalf("expected 3 history entries with limit=3, got %d", len(hist))
	}
	// Oldest-first; with timestamps 1..5 and limit=3 we expect ts 3,4,5.
	want := []int64{3, 4, 5}
	for i, h := range hist {
		if h.Timestamp != want[i] {
			t.Errorf("history[%d] timestamp = %d, want %d", i, h.Timestamp, want[i])
		}
	}

	// limit larger than history should return all entries.
	all := s.GetVerificationHistory(node.ID, 50)
	if len(all) != 5 {
		t.Errorf("expected 5 history entries with high limit, got %d", len(all))
	}
}

// ---------------------------------------------------------------------------
// Heartbeat / stats subtests
// ---------------------------------------------------------------------------

func runHeartbeats(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	node := s.RegisterNode("0xhb", types.BscFull, types.LocalProver, "", "")

	s.RecordHeartbeat(&types.HeartbeatRecord{
		NodeID:      node.ID,
		Timestamp:   1000,
		BlockNumber: 100,
		IsSynced:    true,
		LatencyMs:   42,
		PeersCount:  9,
	})
	s.RecordHeartbeat(&types.HeartbeatRecord{
		NodeID:      node.ID,
		Timestamp:   2000,
		BlockNumber: 200,
		IsSynced:    false,
		LatencyMs:   84,
		PeersCount:  2,
	})

	all := s.GetHeartbeats(node.ID, 0)
	if len(all) != 2 {
		t.Fatalf("expected 2 heartbeats with since=0, got %d", len(all))
	}

	// Verify field round-trip on the first row.
	first := all[0]
	if first.BlockNumber != 100 || !first.IsSynced || first.LatencyMs != 42 || first.PeersCount != 9 {
		t.Errorf("heartbeat round-trip mismatch: %+v", first)
	}

	filtered := s.GetHeartbeats(node.ID, 1500)
	if len(filtered) != 1 {
		t.Errorf("expected 1 heartbeat since=1500, got %d", len(filtered))
	}

	none := s.GetHeartbeats(node.ID, 99999999)
	if len(none) != 0 {
		t.Errorf("expected 0 heartbeats since future timestamp, got %d", len(none))
	}
}

func runGetNodeStats(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	if got := s.GetNodeStats("nonexistent"); got != nil {
		t.Error("GetNodeStats on missing node should return nil")
	}

	node := s.RegisterNode("0xstats", types.BscArchive, types.LocalProver, "", "")
	stats := s.GetNodeStats(node.ID)
	if stats == nil {
		t.Fatal("GetNodeStats returned nil for existing node")
	}
	if stats.NodeID != node.ID {
		t.Errorf("node ID mismatch: got %s", stats.NodeID)
	}
	if stats.TotalPoints != 100 {
		t.Errorf("expected 100 points (BscArchive bonus), got %d", stats.TotalPoints)
	}
	if stats.CheatStatus != types.StatusClean {
		t.Errorf("expected clean status, got %s", stats.CheatStatus)
	}

	// Add 3 verifications: 2 pass + 1 fail in the last 24h, latency
	// 100/200/300 -> avg 200, passRate 66.66...
	now := time.Now().UnixMilli()
	s.RecordVerificationResult(&types.VerificationResult{NodeID: node.ID, ChallengeID: "a", Passed: true, ResponseTimeMs: 100, Timestamp: now - 1000})
	s.RecordVerificationResult(&types.VerificationResult{NodeID: node.ID, ChallengeID: "b", Passed: true, ResponseTimeMs: 200, Timestamp: now - 500})
	s.RecordVerificationResult(&types.VerificationResult{NodeID: node.ID, ChallengeID: "c", Passed: false, ResponseTimeMs: 300, Timestamp: now - 100})

	ns := s.GetNodeStats(node.ID)
	if ns == nil {
		t.Fatal("GetNodeStats returned nil after recording verifications")
	}
	if ns.AverageLatencyMs < 199 || ns.AverageLatencyMs > 201 {
		t.Errorf("avg latency expected ~200, got %v", ns.AverageLatencyMs)
	}
	if ns.ChallengePassRate < 66 || ns.ChallengePassRate > 67 {
		t.Errorf("pass rate expected ~66.66, got %v", ns.ChallengePassRate)
	}
}

func runGetWalletStats(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	if got := s.GetWalletStats("0xnone"); got != nil {
		t.Error("GetWalletStats on unknown wallet should return nil")
	}

	wallet := "0xmywallet"
	s.RegisterNode(wallet, types.BscArchive, types.LocalProver, "", "") // 100
	s.RegisterNode(wallet, types.BscFull, types.LocalProver, "", "")    // 50

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
	if stats.FlaggedNodes != 0 {
		t.Errorf("expected 0 flagged nodes, got %d", stats.FlaggedNodes)
	}
}

// ---------------------------------------------------------------------------
// Uptime / cheat / admin subtests
// ---------------------------------------------------------------------------

func runAwardUptimePoints(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	node := s.RegisterNode("0xup", types.BscFull, types.LocalProver, "", "")
	initialPoints := node.TotalPoints

	s.AwardUptimePoints(node.ID, 5)
	updated := s.GetNode(node.ID)
	if updated.TotalUptimeMinutes != 5 {
		t.Errorf("expected 5 uptime minutes, got %d", updated.TotalUptimeMinutes)
	}
	// BscFull: PointsPerHour=6, /12 = 0 -> minimum 1 point/interval.
	if updated.TotalPoints < initialPoints+1 {
		t.Errorf("expected at least %d points after uptime, got %d", initialPoints+1, updated.TotalPoints)
	}

	// Second award accumulates.
	s.AwardUptimePoints(node.ID, 10)
	updated = s.GetNode(node.ID)
	if updated.TotalUptimeMinutes != 15 {
		t.Errorf("expected 15 cumulative uptime minutes, got %d", updated.TotalUptimeMinutes)
	}
}

func runAwardUptimeFlaggedNoop(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	flagged := s.RegisterNode("0xflagged", types.BscFull, types.LocalProver, "", "")
	flaggedInitial := flagged.TotalPoints
	s.SetNodeCheatStatus(flagged.ID, types.StatusFlagged, "test")
	s.AwardUptimePoints(flagged.ID, 5)
	got := s.GetNode(flagged.ID)
	if got.TotalPoints != flaggedInitial {
		t.Errorf("flagged node should not gain points: initial=%d got=%d", flaggedInitial, got.TotalPoints)
	}

	banned := s.RegisterNode("0xbanned", types.BscFull, types.LocalProver, "", "")
	bannedInitial := banned.TotalPoints
	s.SetNodeCheatStatus(banned.ID, types.StatusBanned, "test")
	s.AwardUptimePoints(banned.ID, 5)
	got = s.GetNode(banned.ID)
	if got.TotalPoints != bannedInitial {
		t.Errorf("banned node should not gain points: initial=%d got=%d", bannedInitial, got.TotalPoints)
	}
}

func runAddSuspiciousEvent(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	node := s.RegisterNode("0xsus", types.BscFull, types.LocalProver, "", "")

	// First event: warning_count=1, status still Clean.
	s.AddSuspiciousEvent(node.ID, "first")
	got := s.GetNode(node.ID)
	if got.WarningCount != 1 {
		t.Errorf("expected warning_count=1, got %d", got.WarningCount)
	}
	if got.CheatStatus != types.StatusClean {
		t.Errorf("expected clean status, got %s", got.CheatStatus)
	}

	// Two more -> 3 events, status Warning (>=2).
	s.AddSuspiciousEvent(node.ID, "second")
	s.AddSuspiciousEvent(node.ID, "third")
	got = s.GetNode(node.ID)
	if got.CheatStatus != types.StatusWarning {
		t.Errorf("expected warning status after 3 events, got %s", got.CheatStatus)
	}

	// Push past 20 to verify the cap.
	for i := 0; i < 25; i++ {
		s.AddSuspiciousEvent(node.ID, "spam")
	}
	got = s.GetNode(node.ID)
	if len(got.SuspiciousEvents) > 20 {
		t.Errorf("suspicious events should be capped at 20, got %d", len(got.SuspiciousEvents))
	}
	if got.CheatStatus != types.StatusFlagged {
		t.Errorf("expected flagged status after >5 events, got %s", got.CheatStatus)
	}
}

func runAddSuspiciousEventMissing(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)
	// Must not panic, must not error.
	s.AddSuspiciousEvent("ghost", "noop")
}

func runGetFlaggedNodes(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	s.RegisterNode("0x1", types.BscFull, types.LocalProver, "", "")
	n2 := s.RegisterNode("0x2", types.BscFull, types.LocalProver, "", "")
	n3 := s.RegisterNode("0x3", types.BscFull, types.LocalProver, "", "")

	s.SetNodeCheatStatus(n2.ID, types.StatusWarning, "watch")
	s.SetNodeCheatStatus(n3.ID, types.StatusFlagged, "manual review")

	flagged := s.GetFlaggedNodes()
	if len(flagged) != 2 {
		t.Errorf("expected 2 flagged nodes (warning + flagged), got %d", len(flagged))
	}
}

func runSetNodeCheatStatus(t *testing.T, mk func(t testing.TB) Store) {
	s := mk(t)
	defer mustClose(t, s)

	node := s.RegisterNode("0xset", types.BscFull, types.LocalProver, "", "")

	// Build up some warnings.
	s.AddSuspiciousEvent(node.ID, "first")
	s.AddSuspiciousEvent(node.ID, "second")
	got := s.GetNode(node.ID)
	if got.WarningCount == 0 {
		t.Fatal("setup: expected warning_count > 0")
	}

	// Clear: warning count and suspicious events both reset.
	if !s.SetNodeCheatStatus(node.ID, types.StatusClean, "cleared") {
		t.Fatal("SetNodeCheatStatus(clean) should return true")
	}
	got = s.GetNode(node.ID)
	if got.CheatStatus != types.StatusClean {
		t.Errorf("expected clean status, got %s", got.CheatStatus)
	}
	if got.WarningCount != 0 {
		t.Errorf("expected warning_count=0 after clear, got %d", got.WarningCount)
	}
	if len(got.SuspiciousEvents) != 0 {
		t.Errorf("expected suspicious events empty after clear, got %d", len(got.SuspiciousEvents))
	}

	// Ban: deactivates the node.
	if !s.SetNodeCheatStatus(node.ID, types.StatusBanned, "confirmed cheating") {
		t.Fatal("SetNodeCheatStatus(banned) should return true")
	}
	got = s.GetNode(node.ID)
	if got.IsActive {
		t.Error("banned node should be deactivated")
	}
	if got.CheatStatus != types.StatusBanned {
		t.Errorf("expected banned status, got %s", got.CheatStatus)
	}

	// Missing node returns false.
	if s.SetNodeCheatStatus("ghost", types.StatusBanned, "") {
		t.Error("SetNodeCheatStatus on missing node should return false")
	}
}
