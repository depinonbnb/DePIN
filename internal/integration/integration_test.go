// Package integration_test wires the full HTTP router with a SQLite-backed
// store and exercises the canonical operator flow end-to-end:
//
//	create-node (admin/test) -> get node -> request challenge ->
//	leaderboard / network stats / health
//
// The live exposed-RPC verification path (POST /api/verify/:nodeId) is
// intentionally NOT exercised here because it would call out to a real BSC
// node. Coverage of that handler lives in internal/verification/verifier_test.go
// (unit) and in the unit tests under internal/api (for the HTTP wiring).
package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/depinonbnb/depin/internal/api"
	"github.com/depinonbnb/depin/internal/store/sqlite"
	"github.com/depinonbnb/depin/internal/verification"
)

// fakeRPCHandler is the smallest JSON-RPC server that satisfies the
// production rpc.Client. It speaks just enough to back a /challenges/request
// round-trip — block-hash, block-by-number and sync-status — which between
// them cover every challenge type the generator emits.
//
// We deliberately do NOT pretend to be a real BSC node; we only return
// well-formed responses so CreateChallenge can store an "expected answer".
// The integration test never submits a challenge response, so answer values
// don't need to be realistic.
func fakeRPCHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		var req struct {
			Method string `json:"method"`
			ID     int    `json:"id"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "parse body", http.StatusBadRequest)
			return
		}

		// Hand-roll the JSON to keep the test free of further struct deps.
		// Fields match the rpc.Client's expectations.
		var result string
		switch req.Method {
		case "eth_blockNumber":
			result = `"0x10"`
		case "eth_syncing":
			result = `false`
		case "eth_getBlockByNumber":
			result = `{"hash":"0xabc","number":"0x10","timestamp":"0x0","parentHash":"0x0","stateRoot":"0x0","transactionsRoot":"0x0","receiptsRoot":"0x0","miner":"0x0","gasUsed":"0x0","gasLimit":"0x0"}`
		case "eth_getBalance":
			result = `"0x0"`
		case "net_peerCount":
			result = `"0x5"`
		default:
			// Unknown method: surface as an RPC error so the verifier marks the
			// challenge as failed-to-create. Tests that depend on this path
			// won't exercise unknown methods in the first place.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":0,"error":{"code":-32601,"message":"method not found"}}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`))
	})
}

// setupServer spins up the production router against a fresh SQLite-backed
// store and a fake trusted-RPC server. Returns the API base URL and a teardown
// closure that the test must defer.
func setupServer(t *testing.T) (string, string) {
	t.Helper()

	// Fake trusted RPC. The real code calls this on every CreateChallenge.
	rpcSrv := httptest.NewServer(fakeRPCHandler(t))
	t.Cleanup(rpcSrv.Close)

	// Fresh on-disk SQLite (temp dir; cleaned up by t.TempDir on close).
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "depin.db") +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_txlock=immediate"

	store, err := sqlite.NewSQLite(dsn)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	verifier := verification.NewVerifier([]string{rpcSrv.URL})

	const adminKey = "test-admin-key"
	router := api.SetupRouter(store, verifier, adminKey)

	apiSrv := httptest.NewServer(router)
	t.Cleanup(apiSrv.Close)

	return apiSrv.URL, adminKey
}

// httpJSON is a tiny test-only HTTP helper that handles JSON marshal/unmarshal
// boilerplate. headers may be nil.
func httpJSON(t *testing.T, method, url string, headers map[string]string, body interface{}, out interface{}) int {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		bs, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		bodyReader = bytes.NewReader(bs)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
			t.Fatalf("decode response: %v", err)
		}
	}
	return resp.StatusCode
}

// TestEndToEndOperatorFlow walks through the canonical happy path:
//  1. Create a test node via the admin endpoint (skips signature)
//  2. GET that node, ensuring auth_token is redacted and node is active
//  3. Request a challenge for it
//  4. GET the leaderboard and expect the test node to appear
//  5. GET aggregate network stats
//  6. GET /health
func TestEndToEndOperatorFlow(t *testing.T) {
	baseURL, adminKey := setupServer(t)
	authHeader := map[string]string{"Authorization": "Bearer " + adminKey}

	// 1. Admin creates a test node (no signature dance).
	type createReq struct {
		WalletAddress      string `json:"wallet_address"`
		NodeType           string `json:"node_type"`
		VerificationMethod string `json:"verification_method"`
		RPCEndpoint        string `json:"rpc_endpoint"`
	}
	type createResp struct {
		Success     bool   `json:"success"`
		NodeID      string `json:"node_id"`
		Wallet      string `json:"wallet"`
		NodeType    string `json:"node_type"`
		TotalPoints uint64 `json:"total_points"`
		Message     string `json:"message"`
	}
	body := createReq{
		WalletAddress:      "0xabcd1234",
		NodeType:           "bsc-archive",
		VerificationMethod: "local-prover",
	}
	var created createResp
	code := httpJSON(t, http.MethodPost, baseURL+"/api/admin/test/create-node", authHeader, body, &created)
	if code != http.StatusOK {
		t.Fatalf("create-node: expected 200, got %d", code)
	}
	if !created.Success {
		t.Errorf("create-node: success was false")
	}
	if created.NodeID == "" {
		t.Fatalf("create-node: node_id empty")
	}
	if created.NodeType != "bsc-archive" {
		t.Errorf("create-node: expected node_type=bsc-archive got %q", created.NodeType)
	}
	if created.TotalPoints != 100 {
		// BscArchive registration bonus per types.NodeType.RegistrationBonus.
		t.Errorf("create-node: expected total_points=100, got %d", created.TotalPoints)
	}

	// 2. Public GET /api/nodes/:id -- node is visible, auth_token redacted.
	type publicNode struct {
		ID                 string `json:"id"`
		WalletAddress      string `json:"wallet_address"`
		NodeType           string `json:"node_type"`
		VerificationMethod string `json:"verification_method"`
		AuthToken          string `json:"auth_token"`
		IsActive           bool   `json:"is_active"`
	}
	var node publicNode
	code = httpJSON(t, http.MethodGet, baseURL+"/api/nodes/"+created.NodeID, nil, nil, &node)
	if code != http.StatusOK {
		t.Fatalf("get node: expected 200, got %d", code)
	}
	if node.ID != created.NodeID {
		t.Errorf("get node: id mismatch %q vs %q", node.ID, created.NodeID)
	}
	if node.AuthToken != "" {
		t.Errorf("get node: auth_token should be redacted, got %q", node.AuthToken)
	}
	if !node.IsActive {
		t.Errorf("get node: expected is_active=true")
	}
	if node.VerificationMethod != "local-prover" {
		t.Errorf("get node: expected verification_method=local-prover, got %q", node.VerificationMethod)
	}

	// 3. GET /api/challenges/request -- backed by our fake RPC.
	type challengePublic struct {
		ID            string `json:"id"`
		ChallengeType string `json:"challenge_type"`
		ExpiresAt     int64  `json:"expires_at"`
	}
	type challengeResp struct {
		Challenge  challengePublic `json:"challenge"`
		ServerTime int64           `json:"server_time"`
	}
	var ch challengeResp
	code = httpJSON(t, http.MethodGet, baseURL+"/api/challenges/request?nodeId="+created.NodeID, nil, nil, &ch)
	if code != http.StatusOK {
		t.Fatalf("request challenge: expected 200, got %d", code)
	}
	if ch.Challenge.ID == "" {
		t.Errorf("request challenge: empty challenge id")
	}
	switch ch.Challenge.ChallengeType {
	case "block-hash", "block-data", "sync-status", "state-balance":
		// known type
	default:
		t.Errorf("request challenge: unexpected type %q", ch.Challenge.ChallengeType)
	}
	if ch.Challenge.ExpiresAt <= time.Now().UnixMilli() {
		t.Errorf("request challenge: expires_at must be in the future, got %d", ch.Challenge.ExpiresAt)
	}

	// 4. /api/leaderboard contains our newly-registered node.
	var leaderboard []map[string]interface{}
	code = httpJSON(t, http.MethodGet, baseURL+"/api/leaderboard", nil, nil, &leaderboard)
	if code != http.StatusOK {
		t.Fatalf("leaderboard: expected 200, got %d", code)
	}
	found := false
	for _, entry := range leaderboard {
		if id, _ := entry["node_id"].(string); id == created.NodeID {
			found = true
			if pts, _ := entry["total_points"].(float64); pts != 100 {
				t.Errorf("leaderboard: expected total_points=100, got %v", pts)
			}
		}
	}
	if !found {
		t.Errorf("leaderboard: created node %s missing from results (entries=%d)", created.NodeID, len(leaderboard))
	}

	// 5. /api/stats reflects exactly one node.
	var stats map[string]interface{}
	code = httpJSON(t, http.MethodGet, baseURL+"/api/stats", nil, nil, &stats)
	if code != http.StatusOK {
		t.Fatalf("stats: expected 200, got %d", code)
	}
	if total, _ := stats["total_nodes"].(float64); total != 1 {
		t.Errorf("stats: expected total_nodes=1, got %v", stats["total_nodes"])
	}
	byType, _ := stats["by_type"].(map[string]interface{})
	if byType == nil {
		t.Fatalf("stats: by_type missing")
	}
	if v, _ := byType["bsc-archive"].(float64); v != 1 {
		t.Errorf("stats: expected by_type[bsc-archive]=1, got %v", v)
	}

	// 6. /health -- liveness ping.
	var health map[string]string
	code = httpJSON(t, http.MethodGet, baseURL+"/health", nil, nil, &health)
	if code != http.StatusOK {
		t.Fatalf("health: expected 200, got %d", code)
	}
	if health["status"] != "ok" {
		t.Errorf("health: expected status=ok, got %q", health["status"])
	}
}
