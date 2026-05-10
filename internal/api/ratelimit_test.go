// Package api: ratelimit_test.go covers the per-IP and per-wallet rate
// limiters added in Phase 3. We use Gin's TestMode (set in handlers_test.go's
// init) and httptest to drive synthetic traffic.
package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// TestRateLimitMiddleware_BlocksAfterBurst fires N rapid requests against a
// stub route protected by a small-burst limiter and verifies the limiter
// allows exactly `burst` and rejects the rest with 429.
func TestRateLimitMiddleware_BlocksAfterBurst(t *testing.T) {
	r := gin.New()
	// rate=zero so refill is effectively never within the test window; only
	// the burst counts. Burst=5.
	r.Use(RateLimitMiddleware(rate.Limit(0.0001), 5))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	statuses := make(map[int]int)
	for i := 0; i < 50; i++ {
		req, _ := http.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "1.2.3.4:1234"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		statuses[w.Code]++
	}

	if statuses[http.StatusOK] != 5 {
		t.Errorf("expected exactly 5 OKs (the burst), got %d", statuses[http.StatusOK])
	}
	if statuses[http.StatusTooManyRequests] != 45 {
		t.Errorf("expected 45 429s, got %d", statuses[http.StatusTooManyRequests])
	}
}

// TestRateLimitMiddleware_PerIPIsolation verifies that two different IPs
// don't share a limiter — each gets its own bucket.
func TestRateLimitMiddleware_PerIPIsolation(t *testing.T) {
	r := gin.New()
	r.Use(RateLimitMiddleware(rate.Limit(0.0001), 2))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	hit := func(ip string) int {
		req, _ := http.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = ip + ":1"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// Burn through IP-A's burst.
	hit("9.9.9.1")
	hit("9.9.9.1")
	if got := hit("9.9.9.1"); got != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on 3rd hit from IP-A, got %d", got)
	}

	// IP-B should still pass twice.
	if got := hit("9.9.9.2"); got != http.StatusOK {
		t.Errorf("IP-B 1st hit expected 200, got %d", got)
	}
	if got := hit("9.9.9.2"); got != http.StatusOK {
		t.Errorf("IP-B 2nd hit expected 200, got %d", got)
	}
}

// TestRateLimitMiddleware_RetryAfterHeader verifies the 429 carries a
// Retry-After header so well-behaved clients can back off.
func TestRateLimitMiddleware_RetryAfterHeader(t *testing.T) {
	r := gin.New()
	r.Use(RateLimitMiddleware(rate.Limit(1), 1))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	// First passes.
	req, _ := http.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "5.5.5.5:1"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first req should pass, got %d", w.Code)
	}

	// Second is rejected with Retry-After.
	req2, _ := http.NewRequest(http.MethodGet, "/x", nil)
	req2.RemoteAddr = "5.5.5.5:1"
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second req should be 429, got %d", w2.Code)
	}
	if w2.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header on 429")
	}
}
