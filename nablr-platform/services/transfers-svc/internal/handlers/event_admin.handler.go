package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"nabla/transfers-svc/internal/events"
)

type EventAdminHandler struct{ consumer *events.IdentityConsumer }

func NewEventAdminHandler(consumer *events.IdentityConsumer) *EventAdminHandler {
	if consumer == nil {
		return nil
	}
	return &EventAdminHandler{consumer: consumer}
}

func (h *EventAdminHandler) Replay(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id must be a valid UUID"})
		return
	}
	if err = h.consumer.ReplayDeadLetter(c.Request.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "dead-letter event not found or already replayed"})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "event replay is temporarily unavailable"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "event queued for replay", "event_id": id})
}

func (h *EventAdminHandler) List(c *gin.Context) {
	items, err := h.consumer.DeadLetters(c.Request.Context(), 50)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "dead-letter events are temporarily unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}
