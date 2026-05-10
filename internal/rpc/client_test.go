package rpc

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/depinonbnb/depin/internal/types"
)

// mockServer returns a test server that answers basic JSON-RPC methods.
// authRequired, when non-empty, asserts the request carries that Bearer token.
func mockServer(t *testing.T, authRequired string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authRequired != "" {
			got := r.Header.Get("Authorization")
			want := "Bearer " + authRequired
			if got != want {
				t.Errorf("auth header: got %q, want %q", got, want)
			}
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			ID     int    `json:"id"`
		}
		json.Unmarshal(body, &req)

		var result interface{}
		switch req.Method {
		case "eth_blockNumber":
			result = "0x64" // 100
		case "eth_syncing":
			result = false
		case "net_peerCount":
			result = "0x8" // 8
		case "eth_getBlockByNumber":
			result = map[string]string{
				"hash":             "0xaaa",
				"number":           "0x64",
				"timestamp":        "0x0",
				"parentHash":       "0xbbb",
				"stateRoot":        "0xccc",
				"transactionsRoot": "0xddd",
				"receiptsRoot":     "0xeee",
				"miner":            "0x0000000000000000000000000000000000000000",
				"gasUsed":          "0x0",
				"gasLimit":         "0x0",
			}
		case "eth_getBalance":
			result = "0xde0b6b3a7640000"
		default:
			result = nil
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		})
	}))
}

func TestGetBlockNumber_Success(t *testing.T) {
	srv := mockServer(t, "")
	defer srv.Close()

	c := NewClient(srv.URL, "")
	num, _, err := c.GetBlockNumber()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if num != 100 {
		t.Errorf("expected block number 100, got %d", num)
	}
}

func TestGetBlockNumber_ServerError(t *testing.T) {
	// Server that returns an RPC error object
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"error":   map[string]string{"message": "method not allowed"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, _, err := c.GetBlockNumber()
	if err == nil {
		t.Error("expected error for RPC error response")
	}
}

func TestGetSyncStatus_Synced(t *testing.T) {
	srv := mockServer(t, "")
	defer srv.Close()

	c := NewClient(srv.URL, "")
	synced, _, err := c.GetSyncStatus()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !synced {
		t.Error("expected synced=true when eth_syncing returns false")
	}
}

func TestGetSyncStatus_Syncing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// When syncing, result is an object, not false
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]string{"startingBlock": "0x0", "currentBlock": "0x50"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	synced, _, err := c.GetSyncStatus()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if synced {
		t.Error("expected synced=false when eth_syncing returns an object")
	}
}

func TestGetPeerCount_Success(t *testing.T) {
	srv := mockServer(t, "")
	defer srv.Close()

	c := NewClient(srv.URL, "")
	count, _, err := c.GetPeerCount()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 8 {
		t.Errorf("expected 8 peers, got %d", count)
	}
}

func TestGetBalance_WithBlockNumber(t *testing.T) {
	srv := mockServer(t, "")
	defer srv.Close()

	c := NewClient(srv.URL, "")
	blockNum := uint64(100)
	bal, _, err := c.GetBalance("0xdeadbeef", &blockNum)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bal == "" {
		t.Error("expected non-empty balance")
	}
}

func TestGetBalance_Latest(t *testing.T) {
	srv := mockServer(t, "")
	defer srv.Close()

	c := NewClient(srv.URL, "")
	bal, _, err := c.GetBalance("0xdeadbeef", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bal == "" {
		t.Error("expected non-empty balance")
	}
}

func TestAuthToken_SentInHeader(t *testing.T) {
	// mockServer asserts the Authorization header is set to the token we pass
	srv := mockServer(t, "mytoken")
	defer srv.Close()

	c := NewClient(srv.URL, "mytoken")
	_, _, err := c.GetBlockNumber()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetBlockByNumber_Success(t *testing.T) {
	srv := mockServer(t, "")
	defer srv.Close()

	c := NewClient(srv.URL, "")
	block, _, err := c.GetBlockByNumber(100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if block.Hash != "0xaaa" {
		t.Errorf("expected hash 0xaaa, got %s", block.Hash)
	}
}

func TestExecuteChallenge_BlockHash(t *testing.T) {
	srv := mockServer(t, "")
	defer srv.Close()

	c := NewClient(srv.URL, "")
	blockNum := uint64(100)
	ch := &types.Challenge{
		ChallengeType: types.BlockHash,
		Params:        types.ChallengeParams{BlockNumber: &blockNum},
	}
	resp := c.ExecuteChallenge(ch)
	if !resp.Success {
		t.Errorf("expected success, got error: %s", resp.Error)
	}
	if resp.Data == "" {
		t.Error("expected non-empty block hash")
	}
}

func TestExecuteChallenge_BlockData(t *testing.T) {
	srv := mockServer(t, "")
	defer srv.Close()

	c := NewClient(srv.URL, "")
	blockNum := uint64(100)
	ch := &types.Challenge{
		ChallengeType: types.BlockData,
		Params:        types.ChallengeParams{BlockNumber: &blockNum},
	}
	resp := c.ExecuteChallenge(ch)
	if !resp.Success {
		t.Errorf("expected success, got error: %s", resp.Error)
	}
}

func TestExecuteChallenge_StateBalance(t *testing.T) {
	srv := mockServer(t, "")
	defer srv.Close()

	c := NewClient(srv.URL, "")
	ch := &types.Challenge{
		ChallengeType: types.StateBalance,
		Params:        types.ChallengeParams{Address: "0xdeadbeef"},
	}
	resp := c.ExecuteChallenge(ch)
	if !resp.Success {
		t.Errorf("expected success, got error: %s", resp.Error)
	}
}

func TestExecuteChallenge_SyncStatus(t *testing.T) {
	srv := mockServer(t, "")
	defer srv.Close()

	c := NewClient(srv.URL, "")
	ch := &types.Challenge{
		ChallengeType: types.SyncStatus,
		Params:        types.ChallengeParams{},
	}
	resp := c.ExecuteChallenge(ch)
	if !resp.Success {
		t.Errorf("expected success, got error: %s", resp.Error)
	}
}

func TestExecuteChallenge_UnknownType(t *testing.T) {
	srv := mockServer(t, "")
	defer srv.Close()

	c := NewClient(srv.URL, "")
	ch := &types.Challenge{
		ChallengeType: "unknown-type",
		Params:        types.ChallengeParams{},
	}
	resp := c.ExecuteChallenge(ch)
	if resp.Success {
		t.Error("expected failure for unknown challenge type")
	}
	if resp.Error != "unknown challenge type" {
		t.Errorf("unexpected error message: %s", resp.Error)
	}
}

func TestClient_Unreachable(t *testing.T) {
	c := NewClient("http://localhost:1", "")
	_, _, err := c.GetBlockNumber()
	if err == nil {
		t.Error("expected error for unreachable server")
	}
}
