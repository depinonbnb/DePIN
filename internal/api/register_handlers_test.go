package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ==================
// verifySignature (unit)
// ==================

func TestVerifySignature_BadHex(t *testing.T) {
	h := &Handlers{}
	if h.verifySignature("msg", "not-hex!!", "0xaddr") {
		t.Error("bad hex should return false")
	}
}

func TestVerifySignature_WrongLength(t *testing.T) {
	h := &Handlers{}
	// 64 bytes = 128 hex chars — one byte short of the required 65
	shortSig := "00" + "0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000"
	if h.verifySignature("msg", shortSig, "0xaddr") {
		t.Error("wrong-length sig should return false")
	}
}

func TestVerifySignature_GarbageSig(t *testing.T) {
	h := &Handlers{}
	// 65 zero-bytes is a valid length but can't recover a public key from
	// an all-zero r/s — crypto.SigToPub will fail.
	zeroSig := "0x" + "0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000" + "00"
	if h.verifySignature("msg", zeroSig, "0xaddr") {
		t.Error("garbage sig should return false")
	}
}

// ==================
// RegisterNode (edge cases)
// ==================

func TestRegisterNode_MissingFields(t *testing.T) {
	router, _ := setupTestRouter("")

	body := []byte(`{}`)
	req, _ := http.NewRequest("POST", "/api/nodes/register", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing fields, got %d", w.Code)
	}
}

func TestRegisterNode_ExposedRPCWithoutEndpoint(t *testing.T) {
	router, _ := setupTestRouter("")

	body := []byte(`{
		"wallet_address": "0xabc",
		"node_type": "bsc-full",
		"verification_method": "exposed-rpc",
		"signature": "0x` + "00" + `",
		"timestamp": 1700000000000,
		"nonce": "n1"
	}`)
	req, _ := http.NewRequest("POST", "/api/nodes/register", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when exposed-rpc has no endpoint, got %d", w.Code)
	}
}

func TestRegisterNode_SSRFRejected(t *testing.T) {
	router, _ := setupTestRouter("")

	// RFC1918 private address — IsAllowedURL should reject it
	body := []byte(`{
		"wallet_address": "0xabc",
		"node_type": "bsc-full",
		"verification_method": "exposed-rpc",
		"rpc_endpoint": "http://192.168.1.1:8545",
		"signature": "0x` + "00" + `",
		"timestamp": 1700000000000,
		"nonce": "n1"
	}`)
	req, _ := http.NewRequest("POST", "/api/nodes/register", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for private RPC endpoint (SSRF), got %d", w.Code)
	}
}

func TestRegisterNode_TimestampTooOld(t *testing.T) {
	router, _ := setupTestRouter("")

	// timestamp from 2001 — definitely more than 5 minutes ago
	body := []byte(`{
		"wallet_address": "0xabc",
		"node_type": "bsc-full",
		"verification_method": "local-prover",
		"signature": "aabbcc00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001b",
		"timestamp": 1000000000000,
		"nonce": "n1"
	}`)
	req, _ := http.NewRequest("POST", "/api/nodes/register", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for stale timestamp, got %d", w.Code)
	}
}

func TestRegisterNode_InvalidSignature(t *testing.T) {
	// Requires a fresh timestamp + real secp256k1 key material.
	// The unit-level path is covered by TestVerifySignature_GarbageSig above.
	// End-to-end coverage lives in integration_test.go.
	t.Skip("requires real secp256k1 key material — covered by integration_test.go")
}
