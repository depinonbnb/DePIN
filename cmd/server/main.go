package main

import (
	"context"
	"errors"
	"log/slog"
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
// ADR-0007 defaults for any that are unset/invalid. Recognised vars:
//
//	HEARTBEAT_INTERVAL       (Go duration, e.g. "5m"; default 5m)
//	CHALLENGE_CHECK_INTERVAL (Go duration; default 1m)
//	REWARD_INTERVAL          (Go duration; default 5m)
//	RPC_WORKERS              (positive int; default 50)
//	SCHEDULER_ENABLED        (truthy unless "0", "false", "no", "off")
func resolveSchedulerConfig() scheduler.Config {
	cfg := scheduler.Config{
		HeartbeatInterval:      parseDurationEnv("HEARTBEAT_INTERVAL", 5*time.Minute),
		ChallengeCheckInterval: parseDurationEnv("CHALLENGE_CHECK_INTERVAL", 1*time.Minute),
		RewardInterval:         parseDurationEnv("REWARD_INTERVAL", 5*time.Minute),
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

func main() {
	// Load .env file if it exists
	_ = godotenv.Load()

	configureLogger()

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	trustedRPC := os.Getenv("TRUSTED_RPC")
	if trustedRPC == "" {
		trustedRPC = "https://bsc-dataseed1.binance.org"
	}

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
		"trusted_rpc", trustedRPC,
		"admin_status", adminStatus,
		"store_backend", backendDescription,
	)
	if adminAPIKey == "" {
		slog.Warn("admin endpoints disabled — ADMIN_API_KEY not configured")
	}

	verifier := verification.NewVerifier(trustedRPC)

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
	if schedCfg.Disabled {
		slog.Info("scheduler disabled via SCHEDULER_ENABLED env")
	} else {
		sched.Start()
	}

	router := api.SetupRouter(nodeStore, verifier, adminAPIKey)

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
