// Package api: health_handlers.go implements two endpoints:
//
//   - GET /health  — SHALLOW liveness. Returns {"status":"ok"} as long as the
//     process is running and the HTTP server is accepting requests. Used by
//     load balancers and Kubernetes' livenessProbe; should NEVER fail unless
//     the process itself is unrecoverable.
//
//   - GET /ready   — DEEP readiness. Probes the store, the trusted-RPC quorum,
//     and the most recent snapshot age. Returns the per-check breakdown so
//     dashboards can pinpoint which dependency is unhealthy. 200 when every
//     mandatory check is OK; 503 when any mandatory check fails.
//
// The shallow / deep split mirrors Kubernetes' guidance: livenessProbe must
// not consider downstream services (otherwise a transient downstream outage
// triggers pod restarts), but readinessProbe should so traffic stops landing
// on a pod whose downstream is dead.
package api

import (
	"context"
	"net/http"
	"time"

	"github.com/depinonbnb/depin/internal/metrics"
	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/verification"
	"github.com/gin-gonic/gin"
)

// snapshotStaleAfter controls when /ready flags the most recent snapshot as
// "stale". 14 days = 2 publish cycles at the default weekly cadence; lets
// operators miss one cycle before /ready degrades.
const snapshotStaleAfter = 14 * 24 * time.Hour

// healthChecker bundles the dependencies the readiness probe needs. We keep
// the API package's primary Handlers type focused on HTTP routes (the existing
// design splits handlers across files), so the readiness probe gets its own
// constructor.
type healthChecker struct {
	store    store.Store
	verifier *verification.Verifier
}

// newHealthChecker is the trivial constructor; kept for symmetry with
// NewHandlers.
func newHealthChecker(s store.Store, v *verification.Verifier) *healthChecker {
	return &healthChecker{store: s, verifier: v}
}

// shallowHealth is GET /health: process-up liveness. Always returns 200 so
// long as the handler runs.
func (hc *healthChecker) shallowHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// readiness is GET /ready: deep readiness. Each check has a 1.5s budget so
// the whole probe stays under ~6s in the worst case (4 checks).
func (hc *healthChecker) readiness(c *gin.Context) {
	overallCtx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Second)
	defer cancel()

	storeCheck := hc.checkStore(overallCtx)
	schedulerCheck := hc.checkScheduler()
	rpcCheck := hc.checkTrustedRPC(overallCtx)
	snapshotCheck := hc.checkSnapshot()

	checks := gin.H{
		"store":       storeCheck,
		"scheduler":   schedulerCheck,
		"trusted_rpc": rpcCheck,
		"snapshot":    snapshotCheck,
	}

	// Roll up. Store + trusted_rpc are mandatory: any "fail" makes the whole
	// probe fail. Snapshot "stale" or "never" is a degraded state, not a
	// failure — we still want operators to take traffic on a pod that hasn't
	// yet published its first cycle.
	overall := "ok"
	httpCode := http.StatusOK
	if storeCheck["status"] == "fail" || rpcCheck["status"] == "fail" {
		overall = "unready"
		httpCode = http.StatusServiceUnavailable
	} else if snapshotCheck["status"] == "stale" || rpcCheck["status"] == "degraded" {
		overall = "degraded"
		// degraded is not a hard fail — keep 200 so the LB doesn't drain us.
	}

	c.JSON(httpCode, gin.H{
		"status": overall,
		"checks": checks,
	})
}

// checkStore runs Store.Ping with a tight deadline.
func (hc *healthChecker) checkStore(parent context.Context) gin.H {
	if hc.store == nil {
		return gin.H{"status": "fail", "error": "store not configured", "latency_ms": 0}
	}
	ctx, cancel := context.WithTimeout(parent, 1*time.Second)
	defer cancel()
	start := time.Now()
	err := hc.store.Ping(ctx)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return gin.H{"status": "fail", "error": err.Error(), "latency_ms": latency}
	}
	return gin.H{"status": "ok", "latency_ms": latency}
}

// checkScheduler reads the scheduler tick gauges directly out of the registry
// (via metrics.SchedulerLastTickValue) rather than holding a back-reference
// into the scheduler package, which would create a dependency cycle.
//
// A zero timestamp means the loop has either never run or hasn't run since
// process start — distinguishable from "actually unhealthy" by inspecting
// the same gauge in /metrics.
func (hc *healthChecker) checkScheduler() gin.H {
	return gin.H{
		"status":              "ok",
		"last_heartbeat_tick": metrics.SchedulerLastTickValue("heartbeat"),
		"last_challenge_tick": metrics.SchedulerLastTickValue("challenge"),
		"last_reward_tick":    metrics.SchedulerLastTickValue("uptime"),
		"last_snapshot_tick":  metrics.SchedulerLastTickValue("snapshot"),
	}
}

// checkTrustedRPC fans out to every endpoint via QuorumClient.HealthCheck.
// Reachability of all endpoints => "ok"; any reachable but not all =>
// "degraded"; none reachable => "fail".
func (hc *healthChecker) checkTrustedRPC(parent context.Context) gin.H {
	if hc.verifier == nil || hc.verifier.TrustedRPC() == nil {
		return gin.H{"status": "fail", "error": "verifier not configured"}
	}
	q := hc.verifier.TrustedRPC()
	endpoints := q.Endpoints()
	total := len(endpoints)
	if total == 0 {
		return gin.H{
			"status":              "fail",
			"endpoints_total":     0,
			"endpoints_reachable": 0,
			"error":               "no trusted RPC endpoints configured",
		}
	}

	probes := q.HealthCheck(parent)
	reachable := 0
	probeOut := make([]gin.H, 0, len(probes))
	for _, p := range probes {
		if p.OK {
			reachable++
		}
		probeOut = append(probeOut, gin.H{
			"endpoint":   p.Endpoint,
			"ok":         p.OK,
			"latency_ms": p.LatencyMs,
			"error":      p.Err,
		})
	}

	status := "ok"
	switch {
	case reachable == 0:
		status = "fail"
	case reachable < total:
		status = "degraded"
	}
	return gin.H{
		"status":              status,
		"endpoints_total":     total,
		"endpoints_reachable": reachable,
		"probes":              probeOut,
	}
}

// checkSnapshot reads the latest snapshot's published_at timestamp via the
// store and reports stale / never / ok.
func (hc *healthChecker) checkSnapshot() gin.H {
	snap, err := hc.store.GetLatestSnapshot()
	if err != nil {
		return gin.H{"status": "fail", "error": err.Error()}
	}
	if snap == nil {
		return gin.H{"status": "never", "last_published_at": int64(0), "age_seconds": int64(0)}
	}
	age := time.Since(time.UnixMilli(snap.PublishedAt))
	status := "ok"
	if age > snapshotStaleAfter {
		status = "stale"
	}
	return gin.H{
		"status":            status,
		"last_published_at": snap.PublishedAt,
		"age_seconds":       int64(age.Seconds()),
		"cycle_id":          snap.CycleID,
	}
}

// metricsHandler returns the gin handler that exposes /metrics. The metrics
// package owns the actual exposition; this is just the gin.WrapH wrapper.
// router.go mounts the result at top level (no /api prefix, no rate limit,
// no CORS).
func metricsHandler() gin.HandlerFunc {
	return gin.WrapH(metrics.Handler())
}
