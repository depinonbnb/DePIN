package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/types"
	"github.com/depinonbnb/depin/internal/verification"
	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func setupTestRouter(adminKey string) (*gin.Engine, *store.Store) {
	s := store.NewStore()
	v := verification.NewVerifier("https://bsc-dataseed1.binance.org")
	router := SetupRouter(s, v, adminKey)
	return router, s
}

func TestHealthEndpoint(t *testing.T) {
	router, _ := setupTestRouter("")

	req, _ := http.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var response map[string]string
	json.Unmarshal(w.Body.Bytes(), &response)

	if response["status"] != "ok" {
		t.Errorf("expected status 'ok', got '%s'", response["status"])
	}
}

func TestGetNodeNotFound(t *testing.T) {
	router, _ := setupTestRouter("")

	req, _ := http.NewRequest("GET", "/api/nodes/nonexistent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestGetNodeStats(t *testing.T) {
	router, s := setupTestRouter("")

	// Create a node directly in store
	node := s.RegisterNode("0xtest", types.BscFull, types.LocalProver, "", "")

	req, _ := http.NewRequest("GET", "/api/nodes/"+node.ID+"/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var stats types.NodeStats
	json.Unmarshal(w.Body.Bytes(), &stats)

	if stats.NodeID != node.ID {
		t.Errorf("node ID mismatch: got %s", stats.NodeID)
	}

	if stats.TotalPoints != 50 {
		t.Errorf("expected 50 points for BSC Full, got %d", stats.TotalPoints)
	}
}

func TestGetLeaderboard(t *testing.T) {
	router, s := setupTestRouter("")

	// Create nodes with different points
	s.RegisterNode("0x1", types.BscArchive, types.LocalProver, "", "") // 100 pts
	s.RegisterNode("0x2", types.BscFull, types.LocalProver, "", "")    // 50 pts
	s.RegisterNode("0x3", types.OpbnbFast, types.LocalProver, "", "")  // 30 pts

	req, _ := http.NewRequest("GET", "/api/leaderboard", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var entries []map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &entries)

	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}

	// Check ranking order (highest points first)
	if entries[0]["total_points"].(float64) != 100 {
		t.Error("first entry should have 100 points (BSC Archive)")
	}
	if entries[1]["total_points"].(float64) != 50 {
		t.Error("second entry should have 50 points (BSC Full)")
	}
	if entries[2]["total_points"].(float64) != 30 {
		t.Error("third entry should have 30 points (opBNB Fast)")
	}

	// Check ranks
	if entries[0]["rank"].(float64) != 1 {
		t.Error("first entry should have rank 1")
	}
}

func TestGetLeaderboardExcludesBanned(t *testing.T) {
	router, s := setupTestRouter("")

	node1 := s.RegisterNode("0x1", types.BscArchive, types.LocalProver, "", "")
	s.RegisterNode("0x2", types.BscFull, types.LocalProver, "", "")

	// Ban first node
	s.SetNodeCheatStatus(node1.ID, types.StatusBanned, "cheating")

	req, _ := http.NewRequest("GET", "/api/leaderboard", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var entries []map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &entries)

	if len(entries) != 1 {
		t.Errorf("expected 1 entry (banned excluded), got %d", len(entries))
	}
}

func TestGetNetworkStats(t *testing.T) {
	router, s := setupTestRouter("")

	s.RegisterNode("0x1", types.BscFull, types.LocalProver, "", "")
	s.RegisterNode("0x2", types.BscArchive, types.ExposedRPC, "http://test", "")
	s.RegisterNode("0x3", types.OpbnbFull, types.LocalProver, "", "")

	req, _ := http.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var stats map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &stats)

	if stats["total_nodes"].(float64) != 3 {
		t.Errorf("expected 3 total nodes, got %v", stats["total_nodes"])
	}

	byType := stats["by_type"].(map[string]interface{})
	if byType["bsc-full"].(float64) != 1 {
		t.Error("expected 1 bsc-full node")
	}

	byMethod := stats["by_method"].(map[string]interface{})
	if byMethod["local-prover"].(float64) != 2 {
		t.Error("expected 2 local-prover nodes")
	}
}

func TestGetWalletStats(t *testing.T) {
	router, s := setupTestRouter("")

	wallet := "0xmywallet"
	s.RegisterNode(wallet, types.BscArchive, types.LocalProver, "", "") // 100 pts
	s.RegisterNode(wallet, types.BscFull, types.LocalProver, "", "")    // 50 pts

	req, _ := http.NewRequest("GET", "/api/wallet/"+wallet+"/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var stats types.WalletStats
	json.Unmarshal(w.Body.Bytes(), &stats)

	if stats.TotalPoints != 150 {
		t.Errorf("expected 150 total points, got %d", stats.TotalPoints)
	}

	if stats.TotalNodes != 2 {
		t.Errorf("expected 2 nodes, got %d", stats.TotalNodes)
	}
}

func TestGetWalletStatsNotFound(t *testing.T) {
	router, _ := setupTestRouter("")

	req, _ := http.NewRequest("GET", "/api/wallet/0xnonexistent/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestGetNodesByWallet(t *testing.T) {
	router, s := setupTestRouter("")

	wallet := "0xmywallet"
	s.RegisterNode(wallet, types.BscFull, types.LocalProver, "", "")
	s.RegisterNode(wallet, types.BscArchive, types.LocalProver, "", "")

	req, _ := http.NewRequest("GET", "/api/nodes/wallet/"+wallet, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var nodes []types.NodeRegistration
	json.Unmarshal(w.Body.Bytes(), &nodes)

	if len(nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(nodes))
	}

	// Auth token should not be exposed
	for _, node := range nodes {
		if node.AuthToken != "" {
			t.Error("auth token should be hidden")
		}
	}
}

// Admin endpoint tests
func TestAdminEndpointRequiresAuth(t *testing.T) {
	router, _ := setupTestRouter("secretkey")

	req, _ := http.NewRequest("GET", "/api/admin/flagged", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401 without auth, got %d", w.Code)
	}
}

func TestAdminEndpointWithWrongKey(t *testing.T) {
	router, _ := setupTestRouter("secretkey")

	req, _ := http.NewRequest("GET", "/api/admin/flagged", nil)
	req.Header.Set("Authorization", "Bearer wrongkey")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401 with wrong key, got %d", w.Code)
	}
}

func TestAdminEndpointWithCorrectKey(t *testing.T) {
	router, _ := setupTestRouter("secretkey")

	req, _ := http.NewRequest("GET", "/api/admin/flagged", nil)
	req.Header.Set("Authorization", "Bearer secretkey")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 with correct key, got %d", w.Code)
	}
}

func TestAdminGetFlaggedNodes(t *testing.T) {
	router, s := setupTestRouter("key")

	s.RegisterNode("0x1", types.BscFull, types.LocalProver, "", "")
	node2 := s.RegisterNode("0x2", types.BscFull, types.LocalProver, "", "")
	s.SetNodeCheatStatus(node2.ID, types.StatusWarning, "suspicious")

	req, _ := http.NewRequest("GET", "/api/admin/flagged", nil)
	req.Header.Set("Authorization", "Bearer key")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var response map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &response)

	if response["count"].(float64) != 1 {
		t.Errorf("expected 1 flagged node, got %v", response["count"])
	}
}

func TestAdminReviewNode(t *testing.T) {
	router, s := setupTestRouter("key")

	node := s.RegisterNode("0x1", types.BscFull, types.LocalProver, "", "")
	s.SetNodeCheatStatus(node.ID, types.StatusWarning, "suspicious")

	// Clear the node
	body := []byte(`{"action": "clear", "reason": "false positive"}`)
	req, _ := http.NewRequest("POST", "/api/admin/review/"+node.ID, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Verify node is cleared
	updated := s.GetNode(node.ID)
	if updated.CheatStatus != types.StatusClean {
		t.Errorf("expected clean status, got %s", updated.CheatStatus)
	}
}

func TestAdminReviewNodeBan(t *testing.T) {
	router, s := setupTestRouter("key")

	node := s.RegisterNode("0x1", types.BscFull, types.LocalProver, "", "")

	body := []byte(`{"action": "ban", "reason": "confirmed cheating"}`)
	req, _ := http.NewRequest("POST", "/api/admin/review/"+node.ID, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	updated := s.GetNode(node.ID)
	if updated.CheatStatus != types.StatusBanned {
		t.Errorf("expected banned status, got %s", updated.CheatStatus)
	}
	if updated.IsActive {
		t.Error("banned node should be deactivated")
	}
}

func TestAdminReviewInvalidAction(t *testing.T) {
	router, s := setupTestRouter("key")

	node := s.RegisterNode("0x1", types.BscFull, types.LocalProver, "", "")

	body := []byte(`{"action": "invalid"}`)
	req, _ := http.NewRequest("POST", "/api/admin/review/"+node.ID, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for invalid action, got %d", w.Code)
	}
}

func TestTestCreateNode(t *testing.T) {
	router, _ := setupTestRouter("key")

	body := []byte(`{
		"wallet_address": "0xtest123",
		"node_type": "bsc-full",
		"verification_method": "local-prover"
	}`)

	req, _ := http.NewRequest("POST", "/api/admin/test/create-node", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var response map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &response)

	if response["success"] != true {
		t.Error("expected success: true")
	}

	if response["node_id"] == "" {
		t.Error("expected node_id to be set")
	}

	if response["total_points"].(float64) != 50 {
		t.Errorf("expected 50 points for bsc-full, got %v", response["total_points"])
	}
}

func TestCORSHeaders(t *testing.T) {
	router, _ := setupTestRouter("")

	req, _ := http.NewRequest("OPTIONS", "/api/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected status 204 for OPTIONS, got %d", w.Code)
	}

	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("CORS header not set correctly")
	}
}
