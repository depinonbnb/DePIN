package api

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// AdminAuthMiddleware enforces admin-key auth on the admin routes.
//
// Behaviour:
//   - If configuredKey is empty, the middleware fails closed: every request
//     receives 503 with a clear "admin endpoints disabled" body. This guards
//     against accidental deploys where ADMIN_API_KEY is unset.
//   - Otherwise it reads the Authorization header (supporting both the bare
//     key and "Bearer <key>" forms, preserving previous behaviour) and uses
//     crypto/subtle.ConstantTimeCompare to defeat timing attacks.
func AdminAuthMiddleware(configuredKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if configuredKey == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "admin endpoints disabled — ADMIN_API_KEY not configured",
			})
			c.Abort()
			return
		}

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
			c.Abort()
			return
		}

		// Support both "Bearer <key>" and just "<key>"
		token := authHeader
		if strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimPrefix(authHeader, "Bearer ")
		}

		if subtle.ConstantTimeCompare([]byte(token), []byte(configuredKey)) != 1 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid api key"})
			c.Abort()
			return
		}

		c.Next()
	}
}
