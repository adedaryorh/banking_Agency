package alerting

import (
	"context"
	"log/slog"
	"strings"
)

type Notifier interface {
	Log(context.Context, slog.Level, string, map[string]string)
	Alert(context.Context, string, string, map[string]string)
	Writer() LogWriter
}

type LogWriter interface {
	Write([]byte) (int, error)
}

type local struct{ service string }
type noopWriter struct{}

func New(service string) Notifier              { return &local{service: service} }
func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }
func (l *local) Writer() LogWriter             { return noopWriter{} }

func (l *local) Log(ctx context.Context, level slog.Level, message string, fields map[string]string) {
	attrs := []any{"service", l.service}
	for key, value := range sanitizeFields(fields) {
		attrs = append(attrs, key, value)
	}
	slog.Log(ctx, level, message, attrs...)
}

func (l *local) Alert(ctx context.Context, title, message string, fields map[string]string) {
	if fields == nil {
		fields = map[string]string{}
	}
	fields["alert_title"] = title
	l.Log(ctx, slog.LevelError, message, fields)
}

func sanitizeFields(fields map[string]string) map[string]string {
	clean := make(map[string]string, len(fields))
	for key, value := range fields {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "password") ||
			strings.Contains(lower, "token") ||
			strings.Contains(lower, "authorization") ||
			strings.Contains(lower, "api_key") ||
			strings.Contains(lower, "apikey") ||
			strings.Contains(lower, "secret") ||
			strings.Contains(lower, "private_key") ||
			strings.Contains(lower, "encrypted_payload") ||
			strings.Contains(lower, "selfie") ||
			strings.Contains(lower, "image") ||
			strings.Contains(lower, "card") {
			clean[key] = "[REDACTED]"
		} else {
			clean[key] = value
		}
	}
	return clean
}
