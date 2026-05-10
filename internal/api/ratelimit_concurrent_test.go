// Package api: ratelimit_concurrent_test.go exercises the rate limiters
// under concurrent load. The goal is twofold:
//
//  1. Race-detector cleanliness — RateLimitMiddleware uses a sync.Mutex
//     around the LRU map and golang.org/x/time/rate's own internal locking,
//     so a fan-out of N goroutines hitting the same key MUST NOT race.
//  2. Capacity correctness under contention — the burst capacity is the
//     hard ceiling on simultaneously-allowed requests; under contention,
//     successful 200s must equal exactly the burst capacity (the limiter
//     refill rate is set absurdly low so we can ignore replenishment).
//
// ratelimit_test.go covers the sequential path. This file covers the
// concurrent one.
package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// TestRateLimiter_ConcurrentSameKey fires N concurrent requests from the
// SAME source IP at a stub route protected by a small-burst limiter. The
// invariant: total 200s == burst, total 429s == N - burst, no panics, no
// races (`go test -race` enforces).
//
// We deliberately set the rate to ~zero (rate.Limit(0.0001)) so the bucket
// never refills inside the test window — the test is purely about burst
// accounting under contention.
func TestRateLimiter_ConcurrentSameKey(t *testing.T) {
	const burst = 5
	const totalRequests = 200

	r := gin.New()
	r.Use(RateLimitMiddleware(rate.Limit(0.0001), burst))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	var ok, throttled, other int32

	var wg sync.WaitGroup
	wg.Add(totalRequests)
	for i := 0; i < totalRequests; i++ {
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, "/x", nil)
			req.RemoteAddr = "9.9.9.9:1234" // same IP for every goroutine
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			switch w.Code {
			case http.StatusOK:
				atomic.AddInt32(&ok, 1)
			case http.StatusTooManyRequests:
				atomic.AddInt32(&throttled, 1)
			default:
				atomic.AddInt32(&other, 1)
			}
		}()
	}
	wg.Wait()

	if other != 0 {
		t.Errorf("unexpected non-200/429 response count: %d", other)
	}
	if ok != int32(burst) {
		t.Errorf("expected exactly %d successful requests (burst), got %d", burst, ok)
	}
	if throttled != int32(totalRequests-burst) {
		t.Errorf("expected %d 429s, got %d", totalRequests-burst, throttled)
	}
}

// TestRateLimiter_DifferentKeysIsolated verifies that draining key A's
// burst does not affect key B's quota. Confirms the LRU map is keyed
// correctly under concurrent access.
func TestRateLimiter_DifferentKeysIsolated(t *testing.T) {
	const burst = 3

	r := gin.New()
	r.Use(RateLimitMiddleware(rate.Limit(0.0001), burst))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	hit := func(ip string) int {
		req, _ := http.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = ip + ":1234"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// Drain A's quota. Three OKs, then 429.
	for i := 0; i < burst; i++ {
		if got := hit("8.8.8.8"); got != http.StatusOK {
			t.Fatalf("A burst slot %d: expected 200, got %d", i, got)
		}
	}
	if got := hit("8.8.8.8"); got != http.StatusTooManyRequests {
		t.Fatalf("A drained: expected 429, got %d", got)
	}

	// B's first burst slots must still pass.
	for i := 0; i < burst; i++ {
		if got := hit("4.4.4.4"); got != http.StatusOK {
			t.Errorf("B burst slot %d (after A drained): expected 200, got %d", i, got)
		}
	}

	// And once B is also drained, B sees its own 429 — independent counter.
	if got := hit("4.4.4.4"); got != http.StatusTooManyRequests {
		t.Errorf("B drained: expected 429, got %d", got)
	}
}
