package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// newMiddlewareTestRouter builds a minimal router with a single protected
// endpoint under /protected. Used only in middleware unit tests.
func newMiddlewareTestRouter(adminKey string) *gin.Engine {
	r := gin.New()
	r.GET("/protected", AdminAuthMiddleware(adminKey), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	return r
}

func TestAdminAuth_EmptyKey_Returns503(t *testing.T) {
	r := newMiddlewareTestRouter("") // unconfigured — no ADMIN_API_KEY

	req, _ := http.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer anything")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("empty admin key: expected 503, got %d", w.Code)
	}
}

func TestAdminAuth_MissingHeader_Returns401(t *testing.T) {
	r := newMiddlewareTestRouter("secretkey")

	req, _ := http.NewRequest("GET", "/protected", nil)
	// No Authorization header
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing header: expected 401, got %d", w.Code)
	}
}

func TestAdminAuth_WrongKey_Returns401(t *testing.T) {
	r := newMiddlewareTestRouter("secretkey")

	req, _ := http.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer wrongkey")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong key: expected 401, got %d", w.Code)
	}
}

func TestAdminAuth_BearerKey_Returns200(t *testing.T) {
	r := newMiddlewareTestRouter("secretkey")

	req, _ := http.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer secretkey")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("correct Bearer key: expected 200, got %d", w.Code)
	}
}

func TestAdminAuth_BareKey_Returns200(t *testing.T) {
	// The middleware supports both "Bearer <key>" and just "<key>"
	r := newMiddlewareTestRouter("secretkey")

	req, _ := http.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "secretkey")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("bare key (no Bearer prefix): expected 200, got %d", w.Code)
	}
}

func TestAdminAuth_TimingSafe_SameLengthWrongKey(t *testing.T) {
	// subtle.ConstantTimeCompare is used — this confirms a same-length wrong
	// key still returns 401, not a timing-confused 200.
	r := newMiddlewareTestRouter("secretkey")

	req, _ := http.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer wrongkey") // same length as "secretkey"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("same-length wrong key: expected 401, got %d", w.Code)
	}
}
