package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	"nabla/identity-svc/internal/models"
)

const (
	AuthUserIDKey   = "auth_user_id"
	AuthUserRoleKey = "auth_user_role"
)

func Authenticate(secret, issuer string) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := strings.TrimSpace(c.GetHeader("Authorization"))
		if !strings.HasPrefix(header, "Bearer ") {
			abortFailure(c, http.StatusUnauthorized, messages.AuthenticationRequired, messages.CodeAuthenticationRequired)
			return
		}
		raw := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		token, err := jwt.Parse(raw, func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS256 {
				return nil, errors.New("unexpected signing method")
			}
			return []byte(secret), nil
		}, jwt.WithIssuer(issuer), jwt.WithExpirationRequired(), jwt.WithValidMethods([]string{"HS256"}))
		if err != nil || !token.Valid {
			abortFailure(c, http.StatusUnauthorized, messages.InvalidAccessToken, messages.CodeInvalidAccessToken)
			return
		}
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			abortFailure(c, http.StatusUnauthorized, messages.InvalidAccessClaims, messages.CodeInvalidAccessToken)
			return
		}
		userID, parseErr := uuid.Parse(claimString(claims, "sub"))
		role := models.UserRole(claimString(claims, "role"))
		if parseErr != nil || !validRole(role) {
			abortFailure(c, http.StatusUnauthorized, messages.InvalidAccessClaims, messages.CodeInvalidAccessToken)
			return
		}
		c.Set(AuthUserIDKey, userID)
		c.Set(AuthUserRoleKey, role)
		c.Next()
	}
}

func RequireRoles(roles ...models.UserRole) gin.HandlerFunc {
	allowed := make(map[models.UserRole]struct{}, len(roles))
	for _, role := range roles {
		allowed[role] = struct{}{}
	}
	return func(c *gin.Context) {
		role, ok := CurrentUserRole(c)
		if !ok {
			abortFailure(c, http.StatusUnauthorized, messages.AuthenticationRequired, messages.CodeAuthenticationRequired)
			return
		}
		if _, ok := allowed[role]; !ok {
			abortFailure(c, http.StatusForbidden, messages.PermissionDenied, messages.CodeForbidden)
			return
		}
		c.Next()
	}
}

func abortFailure(c *gin.Context, status int, message, code string) {
	helpers.Failure(c, status, message, gin.H{"code": code})
	c.Abort()
}

func CurrentUserID(c *gin.Context) (uuid.UUID, bool) {
	value, exists := c.Get(AuthUserIDKey)
	id, ok := value.(uuid.UUID)
	return id, exists && ok
}
func CurrentUserRole(c *gin.Context) (models.UserRole, bool) {
	value, exists := c.Get(AuthUserRoleKey)
	role, ok := value.(models.UserRole)
	return role, exists && ok
}
func claimString(claims jwt.MapClaims, key string) string {
	value, _ := claims[key].(string)
	return value
}
func validRole(role models.UserRole) bool {
	return role == models.UserRoleCreator || role == models.UserRoleBuyer || role == models.UserRoleAdmin
}
