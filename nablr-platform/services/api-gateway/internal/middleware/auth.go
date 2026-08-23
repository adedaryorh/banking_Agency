package middleware

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const (
	GatewayUserIDKey   = "auth_user_id"
	GatewayUserRoleKey = "auth_user_role"
)

var uuidClaimPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

// Claims mirrors the access token issued by Identity. User identity is the
// registered `sub` claim; it is deliberately not duplicated as `user_id`.
type Claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

func JWTAuth(jwtSecret, issuer string) gin.HandlerFunc {
	return func(c *gin.Context) {
		stripUntrustedIdentityHeaders(c)
		claims, ok := authenticateBearer(c.GetHeader("Authorization"), jwtSecret, issuer)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired access token"})
			c.Abort()
			return
		}
		applyValidatedIdentity(c, claims)
		c.Next()
	}
}

func OptionalAuth(jwtSecret, issuer string) gin.HandlerFunc {
	return func(c *gin.Context) {
		stripUntrustedIdentityHeaders(c)
		if strings.TrimSpace(c.GetHeader("Authorization")) != "" {
			if claims, ok := authenticateBearer(c.GetHeader("Authorization"), jwtSecret, issuer); ok {
				applyValidatedIdentity(c, claims)
			}
		}
		c.Next()
	}
}

func authenticateBearer(header, secret, issuer string) (*Claims, bool) {
	if strings.TrimSpace(secret) == "" || strings.TrimSpace(issuer) == "" || !strings.HasPrefix(header, "Bearer ") {
		return nil, false
	}
	raw := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if raw == "" {
		return nil, false
	}
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		return []byte(secret), nil
	}, jwt.WithIssuer(issuer), jwt.WithExpirationRequired(), jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !token.Valid || !uuidClaimPattern.MatchString(claims.Subject) || !validRole(claims.Role) {
		return nil, false
	}
	return claims, true
}

func applyValidatedIdentity(c *gin.Context, claims *Claims) {
	c.Set(GatewayUserIDKey, claims.Subject)
	c.Set(GatewayUserRoleKey, claims.Role)
	c.Request.Header.Set("X-User-ID", claims.Subject)
	c.Request.Header.Set("X-User-Role", claims.Role)
}

func stripUntrustedIdentityHeaders(c *gin.Context) {
	for _, header := range []string{"X-User-ID", "X-User-Role", "X-User-Email", "X-KYC-Tier"} {
		c.Request.Header.Del(header)
	}
}

func validRole(role string) bool { return role == "creator" || role == "buyer" || role == "admin" }
