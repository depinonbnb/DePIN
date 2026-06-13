package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/depinonbnb/depin/internal/store/memory"
	"github.com/depinonbnb/depin/internal/types"
	"github.com/depinonbnb/depin/internal/verification"
)

// fakeHolder is a deterministic token.Checker for gate tests: it allows exactly
// the wallets present (and true) in allow.
type fakeHolder struct {
	enabled bool
	allow   map[string]bool
}

func (f fakeHolder) Enabled() bool                     { return f.enabled }
func (f fakeHolder) Allow(w string) bool               { return f.allow[w] }
func (f fakeHolder) IsHolder(w string) (bool, bool)    { return f.allow[w], true }
func (f fakeHolder) Refresh(context.Context, []string) {}

// TestUptimeRewardTokenGate proves the per-cycle gate: a synced, eligible node
// accrues uptime points only while the token gate allows its wallet.
func TestUptimeRewardTokenGate(t *testing.T) {
	s := memory.NewMemory()
	defer s.Close()
	v := verification.NewVerifier([]string{"http://example.invalid"})

	node := s.RegisterNode("0x1111", types.BscFull, types.ExposedRPC, "http://example.invalid", "")
	// Mark the node synced inside the reward window so it is otherwise eligible.
	s.RecordHeartbeat(&types.HeartbeatRecord{
		NodeID:    node.ID,
		Timestamp: time.Now().UnixMilli(),
		IsSynced:  true,
	})

	sched := New(s, v, Config{
		HeartbeatInterval: 5 * time.Minute,
		RewardInterval:    5 * time.Minute,
		SnapshotInterval:  -1,
		Disabled:          true,
	})
	defer sched.Close()

	base := s.GetNode(node.ID).TotalPoints

	// Gate denies the wallet: uptime points must NOT accrue.
	sched.SetHolderChecker(fakeHolder{enabled: true, allow: map[string]bool{}})
	sched.runUptimeRewardOnce()
	if got := s.GetNode(node.ID).TotalPoints; got != base {
		t.Fatalf("non-holder accrued uptime points: got %d, want %d (unchanged)", got, base)
	}

	// Gate allows the wallet: uptime points accrue.
	sched.SetHolderChecker(fakeHolder{enabled: true, allow: map[string]bool{"0x1111": true}})
	sched.runUptimeRewardOnce()
	if got := s.GetNode(node.ID).TotalPoints; got <= base {
		t.Fatalf("holder accrued no uptime points: got %d, want > %d", got, base)
	}
}

// TestUptimeRewardGateDisabledAwardsNormally confirms the default (Noop) gate
// leaves the reward path unchanged.
func TestUptimeRewardGateDisabledAwardsNormally(t *testing.T) {
	s := memory.NewMemory()
	defer s.Close()
	v := verification.NewVerifier([]string{"http://example.invalid"})

	node := s.RegisterNode("0x2222", types.BscFull, types.ExposedRPC, "http://example.invalid", "")
	s.RecordHeartbeat(&types.HeartbeatRecord{
		NodeID:    node.ID,
		Timestamp: time.Now().UnixMilli(),
		IsSynced:  true,
	})

	sched := New(s, v, Config{
		HeartbeatInterval: 5 * time.Minute,
		RewardInterval:    5 * time.Minute,
		SnapshotInterval:  -1,
		Disabled:          true,
	})
	defer sched.Close()

	base := s.GetNode(node.ID).TotalPoints
	// No SetHolderChecker call: holder defaults to token.Noop (gating disabled).
	sched.runUptimeRewardOnce()
	if got := s.GetNode(node.ID).TotalPoints; got <= base {
		t.Fatalf("gating disabled should award: got %d, want > %d", got, base)
	}
}
