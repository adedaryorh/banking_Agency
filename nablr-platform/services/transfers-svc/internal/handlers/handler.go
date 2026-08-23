package handlers

import (
	"errors"
	"nabla/transfers-svc/internal/middleware"
	"nabla/transfers-svc/internal/service"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Handler struct{ s *service.Service }

func New(s *service.Service) *Handler { return &Handler{s: s} }

func user(c *gin.Context) (uuid.UUID, bool) {
	id, ok := middleware.GetUserID(c)
	if !ok {
		c.AbortWithStatusJSON(401, gin.H{"error": "authentication required"})
	}
	return id, ok
}
func fail(c *gin.Context, e error) {
	switch {
	case errors.Is(e, pgx.ErrNoRows):
		c.JSON(404, gin.H{"error": "not found"})
	case errors.Is(e, service.ErrInsufficientFunds):
		c.JSON(409, gin.H{"error": e.Error(), "code": "insufficient_funds"})
	case errors.Is(e, service.ErrLimitExceeded), errors.Is(e, service.ErrLimitsMissing), errors.Is(e, service.ErrBalanceCap):
		c.JSON(409, gin.H{"error": e.Error(), "code": "limit_exceeded"})
	default:
		c.JSON(400, gin.H{"error": e.Error()})
	}
}
func (h *Handler) CreateWallet(c *gin.Context) {
	u, ok := user(c)
	if !ok {
		return
	}
	var in struct {
		Currency string `json:"currency"`
	}
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(400, gin.H{"error": "invalid request"})
		return
	}
	x, e := h.s.CreateWallet(c, u, in.Currency)
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(201, gin.H{"data": x})
}
func (h *Handler) Balance(c *gin.Context) {
	u, ok := user(c)
	if !ok {
		return
	}
	x, e := h.s.Wallet(c, u, c.DefaultQuery("currency", "NGN"))
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(200, gin.H{"data": x})
}
func (h *Handler) AddBeneficiary(c *gin.Context) {
	u, ok := user(c)
	if !ok {
		return
	}
	var in struct {
		Type            string     `json:"type"`
		RecipientUserID *uuid.UUID `json:"recipient_user_id"`
		BankCode        string     `json:"bank_code"`
		AccountNumber   string     `json:"account_number"`
		AccountName     string     `json:"account_name"`
		Currency        string     `json:"currency"`
	}
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(400, gin.H{"error": "invalid request"})
		return
	}
	x, e := h.s.AddBeneficiary(c, u, service.BeneficiaryInput{Type: in.Type, RecipientUserID: in.RecipientUserID, BankCode: in.BankCode, AccountNumber: in.AccountNumber, AccountName: in.AccountName, Currency: in.Currency})
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(201, gin.H{"data": x})
}
func (h *Handler) Beneficiaries(c *gin.Context) {
	u, ok := user(c)
	if !ok {
		return
	}
	x, e := h.s.Beneficiaries(c, u)
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(200, gin.H{"data": x})
}
func (h *Handler) Transfer(c *gin.Context) {
	u, ok := user(c)
	if !ok {
		return
	}
	var in struct {
		QuoteID       uuid.UUID `json:"quote_id"`
		BeneficiaryID uuid.UUID `json:"beneficiary_id"`
		AmountMinor   int64     `json:"amount_minor"`
		Currency      string    `json:"currency"`
	}
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(400, gin.H{"error": "invalid request"})
		return
	}
	key := c.GetHeader("Idempotency-Key")
	x, e := h.s.Transfer(c, u, service.TransferInput{QuoteID: in.QuoteID, BeneficiaryID: in.BeneficiaryID, AmountMinor: in.AmountMinor, Currency: in.Currency, IdempotencyKey: key})
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(201, gin.H{"data": x})
}

func (h *Handler) Quote(c *gin.Context) {
	u, ok := user(c)
	if !ok {
		return
	}
	var in struct {
		BeneficiaryID uuid.UUID `json:"beneficiary_id"`
		AmountMinor   int64     `json:"amount_minor"`
		Currency      string    `json:"currency"`
	}
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(400, gin.H{"error": "invalid request"})
		return
	}
	x, e := h.s.Quote(c, u, service.TransferInput{BeneficiaryID: in.BeneficiaryID, AmountMinor: in.AmountMinor, Currency: in.Currency})
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(201, gin.H{"data": x})
}
func (h *Handler) Transfers(c *gin.Context) {
	u, ok := user(c)
	if !ok {
		return
	}
	n, _ := strconv.Atoi(c.DefaultQuery("limit", "25"))
	x, e := h.s.Transfers(c, u, int32(n))
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(200, gin.H{"data": x})
}
func (h *Handler) GetTransfer(c *gin.Context) {
	u, ok := user(c)
	if !ok {
		return
	}
	id, e := uuid.Parse(c.Param("id"))
	if e != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid transfer id"})
		return
	}
	x, e := h.s.TransferByID(c, u, id)
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(200, gin.H{"data": x})
}
func (h *Handler) CancelTransfer(c *gin.Context) {
	u, ok := user(c)
	if !ok {
		return
	}
	id, e := uuid.Parse(c.Param("id"))
	if e != nil {
		c.JSON(400, gin.H{"error": "invalid transfer id"})
		return
	}
	if e = h.s.Cancel(c, u, id); e != nil {
		fail(c, e)
		return
	}
	c.Status(204)
}

func (h *Handler) CreateSchedule(c *gin.Context) {
	u, ok := user(c)
	if !ok {
		return
	}
	var in struct {
		BeneficiaryID uuid.UUID `json:"beneficiary_id"`
		AmountMinor   int64     `json:"amount_minor"`
		Currency      string    `json:"currency"`
		ScheduleType  string    `json:"schedule_type"`
		Frequency     string    `json:"frequency"`
		Narrative     string    `json:"narrative"`
		FirstRun      time.Time `json:"first_run_at"`
	}
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(400, gin.H{"error": "invalid request"})
		return
	}
	x, e := h.s.CreateSchedule(c, u, service.ScheduleInput{BeneficiaryID: in.BeneficiaryID, AmountMinor: in.AmountMinor, Currency: in.Currency, ScheduleType: in.ScheduleType, Frequency: in.Frequency, Narrative: in.Narrative, FirstRun: in.FirstRun})
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(201, gin.H{"data": x})
}
func (h *Handler) Schedules(c *gin.Context) {
	u, ok := user(c)
	if !ok {
		return
	}
	x, e := h.s.Schedules(c, u)
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(200, gin.H{"data": x})
}
func (h *Handler) CancelSchedule(c *gin.Context) {
	u, ok := user(c)
	if !ok {
		return
	}
	id, e := uuid.Parse(c.Param("id"))
	if e != nil {
		c.JSON(400, gin.H{"error": "invalid schedule id"})
		return
	}
	if e = h.s.CancelSchedule(c, u, id); e != nil {
		fail(c, e)
		return
	}
	c.Status(204)
}
