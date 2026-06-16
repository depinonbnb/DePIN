package main

import (
	"context"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/depinonbnb/depin/internal/api"
	"github.com/depinonbnb/depin/internal/scheduler"
	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/store/memory"
	"github.com/depinonbnb/depin/internal/store/sqlite"
	"github.com/depinonbnb/depin/internal/token"
	"github.com/depinonbnb/depin/internal/verification"
	"github.com/joho/godotenv"
)

// defaultSQLiteDSN is the on-disk DSN we apply when DATABASE_URL/DB_PATH are
// unset. It enables WAL mode, a 5s busy_timeout, foreign-key enforcement,
// NORMAL synchronous level, and turns BEGIN into BEGIN IMMEDIATE so writer
// lock acquisition is deterministic (see docs/design/persistence.md §6).
const defaultSQLiteDSN = "file:./data/depin.db" +
	"?_pragma=journal_mode(WAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(ON)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_txlock=immediate"

// configureLogger initializes the global slog logger from env:
//   - LOG_FORMAT=json selects the JSON handler (default: text)
//   - LOG_LEVEL=debug|info|warn|error sets the level (default: info)
func configureLogger() {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "info", "":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if strings.EqualFold(os.Getenv("LOG_FORMAT"), "json") {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}

// resolveSchedulerConfig reads scheduler-related env vars and applies the
// ADR-0007 / ADR-0008 defaults for any that are unset/invalid. Recognised
// vars:
//
//	HEARTBEAT_INTERVAL       (Go duration, e.g. "5m"; default 5m)
//	CHALLENGE_CHECK_INTERVAL (Go duration; default 1m)
//	REWARD_INTERVAL          (Go duration; default 5m)
//	SNAPSHOT_INTERVAL        (Go duration; default 168h = weekly per ADR-0008,
//	                         "0" or "off" disables the snapshot cron)
//	RPC_WORKERS              (positive int; default 50)
//	SCHEDULER_ENABLED        (truthy unless "0", "false", "no", "off")
func resolveSchedulerConfig() scheduler.Config {
	cfg := scheduler.Config{
		HeartbeatInterval:      parseDurationEnv("HEARTBEAT_INTERVAL", 5*time.Minute),
		ChallengeCheckInterval: parseDurationEnv("CHALLENGE_CHECK_INTERVAL", 1*time.Minute),
		RewardInterval:         parseDurationEnv("REWARD_INTERVAL", 5*time.Minute),
		SnapshotInterval:       resolveSnapshotInterval(),
		RPCWorkers:             parseIntEnv("RPC_WORKERS", 50),
	}

	switch strings.ToLower(strings.TrimSpace(os.Getenv("SCHEDULER_ENABLED"))) {
	case "0", "false", "no", "off":
		cfg.Disabled = true
	default:
		cfg.Disabled = false
	}
	return cfg
}

// resolveSnapshotInterval interprets SNAPSHOT_INTERVAL with three branches:
//
//   - unset       → return 0 so applyDefaults uses the weekly default.
//   - "0"/"off"   → return -1 so applyDefaults preserves "explicitly disabled".
//   - duration    → return the parsed duration.
//
// Anything unparseable falls back to the weekly default with a warning.
func resolveSnapshotInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("SNAPSHOT_INTERVAL"))
	if raw == "" {
		return 0
	}
	switch strings.ToLower(raw) {
	case "0", "off", "false", "disabled", "no":
		return -1
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		slog.Warn("invalid SNAPSHOT_INTERVAL; using weekly default",
			"value", raw, "default", 7*24*time.Hour)
		return 0
	}
	return d
}

// parseDurationEnv parses a Go duration env var; returns def on miss/error.
func parseDurationEnv(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		slog.Warn("invalid duration env var; using default",
			"key", key, "value", v, "default", def)
		return def
	}
	return d
}

// parseIntEnv parses a positive int env var; returns def on miss/error.
func parseIntEnv(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		slog.Warn("invalid int env var; using default",
			"key", key, "value", v, "default", def)
		return def
	}
	return n
}

// resolveTrustedRPCs returns the list of trusted RPC endpoints used by the
// verifier per ADR-0009. Resolution order:
//
//  1. TRUSTED_RPCS (comma-separated) — preferred, the ADR-0009 contract.
//  2. TRUSTED_RPC (singular) — legacy ADR-0005 var. Logged with a
//     deprecation warning and treated as a one-element list. Drop after
//     one release cycle.
//  3. Default: 3 BSC dataseeds. ADR-0009 documents this as the right
//     default for BSC tiers; opBNB tiers using the same quorum is
//     acceptable for now (the actual lookup is per-call, so heterogeneous
//     endpoints don't matter for correctness — only for accuracy).
func resolveTrustedRPCs() []string {
	if raw := strings.TrimSpace(os.Getenv("TRUSTED_RPCS")); raw != "" {
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}

	if legacy := strings.TrimSpace(os.Getenv("TRUSTED_RPC")); legacy != "" {
		slog.Warn("TRUSTED_RPC (singular) is deprecated and will be removed next release; set TRUSTED_RPCS (comma-separated) instead — see ADR-0009",
			"value", legacy)
		return []string{legacy}
	}

	return []string{
		"https://bsc-dataseed1.binance.org",
		"https://bsc-dataseed2.binance.org",
		"https://bsc-dataseed3.binance.org",
	}
}

// resolveStoreBackend reads DATABASE_URL (preferred) and DB_PATH (legacy fallback
// from ADR-0006) and decides which store to construct. Returns the constructed
// Store and a human-readable description for logging.
func resolveStoreBackend() (store.Store, string, error) {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("DB_PATH"))
	}

	switch dsn {
	case "":
		// No explicit DSN: use SQLite at the default path. Operators who want
		// the volatile in-memory backend can set DATABASE_URL=memory.
		s, err := sqlite.NewSQLite(defaultSQLiteDSN)
		if err != nil {
			return nil, "", err
		}
		return s, "sqlite (default: ./data/depin.db)", nil
	case "memory":
		return memory.NewMemory(), "memory (volatile)", nil
	default:
		s, err := sqlite.NewSQLite(dsn)
		if err != nil {
			return nil, "", err
		}
		return s, "sqlite (" + dsn + ")", nil
	}
}

// defaultTokenContract is the live DEPIN token on BSC mainnet. Point awards
// (uptime + challenge) require the operator's wallet to hold at least
// TOKEN_MIN_BALANCE of it. Override with TOKEN_CONTRACT_ADDRESS, or disable the
// gate entirely with TOKEN_CONTRACT_ADDRESS=off.
const defaultTokenContract = "0x426326e876ad01fd99db898604c16c0628da7777"

// resolveTokenChecker builds the token-balance gate from env. By default it
// gates on the DEPIN token (defaultTokenContract) on BSC mainnet. Recognised vars:
//
//	TOKEN_CONTRACT_ADDRESS  ERC-20 address (default: DEPIN); "off" disables the gate
//	TOKEN_RPC_URL           balance-read RPC (default: BSC mainnet dataseed)
//	TOKEN_MIN_BALANCE       whole tokens required (default: 1000000)
//	TOKEN_DECIMALS          token decimals (default: 18)
//	TOKEN_CACHE_TTL         balance cache TTL (Go duration; default: 10m)
//	TOKEN_GATE_FAIL_OPEN    award when a balance can't be read (default: true)
//
// Any misconfiguration falls back to the disabled (Noop) checker with a logged
// error rather than crashing the server or silently withholding all points.
func resolveTokenChecker() token.Checker {
	addr := strings.TrimSpace(os.Getenv("TOKEN_CONTRACT_ADDRESS"))
	if addr == "" {
		addr = defaultTokenContract
	}
	if strings.EqualFold(addr, "off") || strings.EqualFold(addr, "disabled") {
		slog.Info("token gate disabled (TOKEN_CONTRACT_ADDRESS=off)")
		return token.Noop{}
	}

	rpcURL := strings.TrimSpace(os.Getenv("TOKEN_RPC_URL"))
	if rpcURL == "" {
		rpcURL = "https://bsc-dataseed1.binance.org"
	}

	decimals := uint(parseIntEnv("TOKEN_DECIMALS", 18))

	rawWhole := strings.TrimSpace(os.Getenv("TOKEN_MIN_BALANCE"))
	if rawWhole == "" {
		rawWhole = "1000000"
	}
	wholeTokens, ok := new(big.Int).SetString(rawWhole, 10)
	if !ok || wholeTokens.Sign() <= 0 {
		slog.Error("invalid TOKEN_MIN_BALANCE; token gate disabled", "value", rawWhole)
		return token.Noop{}
	}

	failOpen := true
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TOKEN_GATE_FAIL_OPEN"))) {
	case "0", "false", "no", "off":
		failOpen = false
	}

	checker, err := token.New(token.Config{
		RPCURL:       rpcURL,
		TokenAddress: addr,
		MinBalance:   token.ScaleTokens(wholeTokens, decimals),
		CacheTTL:     parseDurationEnv("TOKEN_CACHE_TTL", 10*time.Minute),
		FailOpen:     failOpen,
	})
	if err != nil {
		slog.Error("token gate init failed; gating disabled", "error", err)
		return token.Noop{}
	}

	slog.Info("token gate enabled",
		"token", addr,
		"rpc", rpcURL,
		"min_tokens", wholeTokens.String(),
		"decimals", decimals,
		"fail_open", failOpen,
	)
	return checker
}

func main() {
	// Load .env file if it exists
	_ = godotenv.Load()

	configureLogger()

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	trustedRPCs := resolveTrustedRPCs()

	adminAPIKey := os.Getenv("ADMIN_API_KEY")
	adminStatus := "configured"
	if adminAPIKey == "" {
		adminStatus = "disabled"
	}

	nodeStore, backendDescription, err := resolveStoreBackend()
	if err != nil {
		slog.Error("failed to initialize store backend", "error", err)
		os.Exit(1)
	}

	slog.Info("DePIN BNB Verification Server starting",
		"port", port,
		"trusted_rpcs", trustedRPCs,
		"admin_status", adminStatus,
		"store_backend", backendDescription,
	)
	if adminAPIKey == "" {
		slog.Warn("admin endpoints disabled — ADMIN_API_KEY not configured")
	}

	verifier := verification.NewVerifier(trustedRPCs)

	// Set up signal-driven shutdown context. Use signal.NotifyContext so the
	// returned context is cancelled on SIGINT/SIGTERM and we can drive
	// graceful shutdown of both the HTTP server and the store.
	shutdownCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	// Start cleanup goroutine for expired in-memory pending challenges held by
	// the verifier (the local-prover flow). The SQLite store has its own
	// pruner for the persistent pending_challenges table; this loop only
	// covers the verifier's own short-lived map. Tied to shutdownCtx so it
	// exits cleanly on signal.
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-shutdownCtx.Done():
				return
			case <-ticker.C:
				cleaned := verifier.CleanupExpiredChallenges()
				if cleaned > 0 {
					slog.Info("cleaned up expired challenges", "count", cleaned)
				}
			}
		}
	}()

	// Build the verification scheduler (ADR-0007). We always construct it so
	// the wiring is the same in tests; SCHEDULER_ENABLED=false skips Start().
	schedCfg := resolveSchedulerConfig()
	sched := scheduler.New(nodeStore, verifier, schedCfg)

	// Token gate: the same checker instance is shared by the scheduler (which
	// enforces the gate on uptime/challenge awards and keeps the balance cache
	// warm) and the API handlers (which read it for holds_token and to gate
	// local-prover submissions). Must be installed before Start.
	holder := resolveTokenChecker()
	sched.SetHolderChecker(holder)

	if schedCfg.Disabled {
		slog.Info("scheduler disabled via SCHEDULER_ENABLED env")
	} else {
		sched.Start()
	}

	// Phase 5 (ADR-0008 cron): SNAPSHOT_INTERVAL is now live — wired into
	// scheduler.Config via resolveSnapshotInterval. The scheduler skips its
	// snapshot loop when SNAPSHOT_INTERVAL is "0"/"off"; otherwise it builds
	// a cycle every interval. Manual publish via POST
	// /api/admin/snapshot/publish remains supported for ad-hoc operator use.
	if schedCfg.SnapshotInterval > 0 {
		slog.Info("snapshot cron active", "interval", schedCfg.SnapshotInterval)
	} else if schedCfg.SnapshotInterval < 0 {
		slog.Info("snapshot cron disabled (SNAPSHOT_INTERVAL=0)")
	}

	router := api.SetupRouter(nodeStore, verifier, adminAPIKey, api.WithHolderChecker(holder))

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("server ready", "addr", ":"+port)
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		slog.Error("server failed", "error", err)
		_ = sched.Close()
		_ = nodeStore.Close()
		os.Exit(1)
	case <-shutdownCtx.Done():
		slog.Info("shutdown signal received")
	}

	// Graceful shutdown order (per ADR-0007 §Consequences): HTTP listener
	// first (stop accepting), schedulers second (stop generating work), store
	// last (so any in-flight reward writes still have a destination).
	gracefulCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(gracefulCtx); err != nil {
		slog.Error("server shutdown failed", "error", err)
	}
	if err := sched.Close(); err != nil {
		slog.Error("scheduler close failed", "error", err)
	}
	if err := nodeStore.Close(); err != nil {
		slog.Error("store close failed", "error", err)
	}
	slog.Info("shutdown complete")
}
