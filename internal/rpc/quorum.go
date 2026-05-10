// Package rpc: quorum.go adds the quorum wrapper described in ADR-0009.
//
// Background: ADR-0005 accepted a single TRUSTED_RPC as the source of truth.
// ADR-0009 supersedes that — verification answers are now taken as the
// majority of N independent endpoints. With 3 endpoints that's 2-of-3; with
// 1 endpoint we silently degrade to ADR-0005 behavior (acceptable for local
// dev, dangerous in prod — we log a warning on construction).
//
// QuorumClient is a drop-in replacement for *Client at the call sites that
// used to talk to the trusted RPC: same ExecuteChallenge surface, same
// RpcResponse shape, but with a third outcome — "no quorum" — surfaced via
// RpcResponse{Success: false, Error: "no quorum"}. The verifier treats that
// as ABORT (don't punish the operator) rather than as a failure.
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/depinonbnb/depin/internal/metrics"
	"github.com/depinonbnb/depin/internal/types"
)

// QuorumNoMajority is the canonical Error string returned when no answer
// reaches a strict majority. Callers (verifier, tests) compare against this
// constant to distinguish "the operator was wrong" from "we couldn't agree".
const QuorumNoMajority = "no quorum"

// QuorumClient fans an ExecuteChallenge call out to N underlying *Client
// instances and returns the majority response. Construction is via NewQuorum.
type QuorumClient struct {
	clients   []*Client
	endpoints []string
	logger    *slog.Logger
}

// NewQuorum builds a QuorumClient over the given endpoints. Each endpoint
// gets its own *Client (and therefore its own http.Client / pool). If the
// caller passes a single endpoint we still return a usable QuorumClient but
// emit a warning — single-source verification was the failure mode that
// motivated ADR-0009.
//
// An empty endpoints slice is treated as a configuration error: we return a
// QuorumClient that always answers "no quorum" so the verifier can keep
// running rather than nil-panic.
func NewQuorum(endpoints []string) *QuorumClient {
	logger := slog.Default()

	cleaned := make([]string, 0, len(endpoints))
	for _, e := range endpoints {
		e = strings.TrimSpace(e)
		if e != "" {
			cleaned = append(cleaned, e)
		}
	}

	clients := make([]*Client, 0, len(cleaned))
	for _, e := range cleaned {
		clients = append(clients, NewClient(e, ""))
	}

	switch len(cleaned) {
	case 0:
		logger.Warn("rpc: QuorumClient constructed with zero endpoints — every ExecuteChallenge will return 'no quorum'")
	case 1:
		logger.Warn("rpc: QuorumClient configured with a single endpoint — degrades to ADR-0005 single-source behavior; set TRUSTED_RPCS to 3+ for production",
			"endpoint", cleaned[0])
	}

	return &QuorumClient{
		clients:   clients,
		endpoints: cleaned,
		logger:    logger,
	}
}

// ExecuteChallenge fans out across every configured endpoint, normalizes the
// answers per challenge type, and returns the majority response. Returns
// RpcResponse{Success: false, Error: QuorumNoMajority} when no answer reaches
// floor(N/2) + 1.
func (q *QuorumClient) ExecuteChallenge(challenge *types.Challenge) RpcResponse {
	start := time.Now()
	defer func() {
		metrics.QuorumLatencyMs.Observe(float64(time.Since(start).Milliseconds()))
	}()

	if len(q.clients) == 0 {
		metrics.QuorumFailuresTotal.Inc()
		return RpcResponse{Success: false, Error: QuorumNoMajority}
	}

	results := make([]quorumResult, len(q.clients))
	var wg sync.WaitGroup
	wg.Add(len(q.clients))
	for i, c := range q.clients {
		i, c := i, c
		go func() {
			defer wg.Done()
			results[i] = quorumResult{endpoint: q.endpoints[i], resp: c.ExecuteChallenge(challenge)}
		}()
	}
	wg.Wait()

	// Bucket by canonicalized answer. Failures don't bucket — they're
	// abstentions. We tally the largest bucket and require a strict majority
	// over the FULL endpoint set (not just over the responders). This is more
	// conservative than "majority of those who answered" and matches the
	// ADR-0009 contract for the no-majority abort case.
	type bucket struct {
		canonical string   // normalized form used for grouping
		raw       []string // raw .Data strings, kept for the winning answer
		latency   uint64   // representative latency (first responder)
		count     int
	}
	buckets := make(map[string]*bucket)
	successResponders := 0
	for _, r := range results {
		if !r.resp.Success {
			continue
		}
		successResponders++
		canon := canonicalAnswer(challenge.ChallengeType, r.resp.Data)
		b, ok := buckets[canon]
		if !ok {
			b = &bucket{canonical: canon, latency: r.resp.LatencyMs}
			buckets[canon] = b
		}
		b.raw = append(b.raw, r.resp.Data)
		b.count++
	}

	// Find the largest bucket.
	var winner *bucket
	for _, b := range buckets {
		if winner == nil || b.count > winner.count {
			winner = b
		}
	}

	majority := len(q.clients)/2 + 1
	if winner == nil || winner.count < majority {
		metrics.QuorumFailuresTotal.Inc()
		// Build a debug-friendly summary for ops.
		summary := summarizeForLog(results)
		q.logger.Warn("rpc: quorum_failure — no majority answer",
			"challenge_type", string(challenge.ChallengeType),
			"endpoints_total", len(q.clients),
			"endpoints_responded", successResponders,
			"majority_required", majority,
			"answers", summary,
		)
		return RpcResponse{Success: false, Error: QuorumNoMajority}
	}

	// Log any minority dissenters for ops visibility.
	if successResponders > winner.count {
		q.logger.Warn("rpc: quorum_dissent — minority disagreement against majority",
			"challenge_type", string(challenge.ChallengeType),
			"majority_count", winner.count,
			"endpoints_total", len(q.clients),
			"answers", summarizeForLog(results),
		)
	}

	// Pick a representative raw answer from the winning bucket. The first
	// response in the bucket is fine — we already know they all canonicalize
	// equal.
	return RpcResponse{Success: true, Data: winner.raw[0], LatencyMs: winner.latency}
}

// canonicalAnswer converts a per-endpoint response string into the form used
// for majority grouping. Comparison is type-aware:
//
//   - BlockHash: hex, lowercase + trim "0x"
//   - StateBalance: parse as big.Int (hex), compare numerically
//   - BlockData / SyncStatus / TxReceipt: parse JSON object, re-marshal in
//     canonical key order
//
// Unknown types fall through to a trimmed-lowercase string.
func canonicalAnswer(challengeType types.ChallengeType, raw string) string {
	switch challengeType {
	case types.BlockHash:
		s := strings.ToLower(strings.TrimSpace(raw))
		return strings.TrimPrefix(s, "0x")

	case types.StateBalance:
		s := strings.ToLower(strings.TrimSpace(raw))
		s = strings.TrimPrefix(s, "0x")
		if v, ok := new(big.Int).SetString(s, 16); ok {
			return v.Text(16)
		}
		return s

	case types.BlockData, types.SyncStatus, types.TxReceipt:
		return canonicalJSON(raw)

	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

// canonicalJSON parses a JSON object/array and re-marshals it with sorted keys
// for stable comparison. Falls back to the lowercased trimmed input if the
// payload isn't parseable JSON.
func canonicalJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var v interface{}
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return strings.ToLower(trimmed)
	}
	out, err := marshalSorted(v)
	if err != nil {
		return strings.ToLower(trimmed)
	}
	return out
}

// marshalSorted writes a JSON value with map keys sorted lexicographically so
// `{"a":1,"b":2}` and `{"b":2,"a":1}` produce the same canonical string.
func marshalSorted(v interface{}) (string, error) {
	switch t := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteByte(':')
			child, err := marshalSorted(t[k])
			if err != nil {
				return "", err
			}
			b.WriteString(child)
		}
		b.WriteByte('}')
		return b.String(), nil
	case []interface{}:
		var b strings.Builder
		b.WriteByte('[')
		for i, elem := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			child, err := marshalSorted(elem)
			if err != nil {
				return "", err
			}
			b.WriteString(child)
		}
		b.WriteByte(']')
		return b.String(), nil
	default:
		bytes, err := json.Marshal(t)
		if err != nil {
			return "", err
		}
		return string(bytes), nil
	}
}

// quorumResult pairs an endpoint URL with its response for fan-out collection.
type quorumResult struct {
	endpoint string
	resp     RpcResponse
}

// summarizeForLog produces a per-endpoint slice of short answer prefixes for
// debug logging. Truncated so log lines stay readable.
func summarizeForLog(results []quorumResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		if !r.resp.Success {
			out = append(out, r.endpoint+": ERR "+truncate(r.resp.Error, 64))
			continue
		}
		out = append(out, r.endpoint+": "+truncate(r.resp.Data, 64))
	}
	return out
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// Endpoints returns the (cleaned) endpoint list this quorum was built from.
// Useful for logging / introspection in cmd/server.
func (q *QuorumClient) Endpoints() []string {
	out := make([]string, len(q.endpoints))
	copy(out, q.endpoints)
	return out
}

// HealthProbe is one row of HealthCheck's per-endpoint result. Err is the
// driver / network error (if any); LatencyMs is the wall-clock time the probe
// took regardless of outcome (so a slow-but-reachable endpoint is observable).
type HealthProbe struct {
	Endpoint  string
	OK        bool
	Err       string
	LatencyMs uint64
}

// HealthCheck pings every configured endpoint with eth_blockNumber under a
// tight per-endpoint timeout (1s). The probes run concurrently so total
// wall-clock time is bounded by the slowest single endpoint, not their sum.
//
// HealthCheck is intentionally separate from ExecuteChallenge: the readiness
// path only needs "can we reach this RPC?", not "do the RPCs agree?". A single
// flaky endpoint shows up as OK=false here without poisoning the verifier's
// quorum logic.
func (q *QuorumClient) HealthCheck(ctx context.Context) []HealthProbe {
	if ctx == nil {
		ctx = context.Background()
	}
	probes := make([]HealthProbe, len(q.endpoints))
	if len(q.endpoints) == 0 {
		return probes
	}

	var wg sync.WaitGroup
	wg.Add(len(q.endpoints))
	for i, ep := range q.endpoints {
		i, ep := i, ep
		go func() {
			defer wg.Done()
			probes[i] = healthProbe(ctx, ep)
		}()
	}
	wg.Wait()
	return probes
}

// healthProbe performs a single eth_blockNumber JSON-RPC POST against ep with
// a 1s timeout (or the caller's deadline, whichever is sooner).
func healthProbe(ctx context.Context, ep string) HealthProbe {
	probeCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	start := time.Now()

	body, _ := json.Marshal(jsonRpcRequest{
		Jsonrpc: "2.0",
		ID:      1,
		Method:  "eth_blockNumber",
		Params:  []interface{}{},
	})
	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, ep, bytes.NewReader(body))
	if err != nil {
		return HealthProbe{Endpoint: ep, OK: false, Err: "build_request: " + err.Error(), LatencyMs: uint64(time.Since(start).Milliseconds())}
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return HealthProbe{Endpoint: ep, OK: false, Err: err.Error(), LatencyMs: uint64(time.Since(start).Milliseconds())}
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return HealthProbe{Endpoint: ep, OK: false, Err: "read_body: " + err.Error(), LatencyMs: uint64(time.Since(start).Milliseconds())}
	}
	if resp.StatusCode/100 != 2 {
		return HealthProbe{Endpoint: ep, OK: false, Err: fmt.Sprintf("http %d", resp.StatusCode), LatencyMs: uint64(time.Since(start).Milliseconds())}
	}
	var rpcResp jsonRpcResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return HealthProbe{Endpoint: ep, OK: false, Err: "decode: " + err.Error(), LatencyMs: uint64(time.Since(start).Milliseconds())}
	}
	if rpcResp.Error != nil {
		return HealthProbe{Endpoint: ep, OK: false, Err: rpcResp.Error.Message, LatencyMs: uint64(time.Since(start).Milliseconds())}
	}
	return HealthProbe{Endpoint: ep, OK: true, LatencyMs: uint64(time.Since(start).Milliseconds())}
}
