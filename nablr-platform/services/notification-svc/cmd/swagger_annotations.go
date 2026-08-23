package main

import "nabla/notification-svc/internal/providers"

var _ = providers.SMSMessage{}

// @Summary Health check
// @Tags Health
// @Success 200 {object} map[string]interface{}
// @Router /health [get]
func swaggerNotificationHealth() {}

// @Summary Send SMS
// @Tags Internal Notifications
// @Security InternalServiceToken
// @Accept json
// @Produce json
// @Param X-Internal-Service-Token header string true "Internal service token"
// @Param payload body providers.SMSMessage true "SMS payload"
// @Success 202 {object} map[string]interface{}
// @Router /internal/v1/sms [post]
func swaggerNotificationSendSMS() {}

// @Summary Send email
// @Tags Internal Notifications
// @Security InternalServiceToken
// @Accept json
// @Produce json
// @Param X-Internal-Service-Token header string true "Internal service token"
// @Param payload body providers.EmailMessage true "Email payload"
// @Success 202 {object} map[string]interface{}
// @Router /internal/v1/email [post]
func swaggerNotificationSendEmail() {}

// @Summary Send push notification
// @Tags Internal Notifications
// @Security InternalServiceToken
// @Accept json
// @Produce json
// @Param X-Internal-Service-Token header string true "Internal service token"
// @Param payload body providers.PushMessage true "Push payload"
// @Success 202 {object} map[string]interface{}
// @Router /internal/v1/push [post]
func swaggerNotificationSendPush() {}
