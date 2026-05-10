package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/depinonbnb/depin/internal/store/memory"
	"github.com/depinonbnb/depin/internal/store/sqlite"
	"github.com/depinonbnb/depin/internal/verification"
)

// fakeQuorumServer returns an httptest server that always answers
// eth_blockNumber with 0x1, so HealthCheck reports OK for it.
func fakeQuorumServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))
	return srv
}

// TestShallowHealth confirms /health stays a no-deps OK regardless of the
// store / verifier state.
func TestShallowHealth(t *testing.T) {
	t.Parallel()

	router, _ := setupTestRouter("")

	req, _ := http.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status field = %q, want ok", resp["status"])
	}
}

// TestReadyHasAllSections asserts the four documented sections are present
// and the overall status reflects the dependency state. We use a fake quorum
// upstream so trusted_rpc is reachable.
func TestReadyHasAllSections(t *testing.T) {
	t.Parallel()

	upstream := fakeQuorumServer(t)
	defer upstream.Close()

	s := memory.NewMemory()
	defer s.Close()
	v := verification.NewVerifier([]string{upstream.URL})
	router := SetupRouter(s, v, "")

	req, _ := http.NewRequest("GET", "/ready", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	var resp struct {
		Status string                 `json:"status"`
		Checks map[string]interface{} `json:"checks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, name := range []string{"store", "scheduler", "trusted_rpc", "snapshot"} {
		if _, ok := resp.Checks[name]; !ok {
			t.Errorf("ready response missing %q section", name)
		}
	}
	// Store check must include latency_ms.
	storeCheck, ok := resp.Checks["store"].(map[string]interface{})
	if !ok {
		t.Fatalf("store check is not an object: %T", resp.Checks["store"])
	}
	if _, ok := storeCheck["latency_ms"]; !ok {
		t.Errorf("store check missing latency_ms")
	}
	if storeCheck["status"] != "ok" {
		t.Errorf("store status = %v, want ok", storeCheck["status"])
	}
	// Snapshot must say "never" because we just registered with no nodes.
	snapCheck, ok := resp.Checks["snapshot"].(map[string]interface{})
	if !ok {
		t.Fatalf("snapshot check is not an object: %T", resp.Checks["snapshot"])
	}
	if snapCheck["status"] != "never" {
		t.Errorf("snapshot status = %v, want 'never' (no snapshots published in test)", snapCheck["status"])
	}
}

// TestReadyStoreDown verifies that a closed SQLiteStore (db handle gone)
// surfaces as store.status=fail and an overall 503.
func TestReadyStoreDown(t *testing.T) {
	t.Parallel()

	upstream := fakeQuorumServer(t)
	defer upstream.Close()

	dsn := "file:" + t.TempDir() + "/ready.db?_pragma=busy_timeout(1000)"
	s, err := sqlite.NewSQLite(dsn)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	// Close BEFORE the request so Ping returns an error.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	v := verification.NewVerifier([]string{upstream.URL})
	router := SetupRouter(s, v, "")

	req, _ := http.NewRequest("GET", "/ready", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Status string                 `json:"status"`
		Checks map[string]interface{} `json:"checks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	storeCheck, _ := resp.Checks["store"].(map[string]interface{})
	if storeCheck["status"] != "fail" {
		t.Errorf("store status = %v, want fail", storeCheck["status"])
	}
}

// TestReadyNoQuorumEndpoints verifies that constructing the verifier with no
// trusted endpoints surfaces as trusted_rpc.status=fail and overall 503.
func TestReadyNoQuorumEndpoints(t *testing.T) {
	t.Parallel()

	s := memory.NewMemory()
	defer s.Close()
	v := verification.NewVerifier(nil)
	router := SetupRouter(s, v, "")

	req, _ := http.NewRequest("GET", "/ready", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "trusted_rpc") {
		t.Errorf("body missing trusted_rpc section: %s", body)
	}
}

// TestMetricsEndpointMounted confirms /metrics is served and contains the
// metrics package's exposition. We rely on the metrics package's own test for
// content; this is the wiring assertion.
func TestMetricsEndpointMounted(t *testing.T) {
	t.Parallel()

	router, _ := setupTestRouter("")

	req, _ := http.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "depin_") {
		t.Errorf("metrics body missing depin_ prefix; got %d bytes", w.Body.Len())
	}
}

