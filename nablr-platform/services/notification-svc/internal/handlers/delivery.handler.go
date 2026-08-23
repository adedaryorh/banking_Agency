package handlers

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"nabla/notification-svc/internal/providers"
)

const internalTokenHeader = "X-Internal-Service-Token"

type DeliveryHandler struct {
	token  string
	sms    providers.SMSProvider
	email  providers.EmailProvider
	push   providers.PushProvider
	events deliveryEventRecorder
}

type deliveryEventRecorder interface {
	Record(ctx context.Context, eventType, channel, provider, reference, requestID string)
}

func NewDeliveryHandler(token string, sms providers.SMSProvider, email providers.EmailProvider, push providers.PushProvider, recorders ...deliveryEventRecorder) *DeliveryHandler {
	var recorder deliveryEventRecorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &DeliveryHandler{token: token, sms: sms, email: email, push: push, events: recorder}
}

func (h *DeliveryHandler) Register(router *gin.Engine) {
	internal := router.Group("/internal/v1", h.authenticate())
	internal.POST("/sms", h.SendSMS)
	internal.POST("/email", h.SendEmail)
	internal.POST("/push", h.SendPush)
}

func (h *DeliveryHandler) authenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		provided := c.GetHeader(internalTokenHeader)
		if h.token == "" || len(provided) != len(h.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(h.token)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid internal service token"})
			return
		}
		c.Next()
	}
}

func (h *DeliveryHandler) SendSMS(c *gin.Context) {
	var message providers.SMSMessage
	if err := c.ShouldBindJSON(&message); err != nil || strings.TrimSpace(message.To) == "" || strings.TrimSpace(message.Body) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "to and body are required"})
		return
	}
	result, err := h.sms.SendSMS(c.Request.Context(), message)
	h.respondDelivery(c, "sms", h.sms.Name(), result, err)
}

func (h *DeliveryHandler) SendEmail(c *gin.Context) {
	var message providers.EmailMessage
	if err := c.ShouldBindJSON(&message); err != nil || strings.TrimSpace(message.To) == "" || strings.TrimSpace(message.Subject) == "" || (strings.TrimSpace(message.Text) == "" && strings.TrimSpace(message.HTML) == "") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "to, subject, and text or html are required"})
		return
	}
	result, err := h.email.SendEmail(c.Request.Context(), message)
	h.respondDelivery(c, "email", h.email.Name(), result, err)
}

func (h *DeliveryHandler) SendPush(c *gin.Context) {
	var message providers.PushMessage
	if err := c.ShouldBindJSON(&message); err != nil || strings.TrimSpace(message.DeviceToken) == "" || strings.TrimSpace(message.Title) == "" || strings.TrimSpace(message.Body) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "device_token, title, and body are required"})
		return
	}
	result, err := h.push.SendPush(c.Request.Context(), message)
	h.respondDelivery(c, "push", h.push.Name(), result, err)
}

func (h *DeliveryHandler) respondDelivery(c *gin.Context, channel, provider string, result any, err error) {
	requestID, _ := c.Get("request_id")
	eventType := "notification.delivery.accepted"
	if err != nil {
		eventType = "notification.delivery.failed"
	}
	if h.events != nil {
		h.events.Record(c.Request.Context(), eventType, channel, provider, "", fmt.Sprint(requestID))
	}
	if err != nil {
		_ = c.Error(err).SetMeta(gin.H{"channel": channel, "provider": provider})
		c.JSON(http.StatusBadGateway, gin.H{
			"error":      "delivery failed",
			"code":       "DELIVERY_FAILED",
			"request_id": requestID,
		})
		return
	}
	c.JSON(http.StatusAccepted, result)
}
