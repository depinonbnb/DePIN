package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandlerExposesRegisteredMetric verifies a counter increment becomes
// visible to a Prometheus scrape against the metrics handler. We assert on
// substring presence rather than exact text so future buckets / help-string
// edits don't break the test.
func TestHandlerExposesRegisteredMetric(t *testing.T) {
	t.Parallel()

	// Touch every family at least once so each one appears in the text
	// exposition. Counters and gauges show up as soon as they're registered,
	// but histograms only render after the first Observe — same pattern for
	// labelled vec children. The asserts below rely on the metric NAMES being
	// present in the body, not specific values, so a single touch per family
	// is enough.
	ChallengesTotal.WithLabelValues("passed", "exposed-rpc", "bsc-full").Inc()
	QuorumFailuresTotal.Inc()
	RateLimit429Total.WithLabelValues("global").Inc()
	NonceReplaysTotal.Inc()
	SSRFRejectionsTotal.Inc()
	SnapshotsPublished.Inc()
	SnapshotBuildFailures.Inc()
	ClaimLookupsTotal.WithLabelValues("found").Inc()
	SuspiciousEventsTotal.WithLabelValues("latency").Inc()
	AdminActionsTotal.WithLabelValues("clear").Inc()
	NodesActive.Set(1)
	NodesByStatus.WithLabelValues("clean").Set(1)
	NodesByType.WithLabelValues("bsc-full").Set(1)
	LastSnapshotPublished.SetToCurrentTime()
	SchedulerLastTick.WithLabelValues("heartbeat").SetToCurrentTime()
	SchedulerLastTick.WithLabelValues("snapshot").SetToCurrentTime()
	RPCWorkerInflight.Set(0)
	RPCWorkerCapacity.Set(50)
	VerificationLatencyMs.Observe(42)
	SnapshotBuildSeconds.Observe(0.1)
	QuorumLatencyMs.Observe(120)
	HTTPRequestSeconds.WithLabelValues("/health", "GET", "2xx").Observe(0.001)

	srv := httptest.NewServer(Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	body := readAll(t, resp)
	for _, want := range []string{
		"depin_challenges_total",
		"depin_nonce_replays_total",
		"depin_quorum_failures_total",
		"depin_rate_limit_429_total",
		"depin_ssrf_rejections_total",
		"depin_snapshots_published_total",
		"depin_snapshot_build_failures_total",
		"depin_claim_lookups_total",
		"depin_suspicious_events_total",
		"depin_admin_actions_total",
		"depin_nodes_active",
		"depin_nodes_by_status",
		"depin_nodes_by_type",
		"depin_last_snapshot_published_seconds",
		"depin_scheduler_last_tick_seconds",
		"depin_rpc_worker_inflight",
		"depin_rpc_worker_capacity",
		"depin_verification_latency_ms",
		"depin_snapshot_build_seconds",
		"depin_quorum_latency_ms",
		"depin_http_request_seconds",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape body missing %q", want)
		}
	}
}

func TestBucketSuspiciousReason(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"latency too high":              "latency",
		"answer mismatch with quorum":   "answer_mismatch",
		"missing node, untracked id":    "missing_node",
		"timeout exceeded":              "latency",
		"unknown thing":                 "other",
		"":                              "other",
		"answer differs from majority":  "answer_mismatch",
	}
	for in, want := range cases {
		if got := BucketSuspiciousReason(in); got != want {
			t.Errorf("BucketSuspiciousReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStatusClass(t *testing.T) {
	t.Parallel()
	cases := map[int]string{
		200: "2xx",
		201: "2xx",
		301: "3xx",
		400: "4xx",
		404: "4xx",
		500: "5xx",
		503: "5xx",
		100: "other",
		999: "other",
	}
	for in, want := range cases {
		if got := StatusClass(in); got != want {
			t.Errorf("StatusClass(%d) = %q, want %q", in, got, want)
		}
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(body)
}
