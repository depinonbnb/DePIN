// Package api: ratelimit.go implements per-IP and per-wallet rate limiting
// using golang.org/x/time/rate. Two middlewares are exposed:
//
//   - RateLimitMiddleware(rps, burst): keyed by gin.Context.ClientIP().
//     Used globally and for tighter caps on /nodes/register and
//     /challenges/submit.
//   - WalletRateLimitMiddleware(rps, burst, walletField): keyed by the
//     wallet address inside the request body. Applied to signature-bearing
//     routes so a single attacker can't spread attempts across many IPs and
//     bypass the per-IP cap.
//
// Both middlewares maintain their own bounded LRU map of key → *rate.Limiter
// so the process can't be DOSed into unbounded memory growth by a flood of
// distinct IPs / wallets. The cap is set by maxLimiters.
package api

import (
	"bytes"
	"container/list"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/depinonbnb/depin/internal/metrics"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// maxLimiters caps the LRU per limiter set. 10k keys × ~64 bytes/limiter ≈
// 640KB, comfortably bounded; a real public service should tune this.
const maxLimiters = 10000

// limiterStore is a goroutine-safe LRU keyed by string. Each value is a
// *rate.Limiter. Eviction is on insert when the map size exceeds maxLimiters.
type limiterStore struct {
	mu    sync.Mutex
	m     map[string]*list.Element
	order *list.List
	rps   rate.Limit
	burst int
	cap   int
}

type limiterEntry struct {
	key string
	lim *rate.Limiter
}

func newLimiterStore(rps rate.Limit, burst, cap int) *limiterStore {
	return &limiterStore{
		m:     make(map[string]*list.Element, cap),
		order: list.New(),
		rps:   rps,
		burst: burst,
		cap:   cap,
	}
}

// get returns the limiter for key, creating it if missing. LRU position is
// updated so evictions hit cold keys first.
func (s *limiterStore) get(key string) *rate.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()

	if elem, ok := s.m[key]; ok {
		s.order.MoveToFront(elem)
		return elem.Value.(*limiterEntry).lim
	}

	lim := rate.NewLimiter(s.rps, s.burst)
	elem := s.order.PushFront(&limiterEntry{key: key, lim: lim})
	s.m[key] = elem

	if s.order.Len() > s.cap {
		oldest := s.order.Back()
		if oldest != nil {
			s.order.Remove(oldest)
			entry := oldest.Value.(*limiterEntry)
			delete(s.m, entry.key)
		}
	}
	return lim
}

// RateLimitMiddleware enforces per-IP rate limiting at perIPRPS requests per
// second with the given burst. On limit it returns 429 Too Many Requests with
// a Retry-After header (seconds, rounded up).
//
// Calls metrics.RateLimit429Total with the route label "global" or "strict".
// Routes are inferred from the rate parameters: anything matching the strict
// caps below is "strict", everything else is "global". The router calls a
// labelled variant when it wants explicit naming.
func RateLimitMiddleware(perIPRPS rate.Limit, burst int) gin.HandlerFunc {
	label := "global"
	if perIPRPS == StrictIPRPS && burst == StrictIPBurst {
		label = "strict"
	}
	return RateLimitMiddlewareLabelled(perIPRPS, burst, label)
}

// RateLimitMiddlewareLabelled is RateLimitMiddleware with an explicit label
// for the metrics emission. Used by callers that want to distinguish multiple
// strict limits (e.g. /nodes/register vs /challenges/submit) in dashboards.
func RateLimitMiddlewareLabelled(perIPRPS rate.Limit, burst int, label string) gin.HandlerFunc {
	store := newLimiterStore(perIPRPS, burst, maxLimiters)
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if ip == "" {
			ip = "unknown"
		}
		lim := store.get(ip)
		if !lim.Allow() {
			metrics.RateLimit429Total.WithLabelValues(label).Inc()
			retry := retryAfterSeconds(perIPRPS)
			c.Header("Retry-After", fmt.Sprintf("%d", retry))
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// WalletRateLimitMiddleware enforces per-wallet rate limiting on JSON request
// bodies. It reads the body, extracts walletField, and consults a per-wallet
// rate.Limiter. The body is restored to the context so downstream handlers
// can re-bind it normally.
//
// Routes that don't carry a wallet (or carry one outside walletField) skip
// the limiter — we don't want CPU-cheap parses to drop into the limiter
// unnecessarily.
//
// 429 emissions are tagged "wallet_<walletField>" in the rate_limit_429_total
// counter (e.g. "wallet_wallet_address" or "wallet_node_id") so a single
// dashboard panel can split rate-limit kinds without an explicit per-route
// label.
func WalletRateLimitMiddleware(rps rate.Limit, burst int, walletField string) gin.HandlerFunc {
	store := newLimiterStore(rps, burst, maxLimiters)
	label := "wallet_" + walletField
	return func(c *gin.Context) {
		// Only POSTs carry a wallet body. Don't read GET bodies.
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}

		// Drain body, parse for the wallet, restore for handlers.
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.Next()
			return
		}
		_ = c.Request.Body.Close()
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		wallet := extractWallet(body, walletField)
		if wallet == "" {
			c.Next()
			return
		}

		key := strings.ToLower(wallet)
		if !store.get(key).Allow() {
			metrics.RateLimit429Total.WithLabelValues(label).Inc()
			retry := retryAfterSeconds(rps)
			c.Header("Retry-After", fmt.Sprintf("%d", retry))
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "wallet rate limit exceeded"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// extractWallet pulls a single string field out of a JSON body. It does NOT
// fully unmarshal the request — the goal is to be cheap on the limiter
// fast-path. If the field is missing or non-string, returns "".
func extractWallet(body []byte, field string) string {
	if len(body) == 0 {
		return ""
	}
	// Decode into a generic map once; same allocation cost as a targeted
	// streaming parse for our small (~1 KB) bodies.
	var generic map[string]interface{}
	if err := json.Unmarshal(body, &generic); err != nil {
		return ""
	}
	if v, ok := generic[field].(string); ok {
		return v
	}
	return ""
}

// retryAfterSeconds picks a sensible Retry-After integer for the given rate.
// Uses ceil(1 / rps) so a 1-rps limit reports 1 second, 0.1 rps reports 10s.
// Falls back to 1 second if the limit is not finite.
func retryAfterSeconds(r rate.Limit) int {
	if r <= 0 {
		return 60
	}
	secs := 1.0 / float64(r)
	if secs < 1 {
		return 1
	}
	// Round up.
	return int(secs + 0.999)
}

// Per-route helper constants. Values tuned to ADR-0009-adjacent norms; not
// a separate ADR — operations can tune via constants here.
//
// Globals are deliberately permissive (60 rpm with a generous burst); the
// tighter caps live on the sensitive routes.
var (
	GlobalIPRPS    = rate.Limit(1.0)         // 60 / minute
	GlobalIPBurst  = 100
	StrictIPRPS    = rate.Every(6 * time.Second) // 10 / minute
	StrictIPBurst  = 5

	WalletRegisterRPS    = rate.Every(12 * time.Second) // 5 / minute
	WalletRegisterBurst  = 2
	WalletSubmitRPS      = rate.Every(2 * time.Second) // 30 / minute
	WalletSubmitBurst    = 5
)
