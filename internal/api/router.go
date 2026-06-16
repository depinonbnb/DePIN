// Package api: router.go wires the Gin engine, middleware stack, and route
// table. Phase 3 adds three middleware additions:
//
//   - CORSMiddleware: replaces the previous wildcard "*" Access-Control-
//     Allow-Origin with an env-driven allowlist (CORS_ALLOWED_ORIGINS,
//     comma-separated). Default: empty list, which causes the middleware to
//     never set the header — browsers will block cross-origin reads.
//   - RateLimitMiddleware: per-IP global rate limiting (60 rpm with burst).
//   - Tighter per-IP and per-wallet limits on /nodes/register and
//     /challenges/submit.
//
// Admin routes are still gated by AdminAuthMiddleware; they intentionally
// skip the rate limiters because they're already protected.
package api

import (
	"os"
	"strings"

	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/token"
	"github.com/depinonbnb/depin/internal/verification"
	"github.com/gin-gonic/gin"
)

// RouterOption customizes the handlers wired into the router. Existing callers
// that pass no options keep the previous behavior (token gating disabled).
type RouterOption func(*Handlers)

// WithHolderChecker injects the token-gate checker used to withhold points from
// wallets below the minimum balance and to populate the holds_token field.
func WithHolderChecker(c token.Checker) RouterOption {
	return func(h *Handlers) {
		if c != nil {
			h.holder = c
		}
	}
}

// CORSMiddleware reads CORS_ALLOWED_ORIGINS at construction time and returns
// a Gin handler that echoes the Origin header only when it is in the allow
// list. allowedOrigins should be the env-resolved slice; pass nil/empty for
// "block everything".
func CORSMiddleware(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		o = strings.TrimSpace(o)
		if o != "" {
			allowed[o] = struct{}{}
		}
	}

	const (
		methodList   = "GET, POST, OPTIONS"
		headerList   = "Content-Type, Authorization, X-Admin-Key"
		preflightTTL = "300" // 5 minutes
	)

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			if _, ok := allowed[origin]; ok {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
				c.Header("Access-Control-Allow-Methods", methodList)
				c.Header("Access-Control-Allow-Headers", headerList)
				c.Header("Access-Control-Max-Age", preflightTTL)
			}
		}

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}

// resolveAllowedOrigins reads CORS_ALLOWED_ORIGINS into a slice. Empty/unset
// means "no origin is allowed"; document this in SPEC §8.
func resolveAllowedOrigins() []string {
	raw := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS"))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func SetupRouter(store store.Store, verifier *verification.Verifier, adminAPIKey string, opts ...RouterOption) *gin.Engine {
	router := gin.Default()

	// /metrics MUST be mounted BEFORE the global middleware stack: Prometheus
	// scrapes are server-side and should be neither rate-limited nor
	// CORS-checked. Mounting it on the same listener (rather than a separate
	// :9090) keeps the binary single-port; SPEC §12 documents the choice.
	router.GET("/metrics", metricsHandler())

	// CORS: env-driven allowlist (CORS_ALLOWED_ORIGINS).
	router.Use(CORSMiddleware(resolveAllowedOrigins()))

	// Global per-IP rate limit (defense in depth — sensitive routes also have
	// per-route caps below).
	router.Use(RateLimitMiddleware(GlobalIPRPS, GlobalIPBurst))

	handlers := NewHandlers(store, verifier)
	for _, opt := range opts {
		opt(handlers)
	}
	hc := newHealthChecker(store, verifier)

	// Health check (shallow liveness) and readiness (deep). /health stays a
	// trivial process-up probe so load balancers don't restart the pod when a
	// downstream blip happens; /ready is the deep probe that LBs should drain
	// traffic on. See health_handlers.go for the rationale.
	router.GET("/health", hc.shallowHealth)
	router.GET("/ready", hc.readiness)

	api := router.Group("/api")
	{
		// Tighter per-IP cap + per-wallet cap on /nodes/register.
		api.POST("/nodes/register",
			RateLimitMiddleware(StrictIPRPS, StrictIPBurst),
			WalletRateLimitMiddleware(WalletRegisterRPS, WalletRegisterBurst, "wallet_address"),
			handlers.RegisterNode,
		)
		api.GET("/nodes/:nodeId", handlers.GetNode)
		api.GET("/nodes/wallet/:walletAddress", handlers.GetNodesByWallet)
		api.GET("/nodes/:nodeId/stats", handlers.GetNodeStats)

		// Wallet stats (total points across all nodes)
		api.GET("/wallet/:walletAddress/stats", handlers.GetWalletStats)

		// Challenges (for local-prover). The submit path takes a per-wallet
		// limit too — but on a far higher cap than register because a healthy
		// operator legitimately submits dozens per hour.
		api.GET("/challenges/request", handlers.RequestChallenge)
		api.POST("/challenges/submit",
			RateLimitMiddleware(StrictIPRPS, StrictIPBurst),
			// Per-source cap on submits. We key by node_id rather than wallet
			// because a wallet owning multiple nodes legitimately wants
			// per-node throughput. node_id is in the body the handler will
			// bind anyway, so the middleware reads it without an extra store
			// hit.
			WalletRateLimitMiddleware(WalletSubmitRPS, WalletSubmitBurst, "node_id"),
			handlers.SubmitChallenge,
		)

		// Direct verification (for exposed-rpc)
		api.POST("/verify/:nodeId", handlers.VerifyNode)
		api.GET("/verify/:nodeId/heartbeat", handlers.CheckHeartbeat)

		// Public data
		api.GET("/leaderboard", handlers.GetLeaderboard)
		api.GET("/stats", handlers.GetNetworkStats)

		// Reward snapshots (ADR-0008). Public read endpoints — anyone can
		// re-derive the tree from these to verify the published root. Order
		// matters: the static "latest" route must be registered BEFORE the
		// parametric "/:cycleId" route, otherwise Gin would route a literal
		// "latest" through the param handler. Same for the wallet/claim
		// pair below.
		api.GET("/snapshots/latest", handlers.GetLatestSnapshot)
		api.GET("/snapshots/:cycleId", handlers.GetSnapshotByCycle)
		api.GET("/wallet/:walletAddress/claim/latest", handlers.GetWalletClaimLatest)
		api.GET("/wallet/:walletAddress/claim/:cycleId", handlers.GetWalletClaim)

		// Admin endpoints (protected by API key).
		// AdminAuthMiddleware fails closed when adminAPIKey is empty
		// (returns 503), so it MUST always be applied. Admin routes are
		// intentionally NOT rate limited — they're already auth-gated.
		admin := api.Group("/admin")
		admin.Use(AdminAuthMiddleware(adminAPIKey))
		{
			admin.GET("/flagged", handlers.GetFlaggedNodes)
			admin.POST("/review/:nodeId", handlers.ReviewNode)
			admin.POST("/test/create-node", handlers.TestCreateNode)
			admin.DELETE("/nodes/:nodeId", handlers.DeleteNode)

			// Snapshot publish (ADR-0008). Manual for Phase 4; the
			// SNAPSHOT_INTERVAL env var is reserved for the Phase 5
			// scheduler hook (see cmd/server/main.go).
			admin.POST("/snapshot/publish", handlers.PublishSnapshot)
		}
	}

	return router
}
