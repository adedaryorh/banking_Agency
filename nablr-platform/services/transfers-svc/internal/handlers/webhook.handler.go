package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"nabla/transfers-svc/internal/service"
)

type WebhookHandler struct {
	svc           *service.Service
	webhookSecret string
	guard         *WebhookSourceGuard
}

func NewWebhookHandler(svc *service.Service, webhookSecret string, guards ...*WebhookSourceGuard) *WebhookHandler {
	h := &WebhookHandler{svc: svc, webhookSecret: webhookSecret}
	if len(guards) > 0 {
		h.guard = guards[0]
	}
	return h
}

const (
	webhookMaxAge   = 5 * time.Minute
	webhookMaxSkew  = 1 * time.Minute
	webhookMaxBytes = 1 << 20 // 1 MiB
)

func (h *WebhookHandler) verify(c *gin.Context, body []byte) bool {
	signature := c.GetHeader("X-Webhook-Signature")
	timestamp := c.GetHeader("X-Webhook-Timestamp")
	if signature == "" || timestamp == "" {
		return false
	}

	ts, err := parseWebhookTimestamp(timestamp)
	if err != nil {
		return false
	}
	if time.Since(ts) > webhookMaxAge || time.Until(ts) > webhookMaxSkew {
		return false
	}

	mac := hmac.New(sha256.New, []byte(h.webhookSecret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	// Constant-time: a byte-by-byte comparison leaks how much of a forged
	// signature was correct, which is enough to construct one.
	return hmac.Equal([]byte(signature), []byte(expected))
}

func parseWebhookTimestamp(v string) (time.Time, error) {
	if ts, err := time.Parse(time.RFC3339, v); err == nil {
		return ts, nil
	}
	secs, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(secs, 0), nil
}

func (h *WebhookHandler) read(c *gin.Context) (service.PayoutWebhook, []byte, bool) {
	var payload service.PayoutWebhook
	if h.guard != nil && !h.guard.Allowed(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return payload, nil, false
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, webhookMaxBytes))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return payload, nil, false
	}
	// Novac's documented source authentication is its IP allowlist. Retain the
	// HMAC verifier only for local/mock rails where no IP allowlist is enabled.
	if (h.guard == nil || !h.guard.Enabled()) && !h.verify(c, body) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
		return payload, nil, false
	}
	// Unmarshal from the same bytes that were signed. Re-reading the request
	// body here would parse something the HMAC never covered.
	if err := json.Unmarshal(body, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return payload, nil, false
	}
	return payload, body, true
}

func (h *WebhookHandler) accept(c *gin.Context, p service.PayoutWebhook, body []byte) (bool, bool) {
	digest := sha256.Sum256(body)
	hexDigest := hex.EncodeToString(digest[:])

	eventID := p.EventID
	if eventID == "" {
		eventID = "sha256:" + hexDigest
	}
	eventType, status := p.OutcomeFields()
	if eventType == "" {
		eventType = status
	}

	first, err := h.svc.AcceptProviderWebhook(c.Request.Context(), eventID, eventType, hexDigest, body, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "webhook processing failed"})
		return false, false
	}
	return first, true
}

// HandlePayoutWebhook is the provider's payout lifecycle callback.
func (h *WebhookHandler) HandlePayoutWebhook(c *gin.Context) {
	payload, body, ok := h.read(c)
	if !ok {
		return
	}

	// Classify before recording, so an event we do not act on is not consumed as
	// if it had been: a rail that later starts sending us a meaningful event
	// under that id would otherwise be silently deduped away.
	event, status := payload.OutcomeFields()
	outcome, known := service.ClassifyPayout(event, status)
	if !known {
		c.JSON(http.StatusOK, gin.H{"status": "event type not handled", "event": event, "provider_status": status})
		return
	}
	first, ok := h.accept(c, payload, body)
	if !ok {
		return
	}
	h.apply(c, outcome, payload, first, "provider payout webhook")
}

// HandleStatusWebhook is the same contract keyed on `status` rather than
// `event`; some rails send only one of the two.
func (h *WebhookHandler) HandleStatusWebhook(c *gin.Context) {
	payload, body, ok := h.read(c)
	if !ok {
		return
	}

	event, status := payload.OutcomeFields()
	outcome, known := service.ClassifyPayout(event, status)
	if !known {
		c.JSON(http.StatusOK, gin.H{"status": "event type not handled", "event": event, "provider_status": status})
		return
	}
	first, ok := h.accept(c, payload, body)
	if !ok {
		return
	}
	h.apply(c, outcome, payload, first, "provider status webhook")
}

func (h *WebhookHandler) apply(c *gin.Context, outcome service.PayoutOutcome, p service.PayoutWebhook, first bool, fallbackReason string) {
	ref := p.Reference()
	if ref == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider_reference is required"})
		return
	}

	verifiedOutcome, terminal, verifiedRef, err := h.svc.VerifyPayoutOutcome(c.Request.Context(), ref)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "webhook verification failed"})
		return
	}
	if !terminal {
		c.JSON(http.StatusOK, gin.H{"status": "verified_pending", "duplicate": !first})
		return
	}
	if verifiedRef != "" {
		ref = verifiedRef
	}
	outcome = verifiedOutcome
	switch outcome {
	case service.PayoutSettled:
		err = h.svc.SettlePayoutByReference(c.Request.Context(), ref)
	case service.PayoutFailed:
		err = h.svc.FailPayoutByReference(c.Request.Context(), ref, p.FailureReason(fallbackReason))
	}
	if err != nil {

		c.JSON(http.StatusInternalServerError, gin.H{"error": "webhook processing failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "processed", "duplicate": !first})
}
