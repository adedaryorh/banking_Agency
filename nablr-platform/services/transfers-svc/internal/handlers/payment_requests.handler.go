package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	db "nabla/transfers-svc/db/sqlc"
	"nabla/transfers-svc/internal/middleware"
	"nabla/transfers-svc/internal/service"
)

type paymentRequestDTO struct {
	ID            string          `json:"id"`
	Direction     string          `json:"direction"` // "incoming" (I pay) | "outgoing" (I asked)
	Status        string          `json:"status"`
	Amount        Money           `json:"amount"`
	Note          string          `json:"note,omitempty"`
	Requester     requestPartyDTO `json:"requester"`
	Payer         requestPartyDTO `json:"payer"`
	TransferID    string          `json:"transfer_id,omitempty"`
	DeclineReason string          `json:"decline_reason,omitempty"`
	ExpiresAt     string          `json:"expires_at,omitempty"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
	// Action flags tell the client which buttons to show without re-deriving the
	// rules: only the payer pays or declines a pending request, only the
	// requester cancels one.
	CanPay     bool `json:"can_pay"`
	CanDecline bool `json:"can_decline"`
	CanCancel  bool `json:"can_cancel"`
}

// requestPartyDTO is one side of a request as captured at creation: display name,
// @handle and initials for a client-drawn avatar. Any field may be empty for a
// legacy row or when identity capture was best-effort and came back blank.
type requestPartyDTO struct {
	Name     string `json:"name,omitempty"`
	Username string `json:"username,omitempty"`
	Initials string `json:"initials,omitempty"`
}

func partyDTO(name, username string) requestPartyDTO {
	p := requestPartyDTO{Name: strings.TrimSpace(name)}
	if p.Name != "" {
		p.Initials = initialsOf(p.Name)
	} else if username != "" {
		p.Initials = initialsOf(username)
	}
	if username != "" {
		p.Username = "@" + username
	}
	return p
}
func effectivePaymentRequestStatus(r db.PaymentRequest, now time.Time) string {
	if r.Status == "pending" && r.ExpiresAt.Valid && r.ExpiresAt.Time.Before(now) {
		return "expired"
	}
	return r.Status
}

func toPaymentRequestDTO(r db.PaymentRequest, viewer uuid.UUID, now time.Time) paymentRequestDTO {
	status := effectivePaymentRequestStatus(r, now)
	incoming := r.PayerUserID == viewer // someone is asking ME to pay
	dto := paymentRequestDTO{
		ID:            r.ID.String(),
		Status:        status,
		Amount:        moneyDTO(r.AmountMinor, r.Currency),
		Note:          r.Note.String,
		Requester:     partyDTO(r.RequesterName.String, r.RequesterUsername.String),
		Payer:         partyDTO(r.PayerName.String, r.PayerUsername.String),
		DeclineReason: r.DeclineReason.String,
		CreatedAt:     r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     r.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if incoming {
		dto.Direction = "incoming"
	} else {
		dto.Direction = "outgoing"
	}
	if r.TransferID.Valid {
		dto.TransferID = uuid.UUID(r.TransferID.Bytes).String()
	}
	if r.ExpiresAt.Valid {
		dto.ExpiresAt = r.ExpiresAt.Time.UTC().Format(time.RFC3339)
	}
	// Actions are offered only on a genuinely pending request (effective status,
	// so an expired one shows no pay button) and only to the entitled party.
	if status == "pending" {
		dto.CanPay = incoming
		dto.CanDecline = incoming
		dto.CanCancel = !incoming
	}
	return dto
}

type createPaymentRequestRequest struct {
	// PayerReference is who to bill: a @handle, a 10-digit Nablr number, or a raw
	// user id. Resolution (and its fail-closed identity checks) happens in the
	// service.
	PayerReference string `json:"payer_reference"`
	Amount         string `json:"amount"`
	Currency       string `json:"currency,omitempty"`
	Note           string `json:"note,omitempty"`
	// ExpiresInHours overrides the default request lifetime; 0 uses the default.
	ExpiresInHours int `json:"expires_in_hours,omitempty"`
}

func (h *TransferHandler) CreatePaymentRequest(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	var req createPaymentRequestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	if strings.TrimSpace(req.PayerReference) == "" {
		badRequest(c, "payer_reference is required")
		return
	}
	amountMinor, err := parseMinor(req.Amount)
	if err != nil {
		badRequest(c, err.Error())
		return
	}

	in := service.PaymentRequestInput{
		PayerReference: strings.TrimSpace(req.PayerReference),
		AmountMinor:    amountMinor,
		Currency:       defaultCurrency(req.Currency),
		Note:           req.Note,
	}
	// A UUID reference is taken as the payer's user id directly, matching how
	// CreateBeneficiary treats a raw id; anything else is resolved in the service.
	if id, perr := uuid.Parse(in.PayerReference); perr == nil {
		in.PayerUserID = &id
	}
	if req.ExpiresInHours > 0 {
		in.ExpiresIn = time.Duration(req.ExpiresInHours) * time.Hour
	}

	r, err := h.svc.CreatePaymentRequest(c.Request.Context(), userID, in)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toPaymentRequestDTO(r, userID, time.Now()))
}

const (
	paymentRequestsDefaultLimit = 20
	paymentRequestsMaxLimit     = 100
)

// ListPaymentRequests returns the caller's requests. ?direction=incoming shows
// what I have been asked to pay, outgoing shows what I asked for, and the default
// (all) shows both — the send-money screen's "requests" tab.
func (h *TransferHandler) ListPaymentRequests(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	incoming, outgoing := true, true
	switch strings.ToLower(strings.TrimSpace(c.Query("direction"))) {
	case "incoming":
		outgoing = false
	case "outgoing":
		incoming = false
	case "", "all":
		// both
	default:
		badRequest(c, "direction must be incoming, outgoing or all")
		return
	}
	limit := paymentRequestsDefaultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > paymentRequestsMaxLimit {
		limit = paymentRequestsMaxLimit
	}

	rows, err := h.svc.ListPaymentRequests(c.Request.Context(), userID, incoming, outgoing, int32(limit))
	if err != nil {
		handleError(c, err)
		return
	}
	now := time.Now()
	out := make([]paymentRequestDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toPaymentRequestDTO(r, userID, now))
	}
	c.JSON(http.StatusOK, out)
}

func (h *TransferHandler) GetPaymentRequest(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "payment request not found")
		return
	}
	r, err := h.svc.GetPaymentRequest(c.Request.Context(), userID, id)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, toPaymentRequestDTO(r, userID, time.Now()))
}

type payPaymentRequestRequest struct {
	// PIN is the payer's transaction PIN, forwarded to the service for
	// verification and never logged or persisted here.
	PIN string `json:"pin,omitempty"`
}

// PayPaymentRequest settles a request the caller was asked to pay. The transfer's
// idempotency key is derived from the request id inside the service, so this
// endpoint deliberately does NOT read an Idempotency-Key header: paying request
// X is inherently idempotent on X, and honouring a client-supplied key would
// reopen the double-pay door the derived key closes.
func (h *TransferHandler) PayPaymentRequest(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "payment request not found")
		return
	}
	// Tolerate a missing/blank body: an absent PIN becomes ErrPINRequired in the
	// service, which the client should handle as "prompt for PIN", not "bad JSON".
	var req payPaymentRequestRequest
	_ = c.ShouldBindJSON(&req)

	t, r, err := h.svc.PayPaymentRequest(c.Request.Context(), userID, id, req.PIN)
	if err != nil {
		handleError(c, err)
		return
	}
	dto := toTransferDTO(t)
	dto.Recipient = h.attachRecipient(c.Request.Context(), userID, t.BeneficiaryID)
	c.JSON(http.StatusOK, gin.H{
		"payment_request": toPaymentRequestDTO(r, userID, time.Now()),
		"transfer":        dto,
	})
}

type declinePaymentRequestRequest struct {
	Reason string `json:"reason,omitempty"`
}

func (h *TransferHandler) DeclinePaymentRequest(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "payment request not found")
		return
	}
	var req declinePaymentRequestRequest
	_ = c.ShouldBindJSON(&req)

	r, err := h.svc.DeclinePaymentRequest(c.Request.Context(), userID, id, req.Reason)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, toPaymentRequestDTO(r, userID, time.Now()))
}

func (h *TransferHandler) CancelPaymentRequest(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "payment request not found")
		return
	}
	r, err := h.svc.CancelPaymentRequest(c.Request.Context(), userID, id)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, toPaymentRequestDTO(r, userID, time.Now()))
}
