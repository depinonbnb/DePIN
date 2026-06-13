package token

import (
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
)

// hex32 left-pads a big.Int to a 32-byte (64 hex char) word, the ABI encoding
// of a uint256 return value.
func hex32(n *big.Int) string {
	h := n.Text(16)
	for len(h) < 64 {
		h = "0" + h
	}
	return "0x" + h
}

// mockEthCallServer returns a JSON-RPC server that answers every eth_call with
// the given balance encoded as a uint256.
func mockEthCallServer(balance *big.Int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  hex32(balance),
		})
	}))
}

// TestNewOnChainReadPath exercises the real ethclient + ABI balanceOf decode
// path built by New(), against a mock JSON-RPC server — the integration that
// can't be reached through NewWithBalanceFunc.
func TestNewOnChainReadPath(t *testing.T) {
	min := ScaleTokens(big.NewInt(1_000_000), 18)
	wallet := "0x1111111111111111111111111111111111111111"

	tests := []struct {
		name      string
		balance   *big.Int
		wantAllow bool
	}{
		{"above minimum", ScaleTokens(big.NewInt(2_000_000), 18), true},
		{"exactly minimum", ScaleTokens(big.NewInt(1_000_000), 18), true},
		{"below minimum", ScaleTokens(big.NewInt(999_999), 18), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := mockEthCallServer(tt.balance)
			defer srv.Close()

			c, err := New(Config{
				RPCURL:       srv.URL,
				TokenAddress: "0x000000000000000000000000000000000000dEaD",
				MinBalance:   min,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if !c.Enabled() {
				t.Fatal("configured checker should report enabled")
			}

			c.Refresh(context.Background(), []string{wallet})

			if got := c.Allow(wallet); got != tt.wantAllow {
				t.Errorf("Allow(%s) with balance %s = %v, want %v", wallet, tt.balance, got, tt.wantAllow)
			}
			if h, known := c.IsHolder(wallet); !known || h != tt.wantAllow {
				t.Errorf("IsHolder = (%v,%v), want (%v,true)", h, known, tt.wantAllow)
			}
		})
	}
}
