// Package api: admin_handlers_branch_test.go covers the error branches of
// ReviewNode that handlers_test.go's happy-path tests don't exercise:
//
//   - 404 when the targeted node ID does not exist (SetNodeCheatStatus
//     returns false).
//   - 400 when the action is not in {"clear", "warn", "ban"}.
//
// Both are short, deterministic, and re-use the existing setupTestRouter
// helper from handlers_test.go.
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

// TestReviewNode_404OnUnknownNode posts a perfectly-formed review request
// to a node ID that doesn't exist in the store. The expected outcome is a
// 404 (the SetNodeCheatStatus call returns false) — NOT a 200 echoing the
// invented status.
func TestReviewNode_404OnUnknownNode(t *testing.T) {
	router, _ := setupTestRouter("admin-key")

	body := []byte(`{"action": "clear", "reason": "test"}`)
	req, _ := http.NewRequest(http.MethodPost, "/api/admin/review/non-existent-id", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown node, got %d (body=%s)", w.Code, w.Body.String())
	}

	// The error body should at least contain "not found" so operators have
	// some visibility. Re-binding to a generic map keeps the test resilient
	// to future field additions.
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response body not JSON: %v", err)
	}
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(strings.ToLower(errMsg), "not found") {
		t.Errorf("expected error to mention 'not found', got %q", errMsg)
	}
}

// TestReviewNode_400OnInvalidAction posts a body whose action is outside
// the allowed enum. The handler must reject with 400 and the body must
// list the valid actions so admins discover the right shape.
func TestReviewNode_400OnInvalidAction(t *testing.T) {
	router, s := setupTestRouter("admin-key")

	// Register a real node so this test only exercises the action-validation
	// branch — not also the not-found one.
	node := s.RegisterNode("0xreviewer", types.BscFull, types.LocalProver, "", "")

	body := []byte(`{"action": "banhammer", "reason": "rude username"}`)
	req, _ := http.NewRequest(http.MethodPost, "/api/admin/review/"+node.ID, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid action, got %d (body=%s)", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response body not JSON: %v", err)
	}
	errMsg, _ := resp["error"].(string)
	low := strings.ToLower(errMsg)
	// The message must reference the valid actions so admins can self-correct.
	// admin_handlers.go currently emits "invalid action - use clear, warn, or ban".
	for _, kw := range []string{"clear", "warn", "ban"} {
		if !strings.Contains(low, kw) {
			t.Errorf("expected error to list valid action %q, got %q", kw, errMsg)
		}
	}

	// Cross-check: the node's status must NOT have been mutated by the
	// rejected request.
	got := s.GetNode(node.ID)
	if got == nil {
		t.Fatal("node disappeared during failed review")
	}
	if got.CheatStatus != types.StatusClean {
		t.Errorf("rejected review should not change status; got %s", got.CheatStatus)
	}
}
