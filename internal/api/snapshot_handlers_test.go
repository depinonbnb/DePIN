package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/depinonbnb/depin/internal/types"
)

// ==================
// PublishSnapshot
// ==================

func TestPublishSnapshot_RequiresAdmin(t *testing.T) {
	router, _ := setupTestRouter("admin-key")

	body := []byte(`{"cycle_id":"c1"}`)
	req, _ := http.NewRequest("POST", "/api/admin/snapshot/publish", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", w.Code)
	}
}

func TestPublishSnapshot_MissingCycleID(t *testing.T) {
	router, _ := setupTestRouter("admin-key")

	body := []byte(`{}`)
	req, _ := http.NewRequest("POST", "/api/admin/snapshot/publish", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing cycle_id, got %d", w.Code)
	}
}

func TestPublishSnapshot_ZeroEarners(t *testing.T) {
	// No nodes in store → ErrNoEarners → 200 with published=false.
	router, _ := setupTestRouter("admin-key")

	body := []byte(`{"cycle_id":"empty-cycle"}`)
	req, _ := http.NewRequest("POST", "/api/admin/snapshot/publish", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for zero earners, got %d (body=%s)", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["published"] != false {
		t.Errorf("expected published=false for empty cycle, got %v", resp["published"])
	}
	if resp["node_count"].(float64) != 0 {
		t.Errorf("expected node_count=0, got %v", resp["node_count"])
	}
}

func TestPublishSnapshot_Success(t *testing.T) {
	router, s := setupTestRouter("admin-key")

	// Register a node with points so BuildCycle finds at least one earner.
	s.RegisterNode("0x1234567890abcdef1234567890abcdef12345678", types.BscFull, types.LocalProver, "", "")

	body := []byte(`{"cycle_id":"cycle-001"}`)
	req, _ := http.NewRequest("POST", "/api/admin/snapshot/publish", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["published"] != true {
		t.Errorf("expected published=true, got %v", resp["published"])
	}
	if resp["root"] == nil || resp["root"] == "" {
		t.Error("expected non-empty root hex in response")
	}
}

// ==================
// GetLatestSnapshot
// ==================

func TestGetLatestSnapshot_NoneYet(t *testing.T) {
	router, _ := setupTestRouter("")

	req, _ := http.NewRequest("GET", "/api/snapshots/latest", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when no snapshots published, got %d", w.Code)
	}
}

func TestGetLatestSnapshot_Success(t *testing.T) {
	router, s := setupTestRouter("admin-key")

	// Publish via PublishSnapshot first.
	s.RegisterNode("0x1234567890abcdef1234567890abcdef12345678", types.BscFull, types.LocalProver, "", "")
	publishBody := []byte(`{"cycle_id":"cycle-latest"}`)
	pubReq, _ := http.NewRequest("POST", "/api/admin/snapshot/publish", bytes.NewBuffer(publishBody))
	pubReq.Header.Set("Authorization", "Bearer admin-key")
	pubReq.Header.Set("Content-Type", "application/json")
	pubW := httptest.NewRecorder()
	router.ServeHTTP(pubW, pubReq)
	if pubW.Code != http.StatusOK {
		t.Fatalf("publish failed: %d %s", pubW.Code, pubW.Body.String())
	}

	req, _ := http.NewRequest("GET", "/api/snapshots/latest", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}

	var snap types.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatalf("response not a valid Snapshot: %v", err)
	}
	if snap.CycleID != "cycle-latest" {
		t.Errorf("unexpected cycle_id: %s", snap.CycleID)
	}
}

// ==================
// GetSnapshotByCycle
// ==================

func TestGetSnapshotByCycle_NotFound(t *testing.T) {
	router, _ := setupTestRouter("")

	req, _ := http.NewRequest("GET", "/api/snapshots/does-not-exist", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestGetSnapshotByCycle_Success(t *testing.T) {
	router, s := setupTestRouter("admin-key")

	s.RegisterNode("0x1234567890abcdef1234567890abcdef12345678", types.BscFull, types.LocalProver, "", "")
	publishBody := []byte(`{"cycle_id":"cycle-byid"}`)
	pubReq, _ := http.NewRequest("POST", "/api/admin/snapshot/publish", bytes.NewBuffer(publishBody))
	pubReq.Header.Set("Authorization", "Bearer admin-key")
	pubReq.Header.Set("Content-Type", "application/json")
	pubW := httptest.NewRecorder()
	router.ServeHTTP(pubW, pubReq)
	if pubW.Code != http.StatusOK {
		t.Fatalf("publish failed: %d %s", pubW.Code, pubW.Body.String())
	}

	req, _ := http.NewRequest("GET", "/api/snapshots/cycle-byid", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// ==================
// GetWalletClaim
// ==================

func TestGetWalletClaim_WalletNotInCycle(t *testing.T) {
	router, s := setupTestRouter("admin-key")

	s.RegisterNode("0x1234567890abcdef1234567890abcdef12345678", types.BscFull, types.LocalProver, "", "")
	publishBody := []byte(`{"cycle_id":"cycle-claim"}`)
	pubReq, _ := http.NewRequest("POST", "/api/admin/snapshot/publish", bytes.NewBuffer(publishBody))
	pubReq.Header.Set("Authorization", "Bearer admin-key")
	pubReq.Header.Set("Content-Type", "application/json")
	pubW := httptest.NewRecorder()
	router.ServeHTTP(pubW, pubReq)
	if pubW.Code != http.StatusOK {
		t.Fatalf("publish failed: %s", pubW.Body.String())
	}

	req, _ := http.NewRequest("GET", "/api/wallet/0xnotearner/claim/cycle-claim", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for wallet with no claim, got %d", w.Code)
	}
}

func TestGetWalletClaim_Success(t *testing.T) {
	router, s := setupTestRouter("admin-key")

	earner := "0x1234567890abcdef1234567890abcdef12345678"
	s.RegisterNode(earner, types.BscFull, types.LocalProver, "", "")

	publishBody := []byte(`{"cycle_id":"cycle-claim2"}`)
	pubReq, _ := http.NewRequest("POST", "/api/admin/snapshot/publish", bytes.NewBuffer(publishBody))
	pubReq.Header.Set("Authorization", "Bearer admin-key")
	pubReq.Header.Set("Content-Type", "application/json")
	pubW := httptest.NewRecorder()
	router.ServeHTTP(pubW, pubReq)
	if pubW.Code != http.StatusOK {
		t.Fatalf("publish failed: %s", pubW.Body.String())
	}

	req, _ := http.NewRequest("GET", "/api/wallet/"+earner+"/claim/cycle-claim2", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["proof"] == nil {
		t.Error("expected proof field in claim response")
	}
	if resp["amount"] == nil {
		t.Error("expected amount field in claim response")
	}
}

// ==================
// GetWalletClaimLatest
// ==================

func TestGetWalletClaimLatest_NoSnapshots(t *testing.T) {
	router, _ := setupTestRouter("")

	req, _ := http.NewRequest("GET", "/api/wallet/0xwallet/claim/latest", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when no snapshots, got %d", w.Code)
	}
}

func TestGetWalletClaimLatest_WalletNotInLatest(t *testing.T) {
	router, s := setupTestRouter("admin-key")

	s.RegisterNode("0x1234567890abcdef1234567890abcdef12345678", types.BscFull, types.LocalProver, "", "")
	publishBody := []byte(`{"cycle_id":"cycle-latest2"}`)
	pubReq, _ := http.NewRequest("POST", "/api/admin/snapshot/publish", bytes.NewBuffer(publishBody))
	pubReq.Header.Set("Authorization", "Bearer admin-key")
	pubReq.Header.Set("Content-Type", "application/json")
	pubW := httptest.NewRecorder()
	router.ServeHTTP(pubW, pubReq)
	if pubW.Code != http.StatusOK {
		t.Fatalf("publish failed: %s", pubW.Body.String())
	}

	req, _ := http.NewRequest("GET", "/api/wallet/0xnoearner/claim/latest", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for wallet not in latest cycle, got %d", w.Code)
	}
}
