package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "nabla/transfers-svc/db/sqlc"
	"nabla/transfers-svc/internal/middleware"
	"nabla/transfers-svc/internal/models"
	"nabla/transfers-svc/internal/service"
)

type TransferHandler struct {
	svc *service.Service
}

func NewTransferHandler(svc *service.Service) *TransferHandler {
	return &TransferHandler{svc: svc}
}

func (h *TransferHandler) ProviderHealth(ctx context.Context) []service.ProviderHealthStatus {
	if h.svc == nil {
		return nil
	}
	return h.svc.ProviderHealth(ctx)
}

type createBeneficiaryRequest struct {
	Type               string `json:"type"`
	Nickname           string `json:"nickname,omitempty"`
	RecipientReference string `json:"recipient_reference,omitempty"`
	CountryCode        string `json:"country_code,omitempty"`
	Currency           string `json:"currency,omitempty"`
	BankCode           string `json:"bank_code,omitempty"`
	BankName           string `json:"bank_name,omitempty"`
	AccountName        string `json:"account_name,omitempty"`
	AccountNumber      string `json:"account_number,omitempty"`
	Save               *bool  `json:"save,omitempty"`
}

type beneficiaryDTO struct {
	ID                 string `json:"id"`
	Type               string `json:"type"`
	DisplayName        string `json:"display_name"`
	Nickname           string `json:"nickname,omitempty"`
	CountryCode        string `json:"country_code,omitempty"`
	Currency           string `json:"currency,omitempty"`
	BankName           string `json:"bank_name,omitempty"`
	BankCode           string `json:"bank_code,omitempty"`
	AccountNumberLast4 string `json:"account_number_last4,omitempty"`
	AvatarURL          string `json:"avatar_url,omitempty"`
	VerificationStatus string `json:"verification_status"`
	VerifiedName       string `json:"verified_name,omitempty"`
	PayableFrom        string `json:"payable_from,omitempty"`
	InCoolingPeriod    bool   `json:"in_cooling_period"`
	NewPayeeLimit      *Money `json:"new_payee_limit,omitempty"`
	IsFavourite        bool   `json:"is_favourite"`
	IsSaved            bool   `json:"is_saved"`
	CreatedAt          string `json:"created_at"`
}

type Money struct {
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Formatted string `json:"formatted"`
}

type quoteRequest struct {
	SourceWalletID string `json:"source_wallet_id"`
	BeneficiaryID  string `json:"beneficiary_id"`
	Amount         string `json:"amount"`
	Currency       string `json:"currency"`
}

type quoteDTO struct {
	ID            string        `json:"id"`
	Type          string        `json:"type"`
	SendAmount    Money         `json:"send_amount"`
	ReceiveAmount Money         `json:"receive_amount"`
	Fee           Money         `json:"fee"`
	TotalDebit    Money         `json:"total_debit"`
	Recipient     *recipientDTO `json:"recipient,omitempty"`
	ExpiresAt     string        `json:"expires_at"`
	HighValue     bool          `json:"high_value"`
	HighValueNote string        `json:"high_value_note,omitempty"`
}

type recipientDTO struct {
	Type          string `json:"type"`
	Name          string `json:"name"`
	Institution   string `json:"institution,omitempty"`
	AccountNumber string `json:"account_number,omitempty"`
	Username      string `json:"username,omitempty"`
	AvatarURL     string `json:"avatar_url,omitempty"`
}

type createTransferRequest struct {
	QuoteID        string `json:"quote_id,omitempty"`
	SourceWalletID string `json:"source_wallet_id,omitempty"`

	BeneficiaryID string `json:"beneficiary_id,omitempty"`

	CustomerID string `json:"customer_id,omitempty"`

	Amount   string `json:"amount"`
	Currency string `json:"currency,omitempty"`

	RecipientType      string `json:"recipient_type,omitempty"`
	RecipientReference string `json:"recipient_reference,omitempty"`
	BankCode           string `json:"bank_code,omitempty"`
	AccountNumber      string `json:"account_number,omitempty"`
	AccountName        string `json:"account_name,omitempty"`
	Narrative          string `json:"narrative,omitempty"`
	// PINToken is the authorization minted by POST /transfers/authorize-pin. It is
	// the Figma flow's gate: present a valid token and the inline PIN is not needed.
	PINToken string `json:"pin_token,omitempty"`

	PIN string `json:"pin,omitempty"`
}

type transferDTO struct {
	ID        string `json:"id"`
	Reference string `json:"reference"`

	SessionID     string        `json:"session_id,omitempty"`
	Type          string        `json:"type"`
	Status        string        `json:"status"`
	SendAmount    Money         `json:"send_amount"`
	ReceiveAmount Money         `json:"receive_amount"`
	Fee           Money         `json:"fee"`
	TotalDebit    Money         `json:"total_debit"`
	Recipient     *recipientDTO `json:"recipient,omitempty"`
	FailureCode   string        `json:"failure_code,omitempty"`
	FailureReason string        `json:"failure_reason,omitempty"`
	BeneficiaryID string        `json:"beneficiary_id,omitempty"`
	Narrative     string        `json:"narrative,omitempty"`
	CreatedAt     string        `json:"created_at"`
	SubmittedAt   string        `json:"submitted_at,omitempty"`
	CompletedAt   string        `json:"completed_at,omitempty"`
}

func parseMinor(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("amount is required")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")

	whole, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		whole, frac = s[:i], s[i+1:]
	}
	if whole == "" {
		whole = "0"
	}
	// Exactly two decimal places, and never silently truncated: "1.005" is an amount we cannot charge, so it is rejected rather than rounded for the customer.
	switch len(frac) {
	case 0:
		frac = "00"
	case 1:
		frac += "0"
	case 2:
	default:
		return 0, errors.New("amount supports at most 2 decimal places")
	}
	if !allDigits(whole) || !allDigits(frac) {
		return 0, errors.New("amount must be a decimal number")
	}
	major, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, errors.New("amount is out of range")
	}
	minor, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, errors.New("amount is out of range")
	}
	// Overflow-checked: major*100 must not wrap, or a hostile amount could
	// present as a small positive number.
	if major > (1<<62)/100 {
		return 0, errors.New("amount is out of range")
	}
	total := major*100 + minor
	if neg {
		total = -total
	}
	return total, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// moneyDTO renders minor units without floating point, for the same reason parseMinor avoids it: the string a customer reads on a receipt must be the
// integer we stored, not a re-derived approximation of it.
func moneyDTO(minor int64, currency string) Money {
	sign := ""
	v := minor
	if v < 0 {
		sign, v = "-", -v
	}
	formatted := sign + strconv.FormatInt(v/100, 10) + "." + pad2(v%100)
	if currency != "" {
		formatted = currency + " " + formatted
	}
	return Money{Amount: minor, Currency: currency, Formatted: formatted}
}

func pad2(v int64) string {
	if v < 10 {
		return "0" + strconv.FormatInt(v, 10)
	}
	return strconv.FormatInt(v, 10)
}

// Row → DTO mapping
func toBeneficiaryDTO(b db.Beneficiary, now time.Time) beneficiaryDTO {
	dto := beneficiaryDTO{
		ID: b.ID.String(), Type: b.Type,
		DisplayName:        beneficiaryDisplayName(b),
		Nickname:           b.Nickname.String,
		Currency:           b.Currency,
		BankName:           b.BankName.String,
		BankCode:           b.BankCode.String,
		AccountNumberLast4: b.AccountNumberLast4.String,
		VerificationStatus: b.VerificationStatus,
		VerifiedName:       b.VerifiedName.String,
		IsFavourite:        b.IsFavourite,
		IsSaved:            b.IsSaved,
		CreatedAt:          b.CreatedAt.UTC().Format(time.RFC3339),
	}
	if b.Type == "internal" {
		dto.AvatarURL = b.RecipientAvatarUrl.String
	}
	// Cooling period and the new-payee allowance are the salvaged new-payee
	// controls: a fresh payee is shown as capped rather than silently blocked.
	if b.CoolingPeriodEndsAt.Valid {
		dto.PayableFrom = b.CoolingPeriodEndsAt.Time.UTC().Format(time.RFC3339)
		dto.InCoolingPeriod = now.Before(b.CoolingPeriodEndsAt.Time)
	}
	if dto.InCoolingPeriod {
		cur := b.Currency
		if cur == "" {
			cur = "NGN"
		}
		limit := moneyDTO(models.NewPayeeAllowance(cur), cur)
		dto.NewPayeeLimit = &limit
	}
	return dto
}

func beneficiaryDisplayName(b db.Beneficiary) string {
	if b.Nickname.Valid && b.Nickname.String != "" {
		return b.Nickname.String
	}
	if b.VerifiedName.Valid && b.VerifiedName.String != "" {
		return b.VerifiedName.String
	}
	return b.AccountName
}

func recipientForBeneficiary(b db.Beneficiary) *recipientDTO {
	r := &recipientDTO{Type: b.Type, Name: beneficiaryDisplayName(b)}
	switch b.Type {
	case "internal":
		r.Institution = "Nablr"
		r.AccountNumber = b.RecipientAccountNumber.String
		r.AvatarURL = b.RecipientAvatarUrl.String
		if b.RecipientUsername.String != "" {
			r.Username = "@" + b.RecipientUsername.String
		}
	case "bank":
		r.Institution = b.BankName.String
		if b.AccountNumberLast4.String != "" {
			r.AccountNumber = maskAccount(b.AccountNumberLast4.String)
		}
	}
	return r
}

func maskAccount(last4 string) string { return "••••" + last4 }

func toTransferDTO(t db.Transfer) transferDTO {
	dto := transferDTO{
		ID: t.ID.String(), Reference: t.Reference, Type: t.Type,
		Status:        t.Status,
		SendAmount:    moneyDTO(t.SendAmountMinor, t.SendCurrency),
		ReceiveAmount: moneyDTO(t.ReceiveAmountMinor, t.ReceiveCurrency),
		TotalDebit:    moneyDTO(t.TotalDebitMinor, t.SendCurrency),
		FailureCode:   t.FailureCode.String,
		FailureReason: t.FailureReason.String,
		Narrative:     t.Narrative.String,
		CreatedAt:     t.CreatedAt.UTC().Format(time.RFC3339),
	}
	if t.BeneficiaryID != uuid.Nil {
		dto.BeneficiaryID = t.BeneficiaryID.String()
	}
	if t.ProviderReference.Valid {
		dto.SessionID = t.ProviderReference.String
	}
	if t.FeeMinor > 0 && t.FeeCurrency.Valid {
		dto.Fee = moneyDTO(t.FeeMinor, t.FeeCurrency.String)
	} else {
		dto.Fee = moneyDTO(0, t.SendCurrency)
	}
	if t.SubmittedAt.Valid {
		dto.SubmittedAt = t.SubmittedAt.Time.UTC().Format(time.RFC3339)
	}
	if t.CompletedAt.Valid {
		dto.CompletedAt = t.CompletedAt.Time.UTC().Format(time.RFC3339)
	}
	return dto
}

func toQuoteDTO(q db.TransferQuote) quoteDTO {
	dto := quoteDTO{
		ID: q.ID.String(), Type: q.Type,
		SendAmount:    moneyDTO(q.SendAmountMinor, q.SendCurrency),
		ReceiveAmount: moneyDTO(q.ReceiveAmountMinor, q.ReceiveCurrency),
		TotalDebit:    moneyDTO(q.TotalDebitMinor, q.SendCurrency),
		ExpiresAt:     q.ExpiresAt.UTC().Format(time.RFC3339),
		HighValue:     q.HighValue,
		HighValueNote: q.HighValueNote.String,
	}
	if q.FeeMinor > 0 && q.FeeCurrency.Valid {
		dto.Fee = moneyDTO(q.FeeMinor, q.FeeCurrency.String)
	} else {
		dto.Fee = moneyDTO(0, q.SendCurrency)
	}
	return dto
}

// ---------------------------------------------------------------------------
// Beneficiaries
// ---------------------------------------------------------------------------

func (h *TransferHandler) CreateBeneficiary(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}

	var req createBeneficiaryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid request body")
		return
	}

	in := service.BeneficiaryInput{
		Type: normaliseBeneficiaryType(req.Type), BankCode: req.BankCode,
		AccountNumber: req.AccountNumber, AccountName: req.AccountName,
		Currency:           defaultCurrency(req.Currency),
		Nickname:           req.Nickname,
		RecipientReference: req.RecipientReference,
		Save:               req.Save,
	}

	if req.RecipientReference != "" {
		if recipient, err := uuid.Parse(req.RecipientReference); err == nil {
			in.RecipientUserID = &recipient
		}
	}

	ben, err := h.svc.AddBeneficiary(c.Request.Context(), userID, in)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toBeneficiaryDTO(ben, time.Now()))
}

// normaliseBeneficiaryType accepts the older internal_user/bank_account spelling
// as well as the schema's internal/bank, so a client written against the
// previous contract is not broken by the consolidation.
func normaliseBeneficiaryType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "internal", "internal_user":
		return "internal"
	case "bank", "bank_account":
		return "bank"
	default:
		return t
	}
}

func defaultCurrency(c string) string {
	if strings.TrimSpace(c) == "" {
		return "NGN"
	}
	return c
}

func (h *TransferHandler) ListBeneficiaries(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}

	list, err := h.svc.Beneficiaries(c.Request.Context(), userID)
	if err != nil {
		handleError(c, err)
		return
	}
	now := time.Now()
	out := make([]beneficiaryDTO, 0, len(list))
	for _, b := range list {
		out = append(out, toBeneficiaryDTO(b, now))
	}
	c.JSON(http.StatusOK, out)
}

func (h *TransferHandler) GetBeneficiary(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	benID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "beneficiary not found")
		return
	}

	ben, err := h.svc.BeneficiaryByID(c.Request.Context(), userID, benID)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, toBeneficiaryDTO(ben, time.Now()))
}

// UpdateBeneficiary changes presentation only. Account number, bank code and
// account name are not editable — see service.Service.UpdateBeneficiary.
func (h *TransferHandler) UpdateBeneficiary(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	benID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "beneficiary not found")
		return
	}

	var req struct {
		Nickname    *string `json:"nickname,omitempty"`
		IsFavourite *bool   `json:"is_favourite,omitempty"`
		Save        *bool   `json:"save,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid request body")
		return
	}

	ben, err := h.svc.UpdateBeneficiary(c.Request.Context(), userID, benID, req.Nickname, req.IsFavourite, req.Save)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, toBeneficiaryDTO(ben, time.Now()))
}

func (h *TransferHandler) DeleteBeneficiary(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	benID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "beneficiary not found")
		return
	}

	if err := h.svc.RemoveBeneficiary(c.Request.Context(), userID, benID); err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ResolveAccount is the name enquiry a client calls before saving a bank payee,
// so the customer sees whose account they are about to pay.
func (h *TransferHandler) ResolveAccount(c *gin.Context) {
	var body struct {
		BankCode      string `json:"bank_code"`
		AccountNumber string `json:"account_number"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		badRequest(c, "invalid request body")
		return
	}

	v, err := h.svc.ResolveAccount(c.Request.Context(), body.BankCode, body.AccountNumber)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"account_name": v.AccountName, "match": v.MatchResult})
}

// attachRecipient fetches the payee a quote or transfer references and projects
// it into the recipient block. It is best-effort: a since-deleted payee or a
// read error simply yields no recipient rather than failing the money record the
// customer asked for. One extra point read per single-item response; the list
// endpoints deliberately do not call it.
func (h *TransferHandler) attachRecipient(ctx context.Context, userID, beneficiaryID uuid.UUID) *recipientDTO {
	if beneficiaryID == uuid.Nil {
		return nil
	}
	b, err := h.svc.BeneficiaryByID(ctx, userID, beneficiaryID)
	if err != nil {
		return nil
	}
	return recipientForBeneficiary(b)
}

func (h *TransferHandler) CreateQuote(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}

	var req quoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	beneficiaryID, err := uuid.Parse(req.BeneficiaryID)
	if err != nil {
		badRequest(c, "beneficiary_id is required")
		return
	}
	amountMinor, err := parseMinor(req.Amount)
	if err != nil {
		badRequest(c, err.Error())
		return
	}

	q, err := h.svc.Quote(c.Request.Context(), userID, service.TransferInput{
		BeneficiaryID: beneficiaryID,
		AmountMinor:   amountMinor,
		Currency:      defaultCurrency(req.Currency),
	})
	if err != nil {
		handleError(c, err)
		return
	}
	dto := toQuoteDTO(q)
	dto.Recipient = h.attachRecipient(c.Request.Context(), userID, q.BeneficiaryID)
	c.JSON(http.StatusOK, dto)
}

func (h *TransferHandler) GetQuote(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	quoteID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "quote not found")
		return
	}

	q, err := h.svc.QuoteByID(c.Request.Context(), userID, quoteID)
	if err != nil {
		handleError(c, err)
		return
	}
	dto := toQuoteDTO(q)
	dto.Recipient = h.attachRecipient(c.Request.Context(), userID, q.BeneficiaryID)
	c.JSON(http.StatusOK, dto)
}

// ListQuotes returns the customer's quotes, newest first, with recipients
// attached so the list stands alone without a second round of lookups.
func (h *TransferHandler) ListQuotes(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}

	limit := int32(25)
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = int32(n)
		}
	}

	quotes, err := h.svc.ListQuotes(c.Request.Context(), userID, limit)
	if err != nil {
		handleError(c, err)
		return
	}

	out := make([]quoteDTO, 0, len(quotes))
	for _, q := range quotes {
		dto := toQuoteDTO(q)
		dto.Recipient = h.attachRecipient(c.Request.Context(), userID, q.BeneficiaryID)
		out = append(out, dto)
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func (h *TransferHandler) CreateTransfer(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}

	var req createTransferRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid request body")
		return
	}

	if req.CustomerID != "" {
		if _, err := uuid.Parse(strings.TrimSpace(req.CustomerID)); err != nil {
			badRequest(c, "invalid customer_id")
			return
		}
	}
	req.RecipientType, req.RecipientReference = applyCustomerID(
		req.CustomerID, req.BeneficiaryID, req.RecipientType, req.RecipientReference)
	if strings.TrimSpace(req.RecipientType) == "" && strings.TrimSpace(req.BankCode) != "" && strings.TrimSpace(req.AccountNumber) != "" {
		req.RecipientType = "bank"
	} else if strings.TrimSpace(req.RecipientType) == "" && strings.TrimSpace(req.RecipientReference) != "" {
		req.RecipientType = "internal"
	}

	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotencyKey == "" {
		badRequest(c, "Idempotency-Key header is required")
		return
	}

	var amountMinor int64
	if strings.TrimSpace(req.Amount) != "" {
		var err error
		amountMinor, err = parseMinor(req.Amount)
		if err != nil {
			badRequest(c, err.Error())
			return
		}
	} else if strings.TrimSpace(req.PINToken) == "" {
		badRequest(c, "amount is required")
		return
	}

	in := service.TransferInput{
		IdempotencyKey:     idempotencyKey,
		AmountMinor:        amountMinor,
		Currency:           defaultCurrency(req.Currency),
		Narrative:          req.Narrative,
		PIN:                req.PIN,
		RecipientType:      normaliseBeneficiaryType(req.RecipientType),
		RecipientReference: req.RecipientReference,
		BankCode:           req.BankCode,
		AccountNumber:      req.AccountNumber,
		AccountName:        req.AccountName,
	}
	if req.BeneficiaryID != "" {
		beneficiaryID, err := uuid.Parse(req.BeneficiaryID)
		if err != nil {
			badRequest(c, "invalid beneficiary_id")
			return
		}
		in.BeneficiaryID = beneficiaryID
	}
	// A send must name someone: a saved beneficiary_id or an inline recipient.
	if in.BeneficiaryID == uuid.Nil &&
		!hasInlineRecipient(req.RecipientReference, req.BankCode, req.AccountNumber) {
		badRequest(c, "a recipient is required: send beneficiary_id, customer_id, or recipient details")
		return
	}
	if req.PINToken != "" {
		token, err := uuid.Parse(req.PINToken)
		if err != nil {
			badRequest(c, "invalid pin_token")
			return
		}
		in.PINAuthorizationID = token
	}
	in.RecipientDescriptor = recipientDescriptor(
		req.BeneficiaryID, req.RecipientType, req.RecipientReference, req.BankCode, req.AccountNumber)

	// QUOTE PATH — PARKED (commented, not deleted). The send used to REQUIRE a
	// quote and take its amount/currency/recipient from it:
	//
	// if req.QuoteID != "" {
	// 	quoteID, err := uuid.Parse(req.QuoteID)
	// 	if err != nil {
	// 		badRequest(c, "invalid quote_id")
	// 		return
	// 	}
	// 	in.QuoteID = quoteID
	// }
	// if in.QuoteID == uuid.Nil {
	// 	badRequest(c, "quote_id is required")
	// 	return
	// }

	t, err := h.svc.Transfer(c.Request.Context(), userID, in)
	if err != nil {
		handleError(c, err)
		return
	}

	// 201 for money that has already arrived (internal, settled in the same
	// transaction); 202 for a bank payout the worker still has to dispatch.
	dto := toTransferDTO(t)
	dto.Recipient = h.attachRecipient(c.Request.Context(), userID, t.BeneficiaryID)
	if t.Status == string(models.StatusCompleted) {
		c.JSON(http.StatusCreated, dto)
		return
	}
	c.JSON(http.StatusAccepted, dto)
}

type authorizePINRequest struct {
	PIN                string `json:"pin"`
	Amount             string `json:"amount"`
	Currency           string `json:"currency,omitempty"`
	BeneficiaryID      string `json:"beneficiary_id,omitempty"`
	CustomerID         string `json:"customer_id,omitempty"`
	RecipientType      string `json:"recipient_type,omitempty"`
	RecipientReference string `json:"recipient_reference,omitempty"`
	BankCode           string `json:"bank_code,omitempty"`
	AccountNumber      string `json:"account_number,omitempty"`
	AccountName        string `json:"account_name,omitempty"`
}

func (h *TransferHandler) AuthorizePIN(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	var req authorizePINRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	// customer_id addresses an internal payee directly; validate and fold it in
	// exactly as POST /transfers does so both bind the token to the same descriptor.
	if req.CustomerID != "" {
		if _, err := uuid.Parse(strings.TrimSpace(req.CustomerID)); err != nil {
			badRequest(c, "invalid customer_id")
			return
		}
	}
	req.RecipientType, req.RecipientReference = applyCustomerID(
		req.CustomerID, req.BeneficiaryID, req.RecipientType, req.RecipientReference)

	amountMinor, err := parseMinor(req.Amount)
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	if req.BeneficiaryID == "" &&
		!hasInlineRecipient(req.RecipientReference, req.BankCode, req.AccountNumber) {
		badRequest(c, "a recipient is required: send beneficiary_id, customer_id, or recipient details")
		return
	}
	if req.BeneficiaryID != "" {
		if _, err := uuid.Parse(req.BeneficiaryID); err != nil {
			badRequest(c, "invalid beneficiary_id")
			return
		}
	}

	auth, err := h.svc.AuthorizePIN(c.Request.Context(), userID, service.AuthorizePINInput{
		PIN:         req.PIN,
		AmountMinor: amountMinor,
		Currency:    defaultCurrency(req.Currency),
		RecipientDescriptor: recipientDescriptor(
			req.BeneficiaryID, req.RecipientType, req.RecipientReference, req.BankCode, req.AccountNumber),
	})
	if err != nil {
		handleError(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"pin_token":  auth.ID.String(),
		"amount":     moneyDTO(auth.AmountMinor, auth.Currency),
		"expires_at": auth.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func hasInlineRecipient(recipientReference, bankCode, accountNumber string) bool {
	if strings.TrimSpace(recipientReference) != "" {
		return true
	}
	return strings.TrimSpace(bankCode) != "" && strings.TrimSpace(accountNumber) != ""
}

func recipientDescriptor(beneficiaryID, recipientType, recipientReference, bankCode, accountNumber string) string {
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	if id := norm(beneficiaryID); id != "" {
		return "beneficiary:" + id
	}
	if normaliseBeneficiaryType(recipientType) == "bank" {
		return "bank:" + norm(bankCode) + ":" + norm(accountNumber)
	}
	return "internal:" + strings.TrimPrefix(norm(recipientReference), "@")
}

func applyCustomerID(customerID, beneficiaryID, recipientType, recipientReference string) (string, string) {
	if strings.TrimSpace(customerID) == "" {
		return recipientType, recipientReference
	}
	if strings.TrimSpace(beneficiaryID) != "" || strings.TrimSpace(recipientReference) != "" {
		return recipientType, recipientReference
	}
	return "internal", strings.TrimSpace(customerID)
}

func (h *TransferHandler) GetTransfer(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	transferID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "transfer not found")
		return
	}

	t, err := h.svc.TransferByID(c.Request.Context(), userID, transferID)
	if err != nil {
		handleError(c, err)
		return
	}
	dto := toTransferDTO(t)
	dto.Recipient = h.attachRecipient(c.Request.Context(), userID, t.BeneficiaryID)
	c.JSON(http.StatusOK, dto)
}

func (h *TransferHandler) GetTransferStatus(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	transferID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "transfer not found")
		return
	}

	t, err := h.svc.TransferByID(c.Request.Context(), userID, transferID)
	if err != nil {
		handleError(c, err)
		return
	}
	out := gin.H{"id": t.ID, "status": t.Status, "reference": t.Reference}
	if t.ProviderReference.Valid {
		out["session_id"] = t.ProviderReference.String
	}
	if t.FailureReason.Valid {
		out["failure_reason"] = t.FailureReason.String
	}
	if t.CompletedAt.Valid {
		out["completed_at"] = t.CompletedAt.Time.UTC().Format(time.RFC3339)
	}
	c.JSON(http.StatusOK, out)
}

type timelineStepDTO struct {
	Status string `json:"status"`
	Label  string `json:"label"`
	Reason string `json:"reason,omitempty"`
	At     string `json:"at"`
}

func statusLabel(status string) string {
	switch status {
	case "created", "pending":
		return "Payment Initiated"
	case "processing":
		return "Processing"
	case "under_review":
		return "Under Review"
	case "completed":
		return "Completed"
	case "failed":
		return "Failed"
	case "cancelled":
		return "Cancelled"
	case "reversed":
		return "Reversed"
	case "refunded":
		return "Refunded"
	default:
		return status
	}
}

func (h *TransferHandler) GetTransferTimeline(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	transferID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "transfer not found")
		return
	}

	events, err := h.svc.TransferEvents(c.Request.Context(), userID, transferID)
	if err != nil {
		handleError(c, err)
		return
	}
	steps := make([]timelineStepDTO, 0, len(events))
	for _, e := range events {
		steps = append(steps, timelineStepDTO{
			Status: e.ToStatus,
			Label:  statusLabel(e.ToStatus),
			Reason: e.Reason.String,
			At:     e.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, gin.H{"transfer_id": transferID.String(), "timeline": steps})
}

type recentRecipientDTO struct {
	BeneficiaryID string `json:"beneficiary_id"`
	Type          string `json:"type"`
	Name          string `json:"name"`
	Initials      string `json:"initials"`
	AvatarURL     string `json:"avatar_url,omitempty"`
	Institution   string `json:"institution,omitempty"`
	AccountNumber string `json:"account_number,omitempty"`
	Username      string `json:"username,omitempty"`
	LastSentAt    string `json:"last_sent_at"`
	TransferCount int64  `json:"transfer_count"`
}

const (
	recentRecipientsDefaultLimit = 8
	recentRecipientsMaxLimit     = 50
)

func (h *TransferHandler) RecentRecipients(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	limit := recentRecipientsDefaultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > recentRecipientsMaxLimit {
		limit = recentRecipientsMaxLimit
	}

	rows, err := h.svc.RecentRecipients(c.Request.Context(), userID, int32(limit))
	if err != nil {
		handleError(c, err)
		return
	}
	out := make([]recentRecipientDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toRecentRecipientDTO(r))
	}
	c.JSON(http.StatusOK, out)
}

// toRecentRecipientDTO mirrors recipientForBeneficiary's name precedence and
// institution/account rendering for a recent-recipient projection row.
func toRecentRecipientDTO(r db.RecentRecipientsRow) recentRecipientDTO {
	name := r.AccountName
	if r.Nickname.Valid && r.Nickname.String != "" {
		name = r.Nickname.String
	} else if r.VerifiedName.Valid && r.VerifiedName.String != "" {
		name = r.VerifiedName.String
	}
	dto := recentRecipientDTO{
		BeneficiaryID: r.ID.String(),
		Type:          r.Type,
		Name:          name,
		Initials:      initialsOf(name),
		LastSentAt:    r.LastSentAt.UTC().Format(time.RFC3339),
		TransferCount: r.TransferCount,
	}
	switch r.Type {
	case "internal":
		dto.Institution = "Nablr"
		dto.AccountNumber = r.RecipientAccountNumber.String
		dto.AvatarURL = r.RecipientAvatarUrl.String
		if r.RecipientUsername.String != "" {
			dto.Username = "@" + r.RecipientUsername.String
		}
	case "bank":
		dto.Institution = r.BankName.String
		if r.AccountNumberLast4.String != "" {
			dto.AccountNumber = maskAccount(r.AccountNumberLast4.String)
		}
	}
	return dto
}

// initialsOf renders up to two initials from a display name for a client-drawn
// avatar, uppercased and letters only.
func initialsOf(name string) string {
	var initials []rune
	for _, field := range strings.Fields(name) {
		if r := firstLetter(field); r != 0 {
			initials = append(initials, unicode.ToUpper(r))
			if len(initials) == 2 {
				break
			}
		}
	}
	return string(initials)
}

func firstLetter(s string) rune {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return r
		}
	}
	return 0
}

func (h *TransferHandler) ListTransfers(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}

	limit := 25
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}

	list, err := h.svc.Transfers(c.Request.Context(), userID, int32(limit))
	if err != nil {
		handleError(c, err)
		return
	}
	out := make([]transferDTO, 0, len(list))
	for _, t := range list {
		out = append(out, toTransferDTO(t))
	}
	c.JSON(http.StatusOK, out)
}

// this only succeeds before dispatch. The engine re-reads and
// re-checks under the cancelling transaction, so a cancel racing a dispatch
// cannot release a hold on money already sent.
func (h *TransferHandler) CancelTransfer(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	transferID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "transfer not found")
		return
	}

	if err := h.svc.Cancel(c.Request.Context(), userID, transferID); err != nil {
		handleError(c, err)
		return
	}
	t, err := h.svc.TransferByID(c.Request.Context(), userID, transferID)
	if err != nil {
		handleError(c, err)
		return
	}
	dto := toTransferDTO(t)
	dto.Recipient = h.attachRecipient(c.Request.Context(), userID, t.BeneficiaryID)
	c.JSON(http.StatusOK, dto)
}

func (h *TransferHandler) GetPayoutStatus(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	transferID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		notFoundResponse(c, "transfer not found")
		return
	}

	status, err := h.svc.PayoutStatusByTransferID(c.Request.Context(), userID, transferID)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"transfer_id": transferID, "provider_status": status})
}

// ResolvePay — GET /api/v1/pay/resolve?q=
//
// "Who are you paying?" — one box that figures out whether the typed value is a
// Nablr user, a bank account, a handle, an email, a name or still a fragment.
func (h *TransferHandler) ResolvePay(c *gin.Context) {
	if h.svc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pay resolve is not available right now"})
		return
	}
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	c.JSON(http.StatusOK, h.svc.ResolvePay(c.Request.Context(), c.Query("q"), customerID.String()))
}

func (h *TransferHandler) GetLimits(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}

	lim, err := h.svc.Limits(c.Request.Context(), userID, defaultCurrency(c.Query("currency")))
	if err != nil {
		handleError(c, err)
		return
	}
	// balance_cap is null, not 0, when the tier imposes no cap (Tier 3): 0 would
	// read as "may hold nothing". A typed *int64 renders as JSON null when absent.
	var balanceCap *int64
	if lim.HasBalanceCap {
		balanceCap = &lim.BalanceCapMinor
	}
	c.JSON(http.StatusOK, gin.H{
		"currency":          lim.Currency,
		"per_transaction":   lim.PerTransactionMinor,
		"daily_outbound":    lim.DailyOutboundMinor,
		"balance_cap":       balanceCap,
		"daily_spent_minor": lim.DailySpentMinor,
		"remaining_minor":   lim.RemainingMinor,
	})
}

func (h *TransferHandler) GetLimitsUsage(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}

	lim, err := h.svc.Limits(c.Request.Context(), userID, defaultCurrency(c.Query("currency")))
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"currency":          lim.Currency,
		"daily_spent_minor": lim.DailySpentMinor,
		"daily_limit_minor": lim.DailyOutboundMinor,
		"remaining_minor":   lim.RemainingMinor,
	})
}

func (h *TransferHandler) SuggestBanks(c *gin.Context) {
	accountNumber := strings.TrimSpace(c.Query("account_number"))
	suggestions, err := h.svc.SuggestBanks(c.Request.Context(), accountNumber)
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, suggestions)
}

func (h *TransferHandler) ListBanks(c *gin.Context) {
	banks, err := h.svc.ListBanks(c.Request.Context())
	if err != nil {
		handleError(c, err)
		return
	}

	out := make([]gin.H, 0, len(banks))
	for _, b := range banks {
		out = append(out, gin.H{
			"code":         b.Code,
			"name":         b.Name,
			"non_interest": b.NonInterest,
			"supported":    b.NovacCode.Valid && b.NovacCode.String != "",
		})
	}
	c.JSON(http.StatusOK, gin.H{"banks": out})
}

func (h *TransferHandler) BankTiming(c *gin.Context) {
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		code = strings.TrimSpace(c.Query("bank_code"))
	}
	if code == "" {
		badRequest(c, "code is required")
		return
	}
	est, err := h.svc.BankTiming(c.Request.Context(), code)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"bank_code":      est.BankCode,
		"bank_name":      est.BankName,
		"samples":        est.Samples,
		"confident":      est.Confident,
		"median_seconds": est.Median,
		"p90_seconds":    est.P90,
		"slow_share":     est.SlowShare,
	})
}

func (h *TransferHandler) Suggestions(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	beneficiaryID, err := uuid.Parse(strings.TrimSpace(c.Query("beneficiary_id")))
	if err != nil {
		badRequest(c, "beneficiary_id is required")
		return
	}
	got, err := h.svc.Suggestions(c.Request.Context(), userID, beneficiaryID)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"times_paid":     got.TimesPaid,
		"total_amount":   moneyDTO(got.TotalAmountMinor, "NGN"),
		"average_amount": moneyDTO(got.AverageAmountMinor, "NGN"),
		"last_amount":    moneyDTO(got.LastAmountMinor, "NGN"),
		"cadence_hours":  got.CadenceHours,
		"first_paid_at":  got.FirstPaidAt,
		"last_paid_at":   got.LastPaidAt,
	})
}

type createScheduledPaymentRequest struct {
	Name               string `json:"name"`
	BeneficiaryID      string `json:"beneficiary_id,omitempty"`
	RecipientType      string `json:"recipient_type,omitempty"`
	RecipientReference string `json:"recipient_reference,omitempty"`
	BankCode           string `json:"bank_code,omitempty"`
	AccountNumber      string `json:"account_number,omitempty"`
	AccountName        string `json:"account_name,omitempty"`
	Amount             string `json:"amount"`
	Currency           string `json:"currency"`
	ScheduleType       string `json:"schedule_type"`
	Frequency          string `json:"frequency,omitempty"`
	Narrative          string `json:"narrative,omitempty"`
	FirstRun           string `json:"first_run"`
	PaymentDate        string `json:"payment_date,omitempty"`
	EndDate            string `json:"end_date,omitempty"`
	MaxOccurrences     *int32 `json:"max_occurrences,omitempty"`
	PINToken           string `json:"pin_token"`
}

func (h *TransferHandler) CreateScheduledPayment(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	var req createScheduledPaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		scheduleBadRequest(c, "invalid_request_body", "body", "request body must be valid JSON")
		return
	}
	var beneficiaryID uuid.UUID
	var err error
	if strings.TrimSpace(req.BeneficiaryID) != "" {
		beneficiaryID, err = uuid.Parse(req.BeneficiaryID)
		if err != nil {
			scheduleBadRequest(c, "invalid_beneficiary_id", "beneficiary_id", "beneficiary_id must be a valid UUID")
			return
		}
	} else if !hasInlineRecipient(req.RecipientReference, req.BankCode, req.AccountNumber) {
		scheduleBadRequest(c, "recipient_required", "recipient", "select a saved beneficiary or provide recipient details")
		return
	}
	amountMinor, err := parseMinor(req.Amount)
	if err != nil {
		scheduleBadRequest(c, "invalid_amount", "amount", err.Error())
		return
	}
	firstRunValue := strings.TrimSpace(req.FirstRun)
	if firstRunValue == "" {
		firstRunValue = strings.TrimSpace(req.PaymentDate)
	}
	firstRun, err := parseScheduleTime(firstRunValue)
	if err != nil {
		scheduleBadRequest(c, "invalid_payment_date", "payment_date", "payment_date is required and must use YYYY-MM-DD or RFC3339 format")
		return
	}
	var endDate *time.Time
	if strings.TrimSpace(req.EndDate) != "" {
		parsed, parseErr := parseScheduleTime(req.EndDate)
		if parseErr != nil {
			scheduleBadRequest(c, "invalid_end_date", "end_date", "end_date must use YYYY-MM-DD or RFC3339 format")
			return
		}
		endDate = &parsed
	}
	pinToken, err := uuid.Parse(strings.TrimSpace(req.PINToken))
	if err != nil {
		scheduleBadRequest(c, "pin_token_required", "pin_token", "authorize your transaction PIN and provide the returned pin_token")
		return
	}
	recipientType := normaliseBeneficiaryType(req.RecipientType)
	if recipientType == "" && strings.TrimSpace(req.BankCode) != "" && strings.TrimSpace(req.AccountNumber) != "" {
		recipientType = "bank"
	} else if recipientType == "" && strings.TrimSpace(req.RecipientReference) != "" {
		recipientType = "internal"
	}
	created, err := h.svc.CreateSchedule(c.Request.Context(), userID, service.ScheduleInput{
		Name: req.Name, BeneficiaryID: beneficiaryID,
		RecipientType: recipientType, RecipientReference: req.RecipientReference,
		BankCode: req.BankCode, AccountNumber: req.AccountNumber, AccountName: req.AccountName,
		AmountMinor: amountMinor, Currency: strings.ToUpper(strings.TrimSpace(req.Currency)),
		ScheduleType: req.ScheduleType, Frequency: req.Frequency, Narrative: req.Narrative,
		FirstRun: firstRun, EndDate: endDate, MaxOccurrences: req.MaxOccurrences,
		PINAuthorizationID:  pinToken,
		RecipientDescriptor: recipientDescriptor(req.BeneficiaryID, recipientType, req.RecipientReference, req.BankCode, req.AccountNumber),
	})
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, h.toScheduledPaymentDTO(c.Request.Context(), userID, created))
}

func (h *TransferHandler) ListScheduledPayments(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	list, err := h.svc.Schedules(c.Request.Context(), userID)
	if err != nil {
		handleError(c, err)
		return
	}
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && status != "active" && status != "paused" && status != "completed" && status != "cancelled" && status != "failed" {
		scheduleBadRequest(c, "invalid_status", "status", "status must be active, paused, completed, cancelled, or failed")
		return
	}
	limit := 25
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 100 {
			scheduleBadRequest(c, "invalid_limit", "limit", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	offset := 0
	if raw := strings.TrimSpace(c.Query("offset")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 0 {
			scheduleBadRequest(c, "invalid_offset", "offset", "offset must be zero or greater")
			return
		}
		offset = parsed
	}
	filtered := make([]db.ScheduledPayment, 0, len(list))
	for _, s := range list {
		if status != "" && s.Status != status {
			continue
		}
		filtered = append(filtered, s)
	}
	start := offset
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	out := make([]gin.H, 0, end-start)
	for _, s := range filtered[start:end] {
		out = append(out, h.toScheduledPaymentDTO(c.Request.Context(), userID, s))
	}
	c.JSON(http.StatusOK, gin.H{"scheduled_payments": out, "pagination": gin.H{"total": len(filtered), "limit": limit, "offset": offset}})
}

func parseScheduleID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		scheduleBadRequest(c, "invalid_scheduled_payment_id", "id", "scheduled payment id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}

func (h *TransferHandler) GetScheduledPayment(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	id, ok := parseScheduleID(c)
	if !ok {
		return
	}
	s, err := h.svc.GetSchedule(c.Request.Context(), userID, id)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.toScheduledPaymentDTO(c.Request.Context(), userID, s))
}

type updateScheduledPaymentRequest struct {
	Name           *string `json:"name"`
	Amount         *string `json:"amount"`
	Narrative      *string `json:"narrative"`
	NextRunAt      *string `json:"next_run_at"`
	Frequency      *string `json:"frequency"`
	EndDate        *string `json:"end_date"`
	MaxOccurrences *int32  `json:"max_occurrences"`
}

func (h *TransferHandler) UpdateScheduledPayment(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	id, ok := parseScheduleID(c)
	if !ok {
		return
	}
	var req updateScheduledPaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		scheduleBadRequest(c, "invalid_request_body", "body", "request body must be valid JSON")
		return
	}
	var amountMinor *int64
	if req.Amount != nil {
		parsed, err := parseMinor(*req.Amount)
		if err != nil {
			scheduleBadRequest(c, "invalid_amount", "amount", err.Error())
			return
		}
		amountMinor = &parsed
	}
	var nextRunAt *time.Time
	if req.NextRunAt != nil {
		if *req.NextRunAt == "" {
			nextRunAt = &time.Time{}
		} else {
			parsed, err := parseScheduleTime(*req.NextRunAt)
			if err != nil {
				scheduleBadRequest(c, "invalid_next_run_at", "next_run_at", "next_run_at must use YYYY-MM-DD or RFC3339 format")
				return
			}
			nextRunAt = &parsed
		}
	}
	var endDate *time.Time
	clearEndDate := false
	if req.EndDate != nil {
		if strings.TrimSpace(*req.EndDate) == "" {
			clearEndDate = true
		} else {
			parsed, parseErr := parseScheduleTime(*req.EndDate)
			if parseErr != nil {
				scheduleBadRequest(c, "invalid_end_date", "end_date", "end_date must use YYYY-MM-DD or RFC3339 format")
				return
			}
			endDate = &parsed
		}
	}
	updated, err := h.svc.UpdateSchedule(c.Request.Context(), userID, id, service.ScheduleUpdate{
		Name: req.Name, AmountMinor: amountMinor, Narrative: req.Narrative,
		NextRunAt: nextRunAt, Frequency: req.Frequency, EndDate: endDate,
		ClearEndDate: clearEndDate, MaxOccurrences: req.MaxOccurrences,
	})
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.toScheduledPaymentDTO(c.Request.Context(), userID, updated))
}

func (h *TransferHandler) CancelScheduledPayment(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	id, ok := parseScheduleID(c)
	if !ok {
		return
	}
	if err := h.svc.CancelSchedule(c.Request.Context(), userID, id); err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "cancelled"})
}

type pauseScheduledPaymentRequest struct {
	Reason string `json:"reason,omitempty"`
}

func (h *TransferHandler) PauseScheduledPayment(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	id, ok := parseScheduleID(c)
	if !ok {
		return
	}
	var req pauseScheduledPaymentRequest
	_ = c.ShouldBindJSON(&req)
	if err := h.svc.PauseSchedule(c.Request.Context(), userID, id, req.Reason); err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "paused"})
}

func (h *TransferHandler) ResumeScheduledPayment(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	id, ok := parseScheduleID(c)
	if !ok {
		return
	}
	if err := h.svc.ResumeSchedule(c.Request.Context(), userID, id); err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "resumed"})
}

func (h *TransferHandler) GetScheduledPaymentRuns(c *gin.Context) {
	userID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	id, ok := parseScheduleID(c)
	if !ok {
		return
	}
	runs, err := h.svc.ScheduleRuns(c.Request.Context(), userID, id, 50)
	if err != nil {
		handleError(c, err)
		return
	}
	out := make([]gin.H, 0, len(runs))
	for _, r := range runs {
		out = append(out, gin.H{
			"id":            r.ID,
			"scheduled_for": r.ScheduledFor,
			"executed_at":   r.ExecutedAt,
			"outcome":       r.Outcome,
			"transfer_id":   nullableUUID(r.TransferID),
			"error_message": scheduleRunMessage(r.Outcome, r.ErrorMessage),
		})
	}
	c.JSON(http.StatusOK, gin.H{"runs": out})
}

func (h *TransferHandler) toScheduledPaymentDTO(ctx context.Context, userID uuid.UUID, s db.ScheduledPayment) gin.H {
	return gin.H{
		"id":               s.ID,
		"name":             s.PaymentName,
		"schedule_type":    s.ScheduleType,
		"frequency":        nullableText(s.Frequency),
		"day_of_month":     nullableInt2(s.DayOfMonth),
		"day_of_week":      nullableInt2(s.DayOfWeek),
		"amount":           moneyDTO(s.AmountMinor, s.Currency),
		"beneficiary_id":   s.BeneficiaryID,
		"recipient":        h.attachRecipient(ctx, userID, s.BeneficiaryID),
		"paid_from":        gin.H{"wallet_id": s.SourceWalletID, "name": "Nablr wallet", "currency": s.Currency},
		"narrative":        nullableText(s.Narrative),
		"status":           s.Status,
		"next_run_at":      s.NextRunAt,
		"next_payment_at":  s.NextRunAt,
		"last_run_at":      nullableTime(s.LastRunAt),
		"end_date":         nullableTime(s.EndDate),
		"max_occurrences":  nullableInt4(s.MaxOccurrences),
		"occurrence_count": s.OccurrenceCount,
		"pause_reason":     schedulePauseReason(s),
		"created_at":       s.CreatedAt,
		"updated_at":       s.UpdatedAt,
	}
}

func scheduleRunMessage(outcome string, raw pgtype.Text) *string {
	if outcome != "failed" || !raw.Valid {
		return nullableText(raw)
	}
	message := "Scheduled payment could not be completed. Please check your balance and try again."
	lower := strings.ToLower(raw.String)
	if strings.Contains(lower, "insufficient funds") {
		message = "Scheduled payment failed because your available balance was insufficient."
	} else if strings.Contains(lower, "limit") {
		message = "Scheduled payment exceeded your current transaction limit."
	} else if strings.Contains(lower, "beneficiary") || strings.Contains(lower, "recipient") {
		message = "Scheduled payment recipient is no longer available."
	}
	return &message
}

func schedulePauseReason(s db.ScheduledPayment) *string {
	if s.ConsecutiveFailures > 0 && s.PauseReason.Valid {
		message := "Paused after repeated payment failures."
		return &message
	}
	return nullableText(s.PauseReason)
}

func parseScheduleTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("date is required")
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	return time.Parse("2006-01-02", value)
}

func nullableInt4(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	value := v.Int32
	return &value
}

func nullableText(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	s := t.String
	return &s
}

func nullableInt2(v pgtype.Int2) *int16 {
	if !v.Valid {
		return nil
	}
	val := v.Int16
	return &val
}

func nullableTime(v pgtype.Timestamptz) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}

func nullableUUID(v pgtype.UUID) *string {
	if !v.Valid {
		return nil
	}
	s := uuid.UUID(v.Bytes).String()
	return &s
}

func unauthorised(c *gin.Context) {
	c.JSON(http.StatusUnauthorized, gin.H{"error": "authenticated user not found in token"})
}

func badRequest(c *gin.Context, msg string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": msg})
}

func scheduleBadRequest(c *gin.Context, code, field, message string) {
	c.JSON(http.StatusBadRequest, gin.H{"code": code, "field": field, "error": message})
}

func notFoundResponse(c *gin.Context, msg string) {
	c.JSON(http.StatusNotFound, gin.H{"error": msg})
}

func handleError(c *gin.Context, err error) {
	var scheduleValidation *models.ScheduleValidationError
	switch {
	// Specific sentinels first: notFound() joins a sentinel with pgx.ErrNoRows,
	// so the generic row-missing case below must not shadow the real message.
	case errors.Is(err, models.ErrTransferNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "transfer not found"})
	case errors.Is(err, models.ErrBeneficiaryNotFound):
		c.JSON(http.StatusNotFound, gin.H{"code": "beneficiary_not_found", "error": "selected beneficiary was not found"})
	case errors.Is(err, models.ErrQuoteNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "quote not found"})
	case errors.Is(err, models.ErrWalletNotFound):
		c.JSON(http.StatusNotFound, gin.H{"code": "wallet_not_found", "error": "you do not have a wallet for the selected currency"})
	case errors.Is(err, models.ErrScheduleNotFound):
		c.JSON(http.StatusNotFound, gin.H{"code": "scheduled_payment_not_found", "error": "scheduled payment not found"})
	case errors.As(err, &scheduleValidation):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"code": scheduleValidation.Code, "field": scheduleValidation.Field, "error": scheduleValidation.Message,
		})

	case errors.Is(err, models.ErrInsufficientFunds):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "insufficient funds"})
	case errors.Is(err, service.ErrBalanceCap):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "recipient balance cap exceeded"})

	case errors.Is(err, models.ErrTierLimitsNotSet):
		// Not the customer's fault and not something they can fix by verifying
		// further: we have no ceiling on record for them at all.
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no verification limits on record"})
	case errors.Is(err, models.ErrLimitExceeded), errors.Is(err, models.ErrTierLimitExceeded):
		c.JSON(http.StatusForbidden, gin.H{"error": "above the verification limit"})
	case errors.Is(err, models.ErrPaymentsFrozen):
		c.JSON(http.StatusForbidden, gin.H{"error": "payments are frozen"})

	// Authorization control plane. These sit in front of the money engine.
	case errors.Is(err, models.ErrPINRequired):
		c.JSON(http.StatusBadRequest, gin.H{"code": "pin_required", "field": "pin", "error": "transaction PIN is required"})
	case errors.Is(err, models.ErrPINInvalid):
		c.JSON(http.StatusForbidden, gin.H{"code": "pin_invalid", "field": "pin", "error": "transaction PIN is incorrect"})
	case errors.Is(err, models.ErrPINNotSet):

		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": "set a transaction PIN first", "field": "pin", "code": "pin_not_set"})
	case errors.Is(err, models.ErrPINLocked):

		c.JSON(http.StatusLocked, gin.H{
			"error": "your PIN is locked; reset it or try again later", "code": "pin_locked"})
	case errors.Is(err, models.ErrPINAuthInvalid):

		c.JSON(http.StatusConflict, gin.H{
			"error": "your PIN authorization has expired or does not match this payment; please re-enter your PIN",
			"code":  "pin_authorization_invalid"})
	case errors.Is(err, models.ErrAccountNotActive):
		c.JSON(http.StatusForbidden, gin.H{"error": "this account is not permitted to transact"})
	case errors.Is(err, models.ErrSanctioned):
		// Deliberately generic: the sanctions control is not disclosed to the payer.
		c.JSON(http.StatusForbidden, gin.H{"error": "this account is restricted"})
	case errors.Is(err, models.ErrAuthorizationUnavailable):
		// Fail closed: we could not confirm the payer is permitted, so we refuse
		// rather than guess. 503 signals the client it may safely retry shortly.
		log.Printf("transfer request failed error=authorization_unavailable detail=%v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "authorization is temporarily unavailable, please try again"})

	case errors.Is(err, models.ErrBeneficiaryDuplicate):
		c.JSON(http.StatusConflict, gin.H{"error": "beneficiary already exists"})
	case errors.Is(err, models.ErrBeneficiaryCooling):
		c.JSON(http.StatusConflict, gin.H{"error": "beneficiary in cooling period"})
	case errors.Is(err, models.ErrBeneficiaryBlocked):
		c.JSON(http.StatusConflict, gin.H{"error": "beneficiary is blocked"})
	case errors.Is(err, models.ErrBankUnsupported):
		c.JSON(http.StatusConflict, gin.H{"error": "bank is not supported by the payout rail"})
	case errors.Is(err, models.ErrNotCancellable):
		c.JSON(http.StatusConflict, gin.H{"error": "transfer cannot be cancelled"})
	case errors.Is(err, models.ErrQuoteExpired):
		c.JSON(http.StatusConflict, gin.H{"error": "quote expired or already used"})
	case errors.Is(err, models.ErrBeneficiaryMismatch):
		c.JSON(http.StatusConflict, gin.H{"error": "beneficiary does not match the quote"})
	case errors.Is(err, models.ErrSameWallet):
		c.JSON(http.StatusConflict, gin.H{"error": "source and destination wallets are the same"})
	case errors.Is(err, models.ErrSelfBeneficiary):
		c.JSON(http.StatusConflict, gin.H{"error": "cannot add yourself as a beneficiary"})
	case errors.Is(err, models.ErrInvalidTransition):
		c.JSON(http.StatusConflict, gin.H{"error": "transfer is not in a state that allows this"})

	case errors.Is(err, models.ErrPaymentRequestNotFound), errors.Is(err, models.ErrNotRequestParty):
		c.JSON(http.StatusNotFound, gin.H{"error": "payment request not found"})
	case errors.Is(err, models.ErrNotRequestPayer):
		c.JSON(http.StatusForbidden, gin.H{"error": "only the payer may pay or decline this request"})
	case errors.Is(err, models.ErrNotRequestRequester):
		c.JSON(http.StatusForbidden, gin.H{"error": "only the requester may cancel this request"})
	case errors.Is(err, models.ErrRequestNotPending):
		c.JSON(http.StatusConflict, gin.H{"error": "this payment request has already been handled"})
	case errors.Is(err, models.ErrRequestExpired):
		c.JSON(http.StatusConflict, gin.H{"error": "this payment request has expired"})
	case errors.Is(err, models.ErrSelfPaymentRequest):
		c.JSON(http.StatusConflict, gin.H{"error": "cannot request money from yourself"})

	case errors.Is(err, models.ErrZeroAmount):
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount must be positive"})
	case errors.Is(err, models.ErrCurrencyMismatch):
		c.JSON(http.StatusBadRequest, gin.H{"error": "wallet currency does not match the quote"})
	case errors.Is(err, models.ErrUnsupportedCorridor):
		c.JSON(http.StatusBadRequest, gin.H{"error": "corridor not supported"})

	// A joined pgx.ErrNoRows only when no specific sentinel matched above.
	case errors.Is(err, pgx.ErrNoRows):
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})

	default:
		if gin.Mode() != gin.ReleaseMode {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}
}
