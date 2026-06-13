// Package token gates DePIN point awards on the operator's on-chain balance of
// the program token. A wallet must hold at least the configured minimum (e.g.
// 1,000,000 tokens) to accrue uptime or challenge points; the scheduler and API
// consult a Checker before crediting points every cycle.
//
// Gating is disabled by default: when the token address is unset, New returns a
// Noop checker that treats every wallet as eligible, so existing behavior is
// unchanged until an operator opts in by configuring TOKEN_CONTRACT_ADDRESS.
package token

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Checker decides whether a wallet may accrue points.
type Checker interface {
	// Enabled reports whether gating is active. When false, every award path
	// behaves exactly as it did before token gating existed.
	Enabled() bool
	// Allow reports whether wallet should receive points this cycle. It encodes
	// the fail-open policy: disabled -> true, known holder -> true, known
	// non-holder -> false, unknown -> the configured fail-open value.
	Allow(wallet string) bool
	// IsHolder returns the raw cached verdict for display (holds_token). known
	// is false when the wallet has not been resolved yet.
	IsHolder(wallet string) (holder bool, known bool)
	// Refresh resolves balances for wallets whose cache entry is missing or
	// older than the TTL and updates the cache. Resolution errors are left
	// unresolved (the wallet stays "unknown" and Allow applies fail-open).
	Refresh(ctx context.Context, wallets []string)
}

// Noop is the disabled checker. Gating is off and every wallet is eligible.
type Noop struct{}

func (Noop) Enabled() bool                     { return false }
func (Noop) Allow(string) bool                 { return true }
func (Noop) IsHolder(string) (bool, bool)      { return true, true }
func (Noop) Refresh(context.Context, []string) {}

// BalanceFunc returns the raw token balance (smallest unit) of wallet.
type BalanceFunc func(ctx context.Context, wallet common.Address) (*big.Int, error)

// Config configures the on-chain checker built by New.
type Config struct {
	RPCURL       string   // BSC mainnet RPC endpoint
	TokenAddress string   // ERC-20 contract address; empty => gating disabled
	MinBalance   *big.Int // minimum balance in smallest units (already scaled by decimals)
	CacheTTL     time.Duration
	FailOpen     bool // when a balance cannot be resolved, award anyway
}

// Minimal ERC-20 ABI: just balanceOf(address) -> uint256.
const erc20BalanceOfABI = `[{"constant":true,"inputs":[{"name":"account","type":"address"}],"name":"balanceOf","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"}]`

// New builds a Checker from cfg. With an empty TokenAddress it returns Noop so
// gating stays disabled. Otherwise it dials the RPC and returns an on-chain
// checker backed by ERC-20 balanceOf.
func New(cfg Config) (Checker, error) {
	if strings.TrimSpace(cfg.TokenAddress) == "" {
		return Noop{}, nil
	}
	if cfg.MinBalance == nil || cfg.MinBalance.Sign() <= 0 {
		return nil, errors.New("token: MinBalance must be positive when gating is enabled")
	}
	client, err := ethclient.Dial(cfg.RPCURL)
	if err != nil {
		return nil, fmt.Errorf("token: dial %q: %w", cfg.RPCURL, err)
	}
	parsed, err := abi.JSON(strings.NewReader(erc20BalanceOfABI))
	if err != nil {
		return nil, fmt.Errorf("token: parse abi: %w", err)
	}
	tokenAddr := common.HexToAddress(cfg.TokenAddress)

	fn := func(ctx context.Context, wallet common.Address) (*big.Int, error) {
		data, err := parsed.Pack("balanceOf", wallet)
		if err != nil {
			return nil, err
		}
		out, err := client.CallContract(ctx, ethereum.CallMsg{To: &tokenAddr, Data: data}, nil)
		if err != nil {
			return nil, err
		}
		vals, err := parsed.Unpack("balanceOf", out)
		if err != nil {
			return nil, err
		}
		if len(vals) == 0 {
			return nil, errors.New("token: empty balanceOf result")
		}
		bal, ok := vals[0].(*big.Int)
		if !ok || bal == nil {
			return nil, errors.New("token: unexpected balanceOf result type")
		}
		return bal, nil
	}

	return NewWithBalanceFunc(cfg, fn), nil
}

// NewWithBalanceFunc builds an on-chain-style checker around an arbitrary
// BalanceFunc. Exposed for tests so balance resolution can be stubbed without a
// live RPC.
func NewWithBalanceFunc(cfg Config, fn BalanceFunc) Checker {
	ttl := cfg.CacheTTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &evmChecker{
		balanceFn: fn,
		min:       new(big.Int).Set(cfg.MinBalance),
		ttl:       ttl,
		failOpen:  cfg.FailOpen,
		cache:     make(map[common.Address]cacheEntry),
		now:       time.Now,
	}
}

type cacheEntry struct {
	holder bool
	at     time.Time
}

type evmChecker struct {
	balanceFn BalanceFunc
	min       *big.Int
	ttl       time.Duration
	failOpen  bool

	mu    sync.RWMutex
	cache map[common.Address]cacheEntry
	now   func() time.Time
}

func (c *evmChecker) Enabled() bool { return true }

func (c *evmChecker) Allow(wallet string) bool {
	holder, known := c.IsHolder(wallet)
	if !known {
		return c.failOpen
	}
	return holder
}

func (c *evmChecker) IsHolder(wallet string) (bool, bool) {
	addr := common.HexToAddress(wallet)
	c.mu.RLock()
	e, ok := c.cache[addr]
	c.mu.RUnlock()
	if !ok || c.now().Sub(e.at) > c.ttl {
		return false, false
	}
	return e.holder, true
}

func (c *evmChecker) Refresh(ctx context.Context, wallets []string) {
	for _, w := range wallets {
		addr := common.HexToAddress(w)

		c.mu.RLock()
		e, ok := c.cache[addr]
		fresh := ok && c.now().Sub(e.at) <= c.ttl
		c.mu.RUnlock()
		if fresh {
			continue
		}

		bal, err := c.balanceFn(ctx, addr)
		if err != nil {
			// Leave the entry unresolved; Allow applies the fail-open policy.
			continue
		}
		c.mu.Lock()
		c.cache[addr] = cacheEntry{holder: bal.Cmp(c.min) >= 0, at: c.now()}
		c.mu.Unlock()
	}
}

// ScaleTokens returns wholeTokens * 10^decimals — the raw minimum balance for a
// human-readable token count (e.g. 1,000,000 tokens at 18 decimals).
func ScaleTokens(wholeTokens *big.Int, decimals uint) *big.Int {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	return new(big.Int).Mul(wholeTokens, scale)
}
