package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/depinonbnb/depin/internal/types"
)

// ─── GET /admin/flagged ───────────────────────────────────────────────────────

func TestGetFlaggedNodes_EmptyStore(t *testing.T) {
	router, _ := setupTestRouter("admin-key")

	req, _ := http.NewRequest(http.MethodGet, "/api/admin/flagged", nil)
	req.Header.Set("Authorization", "Bearer admin-key")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}

	var resp struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.Count != 0 {
		t.Errorf("expected count=0 for empty store, got %d", resp.Count)
	}
}

func TestGetFlaggedNodes_DoesNotExposeAuthToken(t *testing.T) {
	router, s := setupTestRouter("admin-key")

	node := s.RegisterNode("0xflagged", types.BscFull, types.LocalProver, "", "")
	s.SetNodeCheatStatus(node.ID, types.StatusWarning, "suspicious")

	req, _ := http.NewRequest(http.MethodGet, "/api/admin/flagged", nil)
	req.Header.Set("Authorization", "Bearer admin-key")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	// auth token must be scrubbed
	if node.AuthToken != "" && bytes.Contains(w.Body.Bytes(), []byte(node.AuthToken)) {
		t.Error("response must not include auth_token")
	}
}

func TestGetFlaggedNodes_RejectsMissingAdminKey(t *testing.T) {
	router, _ := setupTestRouter("secret")

	req, _ := http.NewRequest(http.MethodGet, "/api/admin/flagged", nil)
	// no Authorization header
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Errorf("unauthenticated request should not succeed, got %d", w.Code)
	}
}

// ─── POST /admin/review/:nodeId ───────────────────────────────────────────────

func TestReviewNode_Clear(t *testing.T) {
	router, s := setupTestRouter("admin-key")

	node := s.RegisterNode("0xclear", types.BscFull, types.LocalProver, "", "")
	s.SetNodeCheatStatus(node.ID, types.StatusWarning, "initial flag")

	body := []byte(`{"action":"clear","reason":"false positive"}`)
	req, _ := http.NewRequest(http.MethodPost, "/api/admin/review/"+node.ID, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	updated := s.GetNode(node.ID)
	if updated == nil {
		t.Fatal("node disappeared after review")
	}
	if updated.CheatStatus != types.StatusClean {
		t.Errorf("expected StatusClean, got %s", updated.CheatStatus)
	}
}

func TestReviewNode_Warn(t *testing.T) {
	router, s := setupTestRouter("admin-key")

	node := s.RegisterNode("0xwarn", types.BscFull, types.LocalProver, "", "")

	body := []byte(`{"action":"warn","reason":"first strike"}`)
	req, _ := http.NewRequest(http.MethodPost, "/api/admin/review/"+node.ID, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	updated := s.GetNode(node.ID)
	if updated.CheatStatus != types.StatusWarning {
		t.Errorf("expected StatusWarning, got %s", updated.CheatStatus)
	}
}

func TestReviewNode_Ban(t *testing.T) {
	router, s := setupTestRouter("admin-key")

	node := s.RegisterNode("0xban", types.BscFull, types.LocalProver, "", "")

	body := []byte(`{"action":"ban","reason":"cheating confirmed"}`)
	req, _ := http.NewRequest(http.MethodPost, "/api/admin/review/"+node.ID, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	updated := s.GetNode(node.ID)
	if updated.CheatStatus != types.StatusBanned {
		t.Errorf("expected StatusBanned, got %s", updated.CheatStatus)
	}
}

func TestReviewNode_ResponseBodyContainsNodeID(t *testing.T) {
	router, s := setupTestRouter("admin-key")

	node := s.RegisterNode("0xcheck", types.BscFull, types.LocalProver, "", "")

	body := []byte(`{"action":"warn","reason":"test"}`)
	req, _ := http.NewRequest(http.MethodPost, "/api/admin/review/"+node.ID, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["node_id"] != node.ID {
		t.Errorf("response node_id %v does not match %s", resp["node_id"], node.ID)
	}
}

func TestReviewNode_MissingActionField(t *testing.T) {
	router, s := setupTestRouter("admin-key")

	node := s.RegisterNode("0xmissing", types.BscFull, types.LocalProver, "", "")

	// action is absent — binding should fail
	body := []byte(`{"reason":"no action provided"}`)
	req, _ := http.NewRequest(http.MethodPost, "/api/admin/review/"+node.ID, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing action, got %d", w.Code)
	}
}

func TestReviewNode_ResponseIncludesValidActionsOnBadAction(t *testing.T) {
	// already covered by admin_handlers_branch_test.go, but verify error message
	// also references the valid set so operators can self-correct.
	router, s := setupTestRouter("admin-key")

	node := s.RegisterNode("0xbadact", types.BscFull, types.LocalProver, "", "")
	body := []byte(`{"action":"delete","reason":"?"}`)
	req, _ := http.NewRequest(http.MethodPost, "/api/admin/review/"+node.ID, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	errMsg := strings.ToLower(resp["error"].(string))
	for _, kw := range []string{"clear", "warn", "ban"} {
		if !strings.Contains(errMsg, kw) {
			t.Errorf("error message missing valid action %q: %s", kw, errMsg)
		}
	}
}

// ─── POST /admin/test/create-node ────────────────────────────────────────────

func TestTestCreateNode_HappyPath(t *testing.T) {
	router, _ := setupTestRouter("admin-key")

	body := []byte(`{
		"wallet_address": "0xTestWallet",
		"node_type": "bsc-full",
		"verification_method": "local-prover"
	}`)
	req, _ := http.NewRequest(http.MethodPost, "/api/admin/test/create-node", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["success"] != true {
		t.Errorf("expected success=true, got %v", resp["success"])
	}
	if resp["node_id"] == nil || resp["node_id"] == "" {
		t.Error("response must include a non-empty node_id")
	}
	// wallet address is lowercased in RegisterNode
	if resp["wallet"] != "0xtestwallet" {
		t.Errorf("expected lowercased wallet, got %v", resp["wallet"])
	}
}

func TestTestCreateNode_MissingWallet(t *testing.T) {
	router, _ := setupTestRouter("admin-key")

	body := []byte(`{"node_type":"bsc-full","verification_method":"local-prover"}`)
	req, _ := http.NewRequest(http.MethodPost, "/api/admin/test/create-node", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing wallet_address, got %d", w.Code)
	}
}

func TestTestCreateNode_MissingNodeType(t *testing.T) {
	router, _ := setupTestRouter("admin-key")

	body := []byte(`{"wallet_address":"0xTest","verification_method":"local-prover"}`)
	req, _ := http.NewRequest(http.MethodPost, "/api/admin/test/create-node", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing node_type, got %d", w.Code)
	}
}

func TestTestCreateNode_RequiresAdminKey(t *testing.T) {
	router, _ := setupTestRouter("secret")

	body := []byte(`{"wallet_address":"0xA","node_type":"bsc-full","verification_method":"local-prover"}`)
	req, _ := http.NewRequest(http.MethodPost, "/api/admin/test/create-node", bytes.NewBuffer(body))
	// wrong key
	req.Header.Set("Authorization", "Bearer wrong-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Errorf("wrong admin key should not succeed, got %d", w.Code)
	}
}

// ─── abs helper ──────────────────────────────────────────────────────────────

func TestAbs(t *testing.T) {
	cases := []struct {
		in   int64
		want int64
	}{
		{0, 0},
		{5, 5},
		{-5, 5},
		{-1, 1},
	}
	for _, c := range cases {
		if got := abs(c.in); got != c.want {
			t.Errorf("abs(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
