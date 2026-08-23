package events

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"

	notificationdb "nabla/notification-svc/db/sqlc"
)

type Recorder struct {
	queries *notificationdb.Queries
}

func NewRecorder(queries *notificationdb.Queries) *Recorder {
	return &Recorder{queries: queries}
}

func (r *Recorder) Record(ctx context.Context, eventType, channel, provider, reference, requestID string) {
	if r == nil || r.queries == nil {
		return
	}
	if requestID == "" {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			requestID = first(md.Get("x-request-id"))
		}
	}
	payload, err := json.Marshal(map[string]string{
		"channel": channel, "provider": provider, "reference": reference, "request_id": requestID,
	})
	if err != nil {
		return
	}
	if _, err := r.queries.CreateNotificationEvent(ctx, notificationdb.CreateNotificationEventParams{
		ID: uuid.New(), EventType: eventType, Payload: payload,
	}); err != nil {
		log.Printf("record notification event failed event_type=%s channel=%s provider=%s error=%v", eventType, channel, provider, err)
	}
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}
