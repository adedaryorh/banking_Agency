package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// JWTMiddleware validates JWT tokens issued by identity-svc
type JWTMiddleware struct {
	secretKey []byte
	issuer    string
}

const (
	UserIDKey     = "user_id"
	CustomerIDKey = "customer_id"
	UserRoleKey   = "user_role"
)

// creates a new JWT middleware that validates tokens from identity-svc
func NewJWTMiddleware(secretKey, issuer string) *JWTMiddleware {
	return &JWTMiddleware{
		secretKey: []byte(secretKey),
		issuer:    issuer,
	}
}

// Authenticate returns a Gin middleware that validates JWT tokens from identity-svc
// Token structure: {"sub": "user_id", "role": "creator|buyer|admin", "iss": "issuer", "exp": timestamp}
func (m *JWTMiddleware) Authenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract token from Authorization header
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "authorization header required",
				"code":  "missing_token",
			})
			c.Abort()
			return
		}

		// Check Bearer prefix
		if !strings.HasPrefix(authHeader, "Bearer ") {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "invalid authorization format, use: Bearer <token>",
				"code":  "invalid_format",
			})
			c.Abort()
			return
		}

		tokenString := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))

		// Parse and validate token (same way as identity-svc)
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			// Validate signing method
			if token.Method != jwt.SigningMethodHS256 {
				return nil, errors.New("unexpected signing method")
			}
			return m.secretKey, nil
		}, jwt.WithIssuer(m.issuer), jwt.WithExpirationRequired(), jwt.WithValidMethods([]string{"HS256"}))

		if err != nil || !token.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "invalid or expired token",
				"code":  "invalid_token",
			})
			c.Abort()
			return
		}

		// Extract claims (identity-svc uses MapClaims)
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "invalid token claims",
				"code":  "invalid_claims",
			})
			c.Abort()
			return
		}

		// Extract user ID from "sub" claim (identity-svc standard)
		subStr, ok := claims["sub"].(string)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "missing sub claim in token",
				"code":  "missing_sub",
			})
			c.Abort()
			return
		}

		userID, err := uuid.Parse(subStr)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "invalid user_id in token",
				"code":  "invalid_user_id",
			})
			c.Abort()
			return
		}
		role, _ := claims["role"].(string)
		c.Set(UserIDKey, userID)
		c.Set(UserRoleKey, role)

		// For transfers-svc, user_id IS the customer_id
		// (In identity-svc, a user can have multiple customers, but for simplicity we'll use user_id)
		// If you need separate customer lookup, add a database query here
		c.Set(CustomerIDKey, userID)

		c.Next()
	}
}

// GetUserID extracts user ID from context (set by Authenticate middleware)
func GetUserID(c *gin.Context) (uuid.UUID, bool) {
	value, exists := c.Get(UserIDKey)
	if !exists {
		return uuid.Nil, false
	}
	userID, ok := value.(uuid.UUID)
	return userID, ok
}

// GetCustomerID extracts customer ID from context
func GetCustomerID(c *gin.Context) (uuid.UUID, bool) {
	value, exists := c.Get(CustomerIDKey)
	if !exists {
		return uuid.Nil, false
	}
	customerID, ok := value.(uuid.UUID)
	return customerID, ok
}

// GetUserRole extracts user role from context
func GetUserRole(c *gin.Context) (string, bool) {
	value, exists := c.Get(UserRoleKey)
	if !exists {
		return "", false
	}
	role, ok := value.(string)
	return role, ok
}
