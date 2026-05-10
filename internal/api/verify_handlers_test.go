package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/depinonbnb/depin/internal/store/memory"
	"github.com/depinonbnb/depin/internal/types"
	"github.com/depinonbnb/depin/internal/verification"
)

// ==================
// VerifyNode
// ==================

func TestVerifyNode_NotFound(t *testing.T) {
	router, _ := setupTestRouter("")

	req, _ := http.NewRequest("POST", "/api/verify/nonexistent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestVerifyNode_WrongMethod_LocalProver(t *testing.T) {
	router, s := setupTestRouter("")

	node := s.RegisterNode("0xwallet", types.BscFull, types.LocalProver, "", "")

	req, _ := http.NewRequest("POST", "/api/verify/"+node.ID, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-exposed-rpc node, got %d", w.Code)
	}
}

func TestVerifyNode_Success(t *testing.T) {
	mock := newMockRPCServer(t)
	defer mock.Close()

	s := memory.NewMemory()
	defer s.Close()
	v := verification.NewVerifier([]string{mock.URL})
	router := SetupRouter(s, v, "")

	node := s.RegisterNode("0xwallet", types.BscFull, types.ExposedRPC, mock.URL, "")

	req, _ := http.NewRequest("POST", "/api/verify/"+node.ID, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// ==================
// CheckHeartbeat
// ==================

func TestCheckHeartbeat_NodeNotFound(t *testing.T) {
	router, _ := setupTestRouter("")

	req, _ := http.NewRequest("GET", "/api/verify/nonexistent/heartbeat", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestCheckHeartbeat_WrongMethod_LocalProver(t *testing.T) {
	router, s := setupTestRouter("")

	node := s.RegisterNode("0xwallet", types.BscFull, types.LocalProver, "", "")

	req, _ := http.NewRequest("GET", "/api/verify/"+node.ID+"/heartbeat", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for local-prover node, got %d", w.Code)
	}
}

func TestCheckHeartbeat_NodeUnreachable(t *testing.T) {
	s := memory.NewMemory()
	defer s.Close()
	v := verification.NewVerifier([]string{"http://localhost:1"})
	router := SetupRouter(s, v, "")

	// Register with a dead endpoint
	node := s.RegisterNode("0xwallet", types.BscFull, types.ExposedRPC, "http://localhost:1", "")

	req, _ := http.NewRequest("GET", "/api/verify/"+node.ID+"/heartbeat", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for unreachable node, got %d", w.Code)
	}
}

func TestCheckHeartbeat_Success(t *testing.T) {
	mock := newMockRPCServer(t)
	defer mock.Close()

	s := memory.NewMemory()
	defer s.Close()
	v := verification.NewVerifier([]string{mock.URL})
	router := SetupRouter(s, v, "")

	node := s.RegisterNode("0xwallet", types.BscFull, types.ExposedRPC, mock.URL, "")

	req, _ := http.NewRequest("GET", "/api/verify/"+node.ID+"/heartbeat", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}

	// Heartbeat should now be recorded in the store
	hbs := s.GetHeartbeats(node.ID, 0)
	if len(hbs) == 0 {
		t.Error("heartbeat should be recorded after successful check")
	}
}
