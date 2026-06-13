package token

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestNoopAllowsEveryone(t *testing.T) {
	var c Checker = Noop{}
	if c.Enabled() {
		t.Fatal("noop should report disabled")
	}
	if !c.Allow("0xabc") {
		t.Fatal("noop should allow every wallet")
	}
	if h, known := c.IsHolder("0xabc"); !h || !known {
		t.Fatalf("noop IsHolder = (%v,%v), want (true,true)", h, known)
	}
}

func TestNewDisabledWithoutAddress(t *testing.T) {
	c, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Enabled() {
		t.Fatal("empty token address should disable gating")
	}
}

func TestNewRejectsNonPositiveMinBalance(t *testing.T) {
	if _, err := New(Config{TokenAddress: "0xtoken", MinBalance: big.NewInt(0)}); err == nil {
		t.Fatal("expected error for non-positive MinBalance when gating enabled")
	}
}

func TestAllowGatesOnThreshold(t *testing.T) {
	min := ScaleTokens(big.NewInt(1_000_000), 18)
	holderAddr := "0x1111111111111111111111111111111111111111"
	poorAddr := "0x2222222222222222222222222222222222222222"
	balances := map[common.Address]*big.Int{
		common.HexToAddress(holderAddr): ScaleTokens(big.NewInt(2_000_000), 18),
		common.HexToAddress(poorAddr):   ScaleTokens(big.NewInt(500_000), 18),
	}
	c := NewWithBalanceFunc(Config{MinBalance: min, FailOpen: false},
		func(_ context.Context, w common.Address) (*big.Int, error) {
			if b, ok := balances[w]; ok {
				return b, nil
			}
			return big.NewInt(0), nil
		})

	c.Refresh(context.Background(), []string{holderAddr, poorAddr})

	if !c.Allow(holderAddr) {
		t.Error("wallet above threshold should be allowed")
	}
	if c.Allow(poorAddr) {
		t.Error("wallet below threshold should be blocked")
	}
	if h, known := c.IsHolder(holderAddr); !known || !h {
		t.Errorf("holder IsHolder = (%v,%v), want (true,true)", h, known)
	}
	if h, known := c.IsHolder(poorAddr); !known || h {
		t.Errorf("non-holder IsHolder = (%v,%v), want (false,true)", h, known)
	}
}

func TestExactThresholdIsHolder(t *testing.T) {
	min := ScaleTokens(big.NewInt(1_000_000), 18)
	c := NewWithBalanceFunc(Config{MinBalance: min},
		func(context.Context, common.Address) (*big.Int, error) {
			return ScaleTokens(big.NewInt(1_000_000), 18), nil // exactly the minimum
		})
	w := "0x3333333333333333333333333333333333333333"
	c.Refresh(context.Background(), []string{w})
	if !c.Allow(w) {
		t.Error("exactly-minimum balance should be allowed (>= comparison)")
	}
}

func TestAllowFailOpenPolicyWhenUnknown(t *testing.T) {
	min := ScaleTokens(big.NewInt(1_000_000), 18)
	mk := func(failOpen bool) Checker {
		return NewWithBalanceFunc(Config{MinBalance: min, FailOpen: failOpen},
			func(context.Context, common.Address) (*big.Int, error) {
				return nil, context.DeadlineExceeded // never resolves
			})
	}
	// No successful Refresh => wallet stays unknown.
	if !mk(true).Allow("0xabc") {
		t.Error("fail-open: unknown wallet should be allowed")
	}
	if mk(false).Allow("0xabc") {
		t.Error("fail-closed: unknown wallet should be blocked")
	}
}
