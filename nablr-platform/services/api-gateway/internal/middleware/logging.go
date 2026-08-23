package middleware

import (
	"context"
	"log/slog"
	"nabla/api-gateway/internal/alerting"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func RequestLogger(logger *zap.Logger, alerts alerting.Notifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		// Process request
		c.Next()

		// Log after request
		latency := time.Since(start)

		fields := []zap.Field{
			zap.Int("status", c.Writer.Status()),
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.String("query", query),
			zap.String("ip", c.ClientIP()),
			zap.String("user-agent", c.Request.UserAgent()),
			zap.Duration("latency", latency),
			zap.String("user_id", c.GetString("user_id")),
		}

		if len(c.Errors) > 0 {
			logger.Error("Request completed with errors",
				append(fields, zap.String("errors", c.Errors.String()))...)
		} else if c.Writer.Status() >= 500 {
			logger.Error("Request failed", fields...)
		} else if c.Writer.Status() >= 400 {
			logger.Warn("Request client error", fields...)
		} else {
			logger.Info("Request completed", fields...)
		}
		if alerts != nil {
			alerts.Log(context.Background(), slogLevel(c.Writer.Status()), "http_request", map[string]string{
				"status":     strconv.Itoa(c.Writer.Status()),
				"method":     c.Request.Method,
				"path":       path,
				"query":      query,
				"client_ip":  c.ClientIP(),
				"user_agent": c.Request.UserAgent(),
				"latency":    latency.String(),
				"user_id":    c.GetString("user_id"),
				"request_id": c.GetString("request_id"),
				"errors":     c.Errors.String(),
			})
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
