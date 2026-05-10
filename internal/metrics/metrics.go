// Package metrics owns the Prometheus instrumentation for the server. Every
// counter / gauge / histogram lives here so the wiring in handlers/scheduler
// can stay one-liners and the SPEC (§12 Observability) has a single index of
// what is exposed.
//
// Exposition: GET /metrics on the API listener (router.go mounts
// metrics.Handler() outside the /api group and skips both rate-limit and CORS
// middleware — Prometheus scrapers must not be throttled and never run in a
// browser).
//
// Conventions:
//   - All metric names are prefixed with `depin_`.
//   - Counter names end in `_total`.
//   - Histogram names end in `_seconds` for time-based observations and
//     `_ms` for explicit millisecond observations (the Prometheus convention
//     prefers seconds, but the verifier already operates in milliseconds and
//     we don't want to do float conversions on every observation).
//   - All labels are bounded — no high-cardinality user input ever lands in a
//     label value (route names are normalised, action names are enumerated).
package metrics

import (
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
)

// Counters
var (
	// ChallengesTotal counts every challenge dispatched by the scheduler or
	// produced by the local-prover submit path. Labels:
	//   result    = passed | failed | abort   (abort = quorum disagreement)
	//   method    = exposed-rpc | local-prover
	//   node_type = bsc-full | bsc-archive | opbnb-full | opbnb-fast | other
	ChallengesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "depin_challenges_total",
			Help: "Total challenges processed, labelled by outcome / verification method / node type.",
		},
		[]string{"result", "method", "node_type"},
	)

	// QuorumFailuresTotal counts ExecuteChallenge calls that returned
	// QuorumNoMajority. The verifier treats these as ABORT, not as cheating —
	// this counter is the canary for trusted-RPC drift.
	QuorumFailuresTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "depin_quorum_failures_total",
			Help: "Total quorum-failure events (no majority answer across trusted RPCs).",
		},
	)

	// RateLimit429Total counts 429 Too Many Requests returns. Labels:
	//   route = global | strict | wallet_register | wallet_submit
	RateLimit429Total = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "depin_rate_limit_429_total",
			Help: "Total 429 responses emitted by the rate-limit middleware.",
		},
		[]string{"route"},
	)

	// NonceReplaysTotal counts replayed nonces rejected by RegisterNode /
	// SubmitChallenge (HTTP 409).
	NonceReplaysTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "depin_nonce_replays_total",
			Help: "Total replayed nonces rejected by the API (HTTP 409).",
		},
	)

	// SSRFRejectionsTotal counts operator-supplied RPC endpoints rejected by
	// the SSRF allow-list.
	SSRFRejectionsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "depin_ssrf_rejections_total",
			Help: "Total operator-supplied RPC endpoints rejected by the SSRF guard.",
		},
	)

	// SnapshotsPublished counts successful snapshot publishes (admin or cron).
	SnapshotsPublished = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "depin_snapshots_published_total",
			Help: "Total reward snapshots successfully published.",
		},
	)

	// SnapshotBuildFailures counts snapshot cron failures (BuildCycle or
	// Persist returning a non-ErrNoEarners error).
	SnapshotBuildFailures = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "depin_snapshot_build_failures_total",
			Help: "Total snapshot build/persist failures (excluding ErrNoEarners).",
		},
	)

	// ClaimLookupsTotal counts /api/wallet/.../claim/... lookups. Labels:
	//   result = found | not_found
	ClaimLookupsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "depin_claim_lookups_total",
			Help: "Total claim lookups by outcome.",
		},
		[]string{"result"},
	)

	// SuspiciousEventsTotal counts AddSuspiciousEvent calls. Labels:
	//   kind = latency | answer_mismatch | missing_node | other
	SuspiciousEventsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "depin_suspicious_events_total",
			Help: "Total suspicious-event records by reason bucket.",
		},
		[]string{"kind"},
	)

	// AdminActionsTotal counts admin review actions. Labels:
	//   action = clear | warn | ban
	AdminActionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "depin_admin_actions_total",
			Help: "Total admin review actions taken.",
		},
		[]string{"action"},
	)
)

// Gauges
var (
	// NodesActive is the count of IsActive nodes observed during the most
	// recent scheduler bucket sweep. Updated by scheduler ticks.
	NodesActive = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "depin_nodes_active",
			Help: "Number of active nodes observed during the most recent scheduler tick.",
		},
	)

	// NodesByStatus partitions active nodes by CheatStatus. Labels:
	//   status = clean | warning | flagged | banned
	NodesByStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "depin_nodes_by_status",
			Help: "Active node count partitioned by CheatStatus.",
		},
		[]string{"status"},
	)

	// NodesByType partitions active nodes by NodeType label.
	NodesByType = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "depin_nodes_by_type",
			Help: "Active node count partitioned by NodeType.",
		},
		[]string{"type"},
	)

	// LastSnapshotPublished is the unix-second timestamp of the most recent
	// successful snapshot publish (manual or cron). 0 means "never".
	LastSnapshotPublished = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "depin_last_snapshot_published_seconds",
			Help: "Unix seconds at which the latest snapshot was published; 0 = never.",
		},
	)

	// SchedulerLastTick records the unix-second timestamp of the most recent
	// completion of each scheduler ticker. Labels:
	//   ticker = heartbeat | challenge | reward | snapshot
	SchedulerLastTick = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "depin_scheduler_last_tick_seconds",
			Help: "Unix seconds at which the most recent scheduler tick completed, by ticker name.",
		},
		[]string{"ticker"},
	)

	// RPCWorkerInflight is the current number of borrowed worker slots in the
	// scheduler's shared semaphore. The verifier doesn't currently update this
	// — the scheduler does, on every acquire/release.
	RPCWorkerInflight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "depin_rpc_worker_inflight",
			Help: "Current number of in-flight RPC workers in the scheduler pool.",
		},
	)

	// RPCWorkerCapacity is the configured Config.RPCWorkers value. Set once at
	// scheduler startup.
	RPCWorkerCapacity = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "depin_rpc_worker_capacity",
			Help: "Configured maximum RPC worker pool size.",
		},
	)
)

// Histograms
var (
	// VerificationLatencyMs records every per-challenge latency observation
	// the verifier produces. Buckets are aligned with the anti-cheat thresholds
	// in internal/types/types.go (plausible <100ms, suspicious 100-150,
	// public-rpc-like 150-300, slow 300-5000, timeout >5000).
	VerificationLatencyMs = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "depin_verification_latency_ms",
			Help:    "Per-challenge response latency in milliseconds.",
			Buckets: []float64{25, 50, 75, 100, 150, 200, 300, 500, 1000, 2000, 5000, 10000},
		},
	)

	// SnapshotBuildSeconds tracks BuildCycle wall-clock cost.
	SnapshotBuildSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "depin_snapshot_build_seconds",
			Help:    "Wall-clock time for reward.BuildCycle in seconds.",
			Buckets: prometheus.DefBuckets,
		},
	)

	// QuorumLatencyMs is the total fan-out time for ExecuteChallenge across
	// every endpoint, measured by the QuorumClient itself.
	QuorumLatencyMs = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "depin_quorum_latency_ms",
			Help:    "Total fan-out time for QuorumClient.ExecuteChallenge in milliseconds.",
			Buckets: []float64{25, 50, 100, 200, 500, 1000, 2000, 5000, 10000},
		},
	)

	// HTTPRequestSeconds records per-request handler duration. Labels:
	//   route   = a normalised path template (e.g. "/api/nodes/:nodeId")
	//   method  = HTTP method (GET, POST, ...)
	//   status  = status_class (2xx, 3xx, 4xx, 5xx) — full status code is too
	//             high cardinality for a histogram label
	HTTPRequestSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "depin_http_request_seconds",
			Help:    "HTTP handler duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"route", "method", "status"},
	)
)

// init registers everything with the default Prometheus registry. Using the
// default registry keeps promhttp.Handler() (the one Handler() returns)
// trivially correct.
func init() {
	prometheus.MustRegister(
		// Counters
		ChallengesTotal,
		QuorumFailuresTotal,
		RateLimit429Total,
		NonceReplaysTotal,
		SSRFRejectionsTotal,
		SnapshotsPublished,
		SnapshotBuildFailures,
		ClaimLookupsTotal,
		SuspiciousEventsTotal,
		AdminActionsTotal,
		// Gauges
		NodesActive,
		NodesByStatus,
		NodesByType,
		LastSnapshotPublished,
		SchedulerLastTick,
		RPCWorkerInflight,
		RPCWorkerCapacity,
		// Histograms
		VerificationLatencyMs,
		SnapshotBuildSeconds,
		QuorumLatencyMs,
		HTTPRequestSeconds,
	)
}

// Handler returns the http.Handler that exposes the Prometheus text exposition
// format. router.go mounts it at /metrics with no rate limit and no CORS.
func Handler() http.Handler {
	return promhttp.Handler()
}

// SchedulerLastTickValue returns the unix-second timestamp the named ticker
// last completed at, or 0 if it never has. Used by the /ready handler so we
// don't have to hold a back-reference from api/ into scheduler/.
//
// ticker must be one of: "heartbeat", "challenge", "uptime", "snapshot".
// Anything else returns 0 — there is no labelled child for it.
func SchedulerLastTickValue(ticker string) int64 {
	g := SchedulerLastTick.WithLabelValues(ticker)
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		return 0
	}
	if m.Gauge == nil || m.Gauge.Value == nil {
		return 0
	}
	return int64(*m.Gauge.Value)
}

// BucketSuspiciousReason maps a free-form reason string into a small fixed
// label set. The store's reason field is operator-/verifier-supplied and
// therefore unbounded; raw reasons would explode the label cardinality.
//
// Keep the keys aligned with the anti-cheat documentation in SPEC §7.
func BucketSuspiciousReason(reason string) string {
	r := strings.ToLower(reason)
	switch {
	case containsAny(r, "latency", "timeout", "slow", "too fast", "too slow"):
		return "latency"
	case containsAny(r, "answer mismatch", "mismatch", "did not match", "doesn't match", "differs"):
		return "answer_mismatch"
	case containsAny(r, "missing node", "not found", "no such node"):
		return "missing_node"
	default:
		return "other"
	}
}

// containsAny reports whether s contains any of substrs.
func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if sub == "" {
			continue
		}
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// StatusClass maps an HTTP status code to a "2xx"/"3xx"/"4xx"/"5xx" label.
// Keeps cardinality bounded for the HTTPRequestSeconds histogram.
func StatusClass(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500 && code < 600:
		return "5xx"
	default:
		return "other"
	}
}
