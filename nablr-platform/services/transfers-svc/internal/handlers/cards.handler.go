package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"nabla/transfers-svc/internal/middleware"
	"nabla/transfers-svc/internal/models"
	"nabla/transfers-svc/internal/service"
)

type CardHandler struct {
	cards    *service.CardsService
	identity service.IdentityClient
}

func NewCardHandler(cards *service.CardsService, identity service.IdentityClient) *CardHandler {
	return &CardHandler{cards: cards, identity: identity}
}

type cardDTO struct {
	ID       string `json:"id"`
	WalletID string `json:"wallet_id"`
	Type     string `json:"type"`
	Brand    string `json:"brand"`
	Currency string `json:"currency"`
	Last4    string `json:"last4"`
	// Expiry is MM/YY, which is what is printed on the card and what a
	// customer checking their own card is looking at.
	Expiry    string `json:"expiry"`
	Label     string `json:"label"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	// Delivery is set for physical cards only: where the plastic is. The card
	// itself works from the moment it is issued, whatever this says.
	Delivery *cardDeliveryDTO `json:"delivery,omitempty"`
}

type cardDeliveryDTO struct {
	Status      string `json:"status"`
	Tracking    string `json:"tracking_ref,omitempty"`
	EstimatedAt string `json:"estimated_at,omitempty"`
}

func toCardDTO(c *service.Card) cardDTO {
	expiry := ""
	if c.ExpiryMonth > 0 {
		expiry = time.Date(int(c.ExpiryYear), time.Month(c.ExpiryMonth), 1, 0, 0, 0, 0, time.UTC).
			Format("01/06")
	}
	dto := cardDTO{
		ID: c.ID.String(), WalletID: c.WalletID.String(), Type: c.Type,
		Brand: c.Brand, Currency: c.Currency, Last4: c.Last4, Expiry: expiry,
		Label: c.Label, Status: c.Status,
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339),
	}
	if c.Type == "physical" && c.DeliveryStatus != "" && c.DeliveryStatus != "not_applicable" {
		d := &cardDeliveryDTO{Status: c.DeliveryStatus, Tracking: c.DeliveryTracking}
		if c.DeliveryEstimated != nil {
			d.EstimatedAt = c.DeliveryEstimated.UTC().Format("2006-01-02")
		}
		dto.Delivery = d
	}
	return dto
}

func handleCardError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrCardNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "card not found"})
	case errors.Is(err, models.ErrWalletNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "wallet not found"})
	case errors.Is(err, service.ErrCardNotActive):
		c.JSON(http.StatusConflict, gin.H{"error": "This card is not in a state that allows that."})
	case errors.Is(err, service.ErrDeliveryRequired):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": "A physical card needs an address to be delivered to.",
			"field": "delivery", "code": "required"})
	case errors.Is(err, service.ErrIdentityRequired):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": "Enter your 11-digit NIN or BVN. The card issuer needs it, and we do not keep it.",
			"field": "identity_number", "code": "required"})
	case errors.Is(err, service.ErrAddressRequired):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": "A card needs a billing address.",
			"field": "billing_address", "code": "required"})
	case errors.Is(err, service.ErrProfileIncomplete):
		c.JSON(http.StatusConflict, gin.H{"error": "Some of your details are missing, so a card " +
			"cannot be issued yet. Finish verifying your account and try again."})
	case errors.Is(err, service.ErrCardLimit):
		c.JSON(http.StatusConflict, gin.H{"error": "You already have a card of that kind. " +
			"Nablr gives you one virtual and one physical card."})
	case errors.Is(err, service.ErrCurrencyUnsupported):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Cards are issued in naira only."})
	case errors.Is(err, service.ErrNotPhysical):
		c.JSON(http.StatusConflict, gin.H{"error": "Only physical cards can be delivered."})
	case errors.Is(err, service.ErrDeliveryOpen):
		c.JSON(http.StatusConflict, gin.H{"error": "A delivery for this card is already on its way."})
	case errors.Is(err, service.ErrIssuerNotConfigured):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Cards are not switched on."})
	case errors.Is(err, models.ErrInsufficientFunds):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "insufficient funds for the card fee"})
	default:
		handleError(c, err)
	}
}

// ListCards — GET /api/v1/cards
func (h *CardHandler) ListCards(c *gin.Context) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	list, err := h.cards.Cards(c.Request.Context(), customerID)
	if err != nil {
		handleCardError(c, err)
		return
	}
	out := make([]cardDTO, 0, len(list))
	for i := range list {
		out = append(out, toCardDTO(&list[i]))
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

type issueCardRequest struct {
	WalletID   string `json:"wallet_id"`
	CardType   string `json:"card_type,omitempty"`
	Label      string `json:"label,omitempty"`
	NameOnCard string `json:"name_on_card,omitempty"`

	IdentityType   string `json:"identity_type,omitempty"`
	IdentityNumber string `json:"identity_number,omitempty"`
	DateOfBirth    string `json:"date_of_birth,omitempty"`
	// Where to courier the plastic. Required for a physical card and ignored
	// for a virtual one.
	Delivery *struct {
		Line1 string `json:"line1"`
		City  string `json:"city"`
		State string `json:"state"`
		Phone string `json:"phone"`
	} `json:"delivery,omitempty"`
	BillingAddress *struct {
		Line1      string `json:"line1"`
		City       string `json:"city"`
		State      string `json:"state"`
		PostalCode string `json:"postal_code,omitempty"`
		Country    string `json:"country,omitempty"`
	} `json:"billing_address,omitempty"`
}

// IssueCard — POST /api/v1/cards
func (h *CardHandler) IssueCard(c *gin.Context) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	var body issueCardRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	walletID, err := uuid.Parse(body.WalletID)
	if err != nil {
		notFoundResponse(c, "wallet not found")
		return
	}

	in := service.IssueInput{
		CustomerID: customerID, WalletID: walletID,
		CardType: body.CardType, Label: body.Label, NameOnCard: body.NameOnCard,
		IdentityType:   strings.TrimSpace(body.IdentityType),
		IdentityNumber: strings.TrimSpace(body.IdentityNumber),
		DateOfBirth:    strings.TrimSpace(body.DateOfBirth),
	}
	if body.Delivery != nil {
		in.Delivery = &service.Delivery{
			Line1: strings.TrimSpace(body.Delivery.Line1),
			City:  strings.TrimSpace(body.Delivery.City),
			State: strings.TrimSpace(body.Delivery.State),
			Phone: strings.TrimSpace(body.Delivery.Phone),
		}
	}
	if body.BillingAddress != nil {
		in.BillingAddress = &service.BillingAddress{
			Line1:      strings.TrimSpace(body.BillingAddress.Line1),
			City:       strings.TrimSpace(body.BillingAddress.City),
			State:      strings.TrimSpace(body.BillingAddress.State),
			PostalCode: strings.TrimSpace(body.BillingAddress.PostalCode),
			Country:    strings.TrimSpace(body.BillingAddress.Country),
		}
	}

	card, err := h.cards.Issue(c.Request.Context(), in)
	if err != nil {
		handleCardError(c, err)
		return
	}
	c.Header("Location", "/api/v1/cards/"+card.ID.String())
	c.JSON(http.StatusCreated, gin.H{"data": toCardDTO(card)})
}

// GetCard — GET /api/v1/cards/:id
func (h *CardHandler) GetCard(c *gin.Context) {
	customerID, cardID, ok := h.customerAndCard(c)
	if !ok {
		return
	}
	card, err := h.cards.CardByID(c.Request.Context(), cardID, customerID)
	if err != nil {
		handleCardError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": toCardDTO(card)})
}

// RevealCard — POST /api/v1/cards/:id/details
//
// The numbers on the card, fetched from the issuer and shown once. The
// transaction PIN is required, the response is marked no-store, and nothing is
// persisted on our side.
func (h *CardHandler) RevealCard(c *gin.Context) {
	customerID, cardID, ok := h.customerAndCard(c)
	if !ok {
		return
	}
	var body struct {
		PIN string `json:"pin"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	if !h.stepUp(c, customerID, body.PIN) {
		return
	}

	secrets, err := h.cards.Reveal(c.Request.Context(), cardID, customerID)
	if err != nil {
		handleCardError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.JSON(http.StatusOK, gin.H{
		"pan":          secrets.PAN,
		"cvv":          secrets.CVV,
		"expiry_month": secrets.ExpiryMonth,
		"expiry_year":  secrets.ExpiryYear,
		"name_on_card": secrets.NameOnCard,
	})
}

// FreezeCard — POST /api/v1/cards/:id/freeze
func (h *CardHandler) FreezeCard(c *gin.Context) { h.setFrozen(c, true) }

// UnfreezeCard — POST /api/v1/cards/:id/unfreeze
func (h *CardHandler) UnfreezeCard(c *gin.Context) { h.setFrozen(c, false) }

func (h *CardHandler) setFrozen(c *gin.Context, freeze bool) {
	customerID, cardID, ok := h.customerAndCard(c)
	if !ok {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		unauthorised(c)
		return
	}
	card, err := h.cards.SetCardFrozen(c.Request.Context(), cardID, customerID, userID, freeze)
	if err != nil {
		handleCardError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": toCardDTO(card)})
}

// CloseCard — DELETE /api/v1/cards/:id
//
// Closing a card is permanent: PIN step-up, then the issuer is told before our
// record changes.
func (h *CardHandler) CloseCard(c *gin.Context) {
	customerID, cardID, ok := h.customerAndCard(c)
	if !ok {
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		unauthorised(c)
		return
	}
	// The PIN may come in a body on a DELETE or in a header; neither is
	// universal, so both are accepted.
	var body struct {
		PIN string `json:"pin"`
	}
	_ = c.ShouldBindJSON(&body)
	pin := body.PIN
	if pin == "" {
		pin = c.GetHeader("X-Transaction-PIN")
	}
	if !h.stepUp(c, customerID, pin) {
		return
	}

	if err := h.cards.TerminateCard(c.Request.Context(), cardID, customerID, userID); err != nil {
		handleCardError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// CardTransactions — GET /api/v1/cards/:id/transactions
func (h *CardHandler) CardTransactions(c *gin.Context) {
	customerID, cardID, ok := h.customerAndCard(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "25"))
	list, err := h.cards.CardTransactions(c.Request.Context(), cardID, customerID, limit)
	if err != nil {
		handleCardError(c, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		out = append(out, map[string]any{
			"id": t.ID.String(), "type": t.Type,
			"amount":        moneyDTO(t.AmountMinor, t.Currency),
			"merchant_name": t.MerchantName, "status": t.Status,
			"created_at": t.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

// RequestCardDelivery — POST /api/v1/cards/:id/delivery
func (h *CardHandler) RequestCardDelivery(c *gin.Context) {
	customerID, cardID, ok := h.customerAndCard(c)
	if !ok {
		return
	}
	var body struct {
		AddressLine1 string `json:"address_line1"`
		City         string `json:"city"`
		State        string `json:"state"`
		Phone        string `json:"phone"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	if body.AddressLine1 == "" || body.City == "" || body.State == "" || body.Phone == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": "We need the full address and a phone number for the courier.",
			"field": "address_line1", "code": "required"})
		return
	}
	status, err := h.cards.RequestCardDelivery(c.Request.Context(), customerID, cardID,
		body.AddressLine1, body.City, body.State, body.Phone)
	if err != nil {
		handleCardError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"status": status})
}

// ControlsHandler is the customer's own ethical spending policy.
type ControlsHandler struct {
	controls *service.ControlsService
}

func NewControlsHandler(controls *service.ControlsService) *ControlsHandler {
	return &ControlsHandler{controls: controls}
}

// ListControls — GET /api/v1/spending-controls
func (h *ControlsHandler) ListControls(c *gin.Context) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	list, err := h.controls.List(c.Request.Context(), customerID)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": list})
}

// EnableControl — POST /api/v1/spending-controls
func (h *ControlsHandler) EnableControl(c *gin.Context) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	userID, _ := middleware.GetUserID(c)
	var body struct {
		Category   string   `json:"category"`
		Action     string   `json:"action"`
		CustomMCCs []string `json:"custom_mccs,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	if body.Action == "" {
		body.Action = "block"
	}
	ctrl, err := h.controls.Enable(c.Request.Context(), customerID, userID,
		body.Category, body.Action, false, body.CustomMCCs)
	if err != nil {
		handleControlError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": ctrl})
}

// DisableControl — DELETE /api/v1/spending-controls/:id
func (h *ControlsHandler) DisableControl(c *gin.Context) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	userID, _ := middleware.GetUserID(c)
	controlID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "control not found")
		return
	}
	if err := h.controls.Disable(c.Request.Context(), controlID, customerID, userID); err != nil {
		handleControlError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// RequestControlOverride — POST /api/v1/spending-controls/:id/override
func (h *ControlsHandler) RequestControlOverride(c *gin.Context) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	userID, _ := middleware.GetUserID(c)
	controlID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "control not found")
		return
	}
	var body struct {
		MerchantName   string `json:"merchant_name,omitempty"`
		Reason         string `json:"reason"`
		WindowsMinutes int    `json:"window_minutes,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	window := time.Duration(body.WindowsMinutes) * time.Minute
	overrideID, err := h.controls.RequestOverride(c.Request.Context(), controlID, customerID,
		userID, body.MerchantName, body.Reason, window)
	if err != nil {
		handleControlError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": gin.H{"override_id": overrideID.String()}})
}

func handleControlError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrControlNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "control not found"})
	case errors.Is(err, service.ErrGuardianControl):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrCategoryInvalid):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error(), "field": "category"})
	case errors.Is(err, service.ErrOverrideNeedsWhy):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error(), "field": "reason"})
	default:
		handleError(c, err)
	}
}

func (h *CardHandler) customerAndCard(c *gin.Context) (uuid.UUID, uuid.UUID, bool) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return uuid.Nil, uuid.Nil, false
	}
	cardID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "card not found")
		return uuid.Nil, uuid.Nil, false
	}
	return customerID, cardID, true
}

func (h *CardHandler) stepUp(c *gin.Context, customerID uuid.UUID, pin string) bool {
	if h.identity == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "we cannot verify your PIN right now, so this is not allowed"})
		return false
	}
	if strings.TrimSpace(pin) == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": "your transaction PIN is required", "field": "pin", "code": "required"})
		return false
	}
	ok, err := h.identity.VerifyPIN(c.Request.Context(), customerID, pin)
	if err != nil {
		// A not-set or locked PIN is identity ANSWERING, not an outage — it must not
		// read as a 503 "we cannot verify right now". Only a genuine transport
		// failure fails closed here.
		switch {
		case errors.Is(err, models.ErrPINNotSet):
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "set a transaction PIN first", "field": "pin", "code": "pin_not_set"})
		case errors.Is(err, models.ErrPINLocked):
			c.JSON(http.StatusLocked, gin.H{
				"error": "your PIN is locked; reset it or try again later", "code": "pin_locked"})
		default:
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "we cannot verify your PIN right now, so this is not allowed"})
		}
		return false
	}
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "that PIN is not correct"})
		return false
	}
	return true
}
