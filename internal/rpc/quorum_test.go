// Package rpc: quorum_test.go covers the QuorumClient added in Phase 3 per
// ADR-0009. The matrix mirrors the ADR's "Tests" requirement: all-agree,
// 2-of-3 agree, all-disagree (no quorum), and one-unreachable + two-agree.
package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/depinonbnb/depin/internal/types"
)

// fakeRPCServer returns an httptest.Server that responds to JSON-RPC calls
// with the supplied per-method answer. The bodyOverride hook lets a single
// test bend a specific endpoint's reply (e.g. to make it return a different
// hash than the others).
func fakeRPCServer(t *testing.T, blockHash string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		var result string
		switch body.Method {
		case "eth_getBlockByNumber":
			result = `{"hash":"` + blockHash + `","number":"0x1","timestamp":"0x0","parentHash":"0x0","stateRoot":"0x0","transactionsRoot":"0x0","receiptsRoot":"0x0","miner":"0x0","gasUsed":"0x0","gasLimit":"0x0"}`
		case "eth_blockNumber":
			result = `"0x1"`
		case "eth_syncing":
			result = `false`
		case "eth_getBalance":
			result = `"0x10"`
		case "net_peerCount":
			result = `"0x5"`
		default:
			http.Error(w, "method", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// blockHashChallenge builds a Challenge of the BlockHash kind for use in the
// per-test ExecuteChallenge calls below.
func blockHashChallenge() *types.Challenge {
	bn := uint64(1)
	return &types.Challenge{
		ID:            "ch-1",
		NodeID:        "node-1",
		ChallengeType: types.BlockHash,
		Params:        types.ChallengeParams{BlockNumber: &bn},
	}
}

func TestQuorum_AllAgree(t *testing.T) {
	a := fakeRPCServer(t, "0xdeadbeef")
	b := fakeRPCServer(t, "0xdeadbeef")
	c := fakeRPCServer(t, "0xdeadbeef")

	q := NewQuorum([]string{a.URL, b.URL, c.URL})
	resp := q.ExecuteChallenge(blockHashChallenge())

	if !resp.Success {
		t.Fatalf("expected success when all agree, got error: %s", resp.Error)
	}
	if !strings.EqualFold(resp.Data, "0xdeadbeef") {
		t.Errorf("expected hash 0xdeadbeef, got %q", resp.Data)
	}
}

func TestQuorum_TwoOfThreeAgree(t *testing.T) {
	a := fakeRPCServer(t, "0xdeadbeef")
	b := fakeRPCServer(t, "0xdeadbeef")
	// c disagrees — minority dissenter.
	c := fakeRPCServer(t, "0xdifferent")

	q := NewQuorum([]string{a.URL, b.URL, c.URL})
	resp := q.ExecuteChallenge(blockHashChallenge())

	if !resp.Success {
		t.Fatalf("expected success on 2-of-3, got error: %s", resp.Error)
	}
	if !strings.EqualFold(resp.Data, "0xdeadbeef") {
		t.Errorf("expected majority answer 0xdeadbeef, got %q", resp.Data)
	}
}

func TestQuorum_AllDisagreeNoMajority(t *testing.T) {
	a := fakeRPCServer(t, "0xaaaa")
	b := fakeRPCServer(t, "0xbbbb")
	c := fakeRPCServer(t, "0xcccc")

	q := NewQuorum([]string{a.URL, b.URL, c.URL})
	resp := q.ExecuteChallenge(blockHashChallenge())

	if resp.Success {
		t.Fatalf("expected failure with no majority")
	}
	if resp.Error != QuorumNoMajority {
		t.Errorf("expected error %q, got %q", QuorumNoMajority, resp.Error)
	}
}

func TestQuorum_OneUnreachableTwoAgree(t *testing.T) {
	a := fakeRPCServer(t, "0xdeadbeef")
	b := fakeRPCServer(t, "0xdeadbeef")
	// c is closed before the call — a reachable but immediately-failing
	// listener stand-in. Since httptest can't easily simulate this, we
	// build the QuorumClient with a known-bad URL.
	q := NewQuorum([]string{a.URL, b.URL, "http://127.0.0.1:1"})
	resp := q.ExecuteChallenge(blockHashChallenge())

	if !resp.Success {
		t.Fatalf("expected success when 2 of 3 reachable agree, got error: %s", resp.Error)
	}
	if !strings.EqualFold(resp.Data, "0xdeadbeef") {
		t.Errorf("expected majority answer, got %q", resp.Data)
	}
}

func TestQuorum_TwoUnreachable(t *testing.T) {
	a := fakeRPCServer(t, "0xdeadbeef")

	q := NewQuorum([]string{a.URL, "http://127.0.0.1:1", "http://127.0.0.1:2"})
	resp := q.ExecuteChallenge(blockHashChallenge())

	if resp.Success {
		t.Fatalf("expected no-quorum when only 1 of 3 responded")
	}
	if resp.Error != QuorumNoMajority {
		t.Errorf("expected error %q, got %q", QuorumNoMajority, resp.Error)
	}
}

func TestQuorum_SingleEndpointDegradesGracefully(t *testing.T) {
	a := fakeRPCServer(t, "0xsolo")

	q := NewQuorum([]string{a.URL})
	resp := q.ExecuteChallenge(blockHashChallenge())

	if !resp.Success {
		t.Fatalf("single endpoint should still satisfy 1-of-1 majority, got error: %s", resp.Error)
	}
	if !strings.EqualFold(resp.Data, "0xsolo") {
		t.Errorf("expected 0xsolo, got %q", resp.Data)
	}
}

func TestQuorum_EmptyEndpointsAlwaysNoQuorum(t *testing.T) {
	q := NewQuorum(nil)
	resp := q.ExecuteChallenge(blockHashChallenge())
	if resp.Success {
		t.Fatalf("expected no-quorum for empty endpoint list")
	}
	if resp.Error != QuorumNoMajority {
		t.Errorf("expected error %q, got %q", QuorumNoMajority, resp.Error)
	}
}

// TestQuorum_HexCaseInsensitiveBlockHash verifies that block-hash answers are
// compared case-insensitively (per ADR-0009 notes). 0xDEADBEEF and 0xdeadbeef
// must canonicalize equal.
func TestQuorum_HexCaseInsensitiveBlockHash(t *testing.T) {
	a := fakeRPCServer(t, "0xDEADBEEF")
	b := fakeRPCServer(t, "0xdeadbeef")
	c := fakeRPCServer(t, "0xDeadBeef")

	q := NewQuorum([]string{a.URL, b.URL, c.URL})
	resp := q.ExecuteChallenge(blockHashChallenge())

	if !resp.Success {
		t.Fatalf("expected success on case-mixed agreement, got error: %s", resp.Error)
	}
}

// TestQuorum_FanOutInParallel sanity-checks that ExecuteChallenge actually
// fires concurrent requests by counting concurrent in-flight handlers.
func TestQuorum_FanOutInParallel(t *testing.T) {
	var inflight, peak int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&inflight, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
				break
			}
		}
		// Hold long enough that sibling goroutines can join us in flight.
		// A bare for-loop here gets optimized away to a no-op.
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"hash":"0xaaa","number":"0x1","timestamp":"0x0","parentHash":"0x0","stateRoot":"0x0","transactionsRoot":"0x0","receiptsRoot":"0x0","miner":"0x0","gasUsed":"0x0","gasLimit":"0x0"}}`))
	}))
	defer srv.Close()

	q := NewQuorum([]string{srv.URL, srv.URL, srv.URL})
	_ = q.ExecuteChallenge(blockHashChallenge())

	if atomic.LoadInt32(&peak) < 2 {
		t.Errorf("expected fan-out to produce >=2 concurrent in-flight requests, peak=%d", peak)
	}
}
