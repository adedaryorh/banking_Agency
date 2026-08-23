package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	"nabla/identity-svc/internal/models"
)

const (
	IdempotencyKeyHeader = "Idempotency-Key"
	IdempotencyKeyCtx    = "idempotency_key"
)

//	attaches the caller's identity and request details to the
//
// request context, for audit log
func RequestMetadata() gin.HandlerFunc {
	return func(c *gin.Context) {
		meta := models.RequestMetadata{
			ActorType:  models.AuditActorSystem,
			RequestID:  c.GetString("request_id"),
			IPAddress:  c.ClientIP(),
			UserAgent:  c.Request.UserAgent(),
			DeviceID:   deviceID(c),
			DeviceName: strings.TrimSpace(c.GetHeader("X-Device-Name")),
			DeviceIP:   strings.TrimSpace(c.GetHeader("X-Device-IP")),
		}
		if userID, ok := CurrentUserID(c); ok {
			meta.ActorType = models.AuditActorUser
			id := userID
			meta.ActorID = &id
			if role, ok := CurrentUserRole(c); ok && role == models.UserRoleAdmin {
				meta.ActorType = models.AuditActorAdmin
			}
		}

		c.Request = c.Request.WithContext(models.MetadataWithContext(c.Request.Context(), meta))
		c.Next()
	}
}

func deviceID(c *gin.Context) string {
	return firstNonEmptyHeader(c, "X-Device-ID", "X-Device-Id", "Device-ID", "Device-Id")
}

func firstNonEmptyHeader(c *gin.Context, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(c.GetHeader(key)); value != "" {
			return value
		}
	}
	return ""
}

func RequireIdempotencyKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := strings.TrimSpace(c.GetHeader(IdempotencyKeyHeader))
		if key == "" {
			helpers.Failure(c, http.StatusBadRequest,
				"An "+IdempotencyKeyHeader+" header is required for this request",
				gin.H{"code": messages.CodeIdempotencyRequired})
			c.Abort()
			return
		}
		if len(key) > 255 {
			helpers.Failure(c, http.StatusBadRequest,
				"The "+IdempotencyKeyHeader+" header is too long",
				gin.H{"code": messages.CodeIdempotencyInvalid})
			c.Abort()
			return
		}

		c.Set(IdempotencyKeyCtx, key)
		c.Next()
	}
}

func IdempotencyKey(c *gin.Context) string {
	return c.GetString(IdempotencyKeyCtx)
}
