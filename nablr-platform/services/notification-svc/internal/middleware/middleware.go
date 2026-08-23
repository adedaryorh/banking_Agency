package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"nabla/notification-svc/internal/alerting"
)

const RequestIDHeader = "X-Request-ID"

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader(RequestIDHeader))
		if requestID == "" {
			requestID = uuid.NewString()
		}
		c.Set("request_id", requestID)
		c.Header(RequestIDHeader, requestID)
		c.Next()
	}
}

func StructuredLogger(alerts alerting.Notifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		if raw := c.Request.URL.RawQuery; raw != "" {
			path = path + "?" + raw
		}

		c.Next()

		status := c.Writer.Status()
		event := log.Info()
		if status >= 500 {
			event = log.Error()
		} else if status >= 400 {
			event = log.Warn()
		}

		requestID, _ := c.Get("request_id")
		event = event.
			Str("request_id", toStr(requestID)).
			Str("method", c.Request.Method).
			Str("path", path).
			Int("status", status).
			Dur("latency", time.Since(start)).
			Str("client_ip", c.ClientIP())
		if last := c.Errors.Last(); last != nil {
			event = event.Err(last.Err)
			if metadata, ok := last.Meta.(gin.H); ok {
				if channel, ok := metadata["channel"].(string); ok {
					event = event.Str("channel", channel)
				}
				if provider, ok := metadata["provider"].(string); ok {
					event = event.Str("provider", provider)
				}
			}
		}
		event.Msg("http_request")
		if alerts != nil {
			fields := map[string]string{
				"request_id": toStr(requestID),
				"method":     c.Request.Method,
				"path":       path,
				"status":     strconv.Itoa(status),
				"latency":    time.Since(start).String(),
				"client_ip":  c.ClientIP(),
			}
			if last := c.Errors.Last(); last != nil {
				fields["error"] = last.Err.Error()
				if metadata, ok := last.Meta.(gin.H); ok {
					if channel, ok := metadata["channel"].(string); ok {
						fields["channel"] = channel
					}
					if provider, ok := metadata["provider"].(string); ok {
						fields["provider"] = provider
					}
				}
			}
			alerts.Log(context.Background(), slogLevel(status), "http_request", fields)
		}
	}
}

func slogLevel(status int) slog.Level {
	if status >= 500 {
		return slog.LevelError
	}
	if status >= 400 {
		return slog.LevelWarn
	}
	return slog.LevelInfo
}

func Recovery(alerts alerting.Notifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				requestID, _ := c.Get("request_id")
				log.Error().
					Str("request_id", toStr(requestID)).
					Interface("panic", r).
					Str("path", c.Request.URL.Path).
					Msg("panic_recovered")
				if alerts != nil {
					go alerts.Alert(context.Background(), "Notification panic recovered", "A panic was recovered in notification service.", map[string]string{
						"path":       c.Request.URL.Path,
						"request_id": toStr(requestID),
						"panic":      toStr(r),
					})
				}
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"success": false,
					"error": gin.H{
						"code":    "internal_error",
						"message": "Something went wrong. Please try again.",
					},
				})
			}
		}()
		c.Next()
	}
}

func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowAll := len(allowedOrigins) == 0
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = struct{}{}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			if allowAll {
				c.Header("Access-Control-Allow-Origin", origin)
			} else if _, ok := allowed[origin]; ok {
				c.Header("Access-Control-Allow-Origin", origin)
			}
			c.Header("Vary", "Origin")
		}
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, "+RequestIDHeader)

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
