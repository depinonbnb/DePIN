package scheduler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/depinonbnb/depin/internal/store/memory"
	"github.com/depinonbnb/depin/internal/types"
	"github.com/depinonbnb/depin/internal/verification"
)

// canonical mock answers — both trustedRPC and the node's RPC point at the
// same mock server so VerifyExposedRPC always sees matching answers.
const (
	mockBlockHashHex     = "0x1111111111111111111111111111111111111111111111111111111111111111"
	mockParentHashHex    = "0x2222222222222222222222222222222222222222222222222222222222222222"
	mockStateRootHex     = "0x3333333333333333333333333333333333333333333333333333333333333333"
	mockReceiptsRootHex  = "0x4444444444444444444444444444444444444444444444444444444444444444"
	mockTransactionsRoot = "0x5555555555555555555555555555555555555555555555555555555555555555"
	mockBalanceHex       = "0xde0b6b3a7640000" // 1 BNB in wei
)

// jsonRpcReq mirrors the rpc package's request shape — kept private to avoid
// importing the unexported rpc internal type.
type jsonRpcReq struct {
	Method string `json:"method"`
	ID     int    `json:"id"`
}

// jsonRpcResp is the canonical JSON-RPC response envelope used by the mock.
type jsonRpcResp struct {
	Jsonrpc string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Result  interface{} `json:"result"`
}

// newMockRPCServer returns an httptest server that handles every JSON-RPC
// method the verifier and rpc client care about. callsTotal counts requests
// served so a test can assert it actually saw traffic.
func newMockRPCServer(callsTotal *atomic.Int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if callsTotal != nil {
			callsTotal.Add(1)
		}
		body, _ := io.ReadAll(r.Body)
		var req jsonRpcReq
		_ = json.Unmarshal(body, &req)

		w.Header().Set("Content-Type", "application/json")
		var result interface{}
		switch req.Method {
		case "eth_blockNumber":
			result = "0x100"
		case "eth_syncing":
			result = false
		case "net_peerCount":
			result = "0x10"
		case "eth_getBlockByNumber":
			result = map[string]string{
				"hash":             mockBlockHashHex,
				"number":           "0x100",
				"timestamp":        "0x0",
				"parentHash":       mockParentHashHex,
				"stateRoot":        mockStateRootHex,
				"transactionsRoot": mockTransactionsRoot,
				"receiptsRoot":     mockReceiptsRootHex,
				"miner":            "0x0000000000000000000000000000000000000000",
				"gasUsed":          "0x0",
				"gasLimit":         "0x0",
			}
		case "eth_getBalance":
			result = mockBalanceHex
		default:
			// Unknown method — return null so anything random still gets a
			// response and the rpc client doesn't hang.
			result = nil
		}

		_ = json.NewEncoder(w).Encode(jsonRpcResp{
			Jsonrpc: "2.0",
			ID:      req.ID,
			Result:  result,
		})
	}))
}

func TestSchedulerRunsAllThreeTickers(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	mock := newMockRPCServer(&calls)
	defer mock.Close()

	s := memory.NewMemory()
	defer s.Close()

	// Register two exposed-RPC nodes pointing at the mock so heartbeat and
	// challenge dispatch both produce results. Use BscFull (always-due
	// because LastVerifiedAt starts at 0).
	n1 := s.RegisterNode("0x1111", types.BscFull, types.ExposedRPC, mock.URL, "")
	n2 := s.RegisterNode("0x2222", types.BscArchive, types.ExposedRPC, mock.URL, "")

	// Also register a local-prover node — the challenge dispatcher must
	// skip it (no server-side dispatch for local-prover nodes).
	nLocal := s.RegisterNode("0x3333", types.BscFull, types.LocalProver, "", "")

	v := verification.NewVerifier([]string{mock.URL})

	// Phase 3: the challenge.Generator's *rand.Rand is now mutex-protected,
	// so we can safely run with multiple workers without -race noise. Use a
	// non-trivial pool size to actually exercise the concurrent path.
	//
	// Phase 5: SnapshotInterval=-1 disables the snapshot cron for this test
	// so we don't accidentally exercise reward.BuildCycle here (covered by
	// its own test).
	sched := New(s, v, Config{
		HeartbeatInterval:      30 * time.Millisecond,
		ChallengeCheckInterval: 30 * time.Millisecond,
		RewardInterval:         30 * time.Millisecond,
		SnapshotInterval:       -1,
		RPCWorkers:             4,
	})

	sched.Start()
	// Let several cycles run.
	time.Sleep(300 * time.Millisecond)
	if err := sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Heartbeat assertions: both exposed-RPC nodes should have at least one
	// recorded heartbeat. Local-prover node should have none.
	hb1 := s.GetHeartbeats(n1.ID, 0)
	hb2 := s.GetHeartbeats(n2.ID, 0)
	hbLocal := s.GetHeartbeats(nLocal.ID, 0)
	if len(hb1) == 0 {
		t.Errorf("n1: expected at least one heartbeat, got 0")
	}
	if len(hb2) == 0 {
		t.Errorf("n2: expected at least one heartbeat, got 0")
	}
	if len(hbLocal) != 0 {
		t.Errorf("local-prover node: expected 0 heartbeats, got %d", len(hbLocal))
	}

	// Challenge assertions: both exposed-RPC nodes should have at least one
	// VerificationResult recorded (either passed or failed — we just assert
	// the dispatcher actually ran).
	updated1 := s.GetNode(n1.ID)
	updated2 := s.GetNode(n2.ID)
	if updated1.LastVerifiedAt == 0 {
		t.Errorf("n1: LastVerifiedAt was never updated")
	}
	if updated2.LastVerifiedAt == 0 {
		t.Errorf("n2: LastVerifiedAt was never updated")
	}
	if (updated1.TotalChallengesPassed + updated1.TotalChallengesFailed) == 0 {
		t.Errorf("n1: no verification result recorded")
	}

	// Uptime assertions: at least one of the active exposed-RPC nodes should
	// have received uptime points beyond its registration bonus. We pick n1
	// (BscFull, registration bonus = 50 from types.RegistrationBonus).
	if updated1.TotalPoints <= n1.NodeType.RegistrationBonus() {
		t.Errorf("n1: expected uptime points beyond registration bonus %d, got %d",
			n1.NodeType.RegistrationBonus(), updated1.TotalPoints)
	}

	if calls.Load() == 0 {
		t.Errorf("mock RPC saw no traffic — scheduler appears to not have run")
	}
}

func TestSchedulerDisabledSkipsStart(t *testing.T) {
	t.Parallel()

	mock := newMockRPCServer(nil)
	defer mock.Close()

	s := memory.NewMemory()
	defer s.Close()

	s.RegisterNode("0xfeed", types.BscFull, types.ExposedRPC, mock.URL, "")
	v := verification.NewVerifier([]string{mock.URL})

	sched := New(s, v, Config{
		HeartbeatInterval:      10 * time.Millisecond,
		ChallengeCheckInterval: 10 * time.Millisecond,
		RewardInterval:         10 * time.Millisecond,
		SnapshotInterval:       -1,
		RPCWorkers:             5,
		Disabled:               true,
	})

	sched.Start() // no-op when Disabled
	time.Sleep(80 * time.Millisecond)
	if err := sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// No tickers ran, so no heartbeats should exist.
	nodes := s.GetAllActiveNodes()
	if len(nodes) != 1 {
		t.Fatalf("expected 1 active node, got %d", len(nodes))
	}
	hb := s.GetHeartbeats(nodes[0].ID, 0)
	if len(hb) != 0 {
		t.Errorf("expected 0 heartbeats when scheduler is disabled, got %d", len(hb))
	}
}

func TestSchedulerSkipsBannedNodes(t *testing.T) {
	t.Parallel()

	mock := newMockRPCServer(nil)
	defer mock.Close()

	s := memory.NewMemory()
	defer s.Close()

	banned := s.RegisterNode("0xbanned", types.BscFull, types.ExposedRPC, mock.URL, "")
	s.SetNodeCheatStatus(banned.ID, types.StatusBanned, "test ban")

	v := verification.NewVerifier([]string{mock.URL})
	sched := New(s, v, Config{
		HeartbeatInterval:      20 * time.Millisecond,
		ChallengeCheckInterval: 20 * time.Millisecond,
		RewardInterval:         20 * time.Millisecond,
		SnapshotInterval:       -1,
		RPCWorkers:             5,
	})
	sched.Start()
	time.Sleep(150 * time.Millisecond)
	if err := sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Banned + IsActive=false ⇒ GetAllActiveNodes won't return it, and even
	// if it did the scheduler skips banned. Either way, no heartbeats.
	hb := s.GetHeartbeats(banned.ID, 0)
	if len(hb) != 0 {
		t.Errorf("banned node should never receive heartbeats; got %d", len(hb))
	}
}

func TestSchedulerCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	s := memory.NewMemory()
	defer s.Close()
	v := verification.NewVerifier([]string{"http://nowhere.invalid"})

	sched := New(s, v, Config{Disabled: true})
	if err := sched.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := sched.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestSchedulerSnapshotCronPublishes drives the snapshot cron at a millisecond
// cadence with a single earner in the store, asserts a snapshot row was
// persisted, and asserts the published cycle id matches the documented format.
func TestSchedulerSnapshotCronPublishes(t *testing.T) {
	t.Parallel()

	s := memory.NewMemory()
	defer s.Close()

	// One node with non-zero TotalPoints (the registration bonus is enough).
	s.RegisterNode("0xabcdef0000000000000000000000000000000000", types.BscFull, types.LocalProver, "", "")

	v := verification.NewVerifier([]string{"http://nowhere.invalid"})
	sched := New(s, v, Config{
		HeartbeatInterval:      24 * time.Hour, // effectively disabled
		ChallengeCheckInterval: 24 * time.Hour, // effectively disabled
		RewardInterval:         24 * time.Hour, // effectively disabled
		SnapshotInterval:       40 * time.Millisecond,
		RPCWorkers:             1,
	})
	sched.Start()
	time.Sleep(220 * time.Millisecond)
	if err := sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	snap, err := s.GetLatestSnapshot()
	if err != nil {
		t.Fatalf("GetLatestSnapshot: %v", err)
	}
	if snap == nil {
		t.Fatalf("expected at least one published snapshot, got none")
	}
	if !strings.HasPrefix(snap.CycleID, "cycle-") {
		t.Errorf("unexpected cycle id format: %q (want cycle-<timestamp>)", snap.CycleID)
	}
	if snap.NodeCount == 0 {
		t.Errorf("snapshot recorded zero leaves; expected at least one earner")
	}
}

// TestSchedulerSnapshotDisabled asserts that SnapshotInterval=-1 prevents the
// snapshot loop from running even when the rest of the scheduler ticks.
func TestSchedulerSnapshotDisabled(t *testing.T) {
	t.Parallel()

	s := memory.NewMemory()
	defer s.Close()
	s.RegisterNode("0xfeedface00000000000000000000000000000000", types.BscFull, types.LocalProver, "", "")

	v := verification.NewVerifier([]string{"http://nowhere.invalid"})
	sched := New(s, v, Config{
		HeartbeatInterval:      30 * time.Millisecond,
		ChallengeCheckInterval: 30 * time.Millisecond,
		RewardInterval:         30 * time.Millisecond,
		SnapshotInterval:       -1, // disabled
		RPCWorkers:             1,
	})
	sched.Start()
	time.Sleep(120 * time.Millisecond)
	if err := sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	snap, err := s.GetLatestSnapshot()
	if err != nil {
		t.Fatalf("GetLatestSnapshot: %v", err)
	}
	if snap != nil {
		t.Errorf("snapshot loop ran despite SnapshotInterval=-1: got cycle %q", snap.CycleID)
	}
}

func TestApplyDefaults(t *testing.T) {
	t.Parallel()

	got := applyDefaults(Config{})
	if got.HeartbeatInterval != defaultHeartbeatInterval {
		t.Errorf("HeartbeatInterval default = %v, want %v", got.HeartbeatInterval, defaultHeartbeatInterval)
	}
	if got.ChallengeCheckInterval != defaultChallengeCheckInterval {
		t.Errorf("ChallengeCheckInterval default = %v, want %v", got.ChallengeCheckInterval, defaultChallengeCheckInterval)
	}
	if got.RewardInterval != defaultRewardInterval {
		t.Errorf("RewardInterval default = %v, want %v", got.RewardInterval, defaultRewardInterval)
	}
	if got.SnapshotInterval != defaultSnapshotInterval {
		t.Errorf("SnapshotInterval default = %v, want %v", got.SnapshotInterval, defaultSnapshotInterval)
	}
	if got.RPCWorkers != defaultRPCWorkers {
		t.Errorf("RPCWorkers default = %d, want %d", got.RPCWorkers, defaultRPCWorkers)
	}

	// Non-zero values should not be overwritten.
	custom := Config{
		HeartbeatInterval:      1 * time.Second,
		ChallengeCheckInterval: 2 * time.Second,
		RewardInterval:         3 * time.Second,
		SnapshotInterval:       4 * time.Second,
		RPCWorkers:             7,
	}
	gotCustom := applyDefaults(custom)
	if gotCustom != custom {
		t.Errorf("applyDefaults mutated explicit config: got %+v want %+v", gotCustom, custom)
	}

	// Negative SnapshotInterval is the "explicitly disabled" sentinel and
	// must round-trip through applyDefaults unchanged. This is the
	// SnapshotInterval contract that Start() relies on to skip launching
	// the snapshot loop.
	disabled := applyDefaults(Config{SnapshotInterval: -1})
	if disabled.SnapshotInterval != -1 {
		t.Errorf("negative SnapshotInterval mutated: got %v, want -1", disabled.SnapshotInterval)
	}
}
