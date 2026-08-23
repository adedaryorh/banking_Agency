package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/gin-gonic/gin"
)

// AdminAuth gates a route behind a shared-secret API key, compared in
// constant time so response timing cannot leak how many prefix bytes of a
// guess matched.
//
// An empty apiKey refuses every request rather than admitting all of them —
// an admin surface that is reachable because a setting was left blank is not
// a safe default, it is an open one. This is the FIRST of two checks a
// request under /admin/* passes: it is proxied on to the owning service,
// which re-validates the same key against its own ADMIN_API_KEY.
func AdminAuth(apiKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if apiKey == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "the admin API is not configured on this gateway"})
			c.Abort()
			return
		}
		given := c.GetHeader("X-Admin-Api-Key")
		if given == "" || subtle.ConstantTimeCompare([]byte(given), []byte(apiKey)) != 1 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or missing admin API key"})
			c.Abort()
			return
		}
		c.Next()
	}
}
