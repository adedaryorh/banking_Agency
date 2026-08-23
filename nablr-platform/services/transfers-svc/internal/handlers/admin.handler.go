package handlers

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"nabla/transfers-svc/internal/service"
)

// AdminHandler is the operator-facing wallet-funding surface. Everything it
// exposes is gated upstream by middleware.AdminAuth — this handler assumes
// the caller has already been authenticated as an admin and only validates
// the request shape.
type AdminHandler struct {
	admin *service.AdminService
}

func NewAdminHandler(a *service.AdminService) *AdminHandler {
	return &AdminHandler{admin: a}
}

type adminFundRequest struct {
	CustomerID string `json:"customer_id"`
	// AmountMinor is a string, like the rest of this codebase's money
	// fields, so a JSON number's float precision never touches it.
	AmountMinor string `json:"amount_minor"`
	Currency    string `json:"currency"`
	Reference   string `json:"reference"`
	Reason      string `json:"reason"`
}

// FundWallet — POST /admin/wallet/fund
//
// Every call is written to the application log before it is even validated —
// this endpoint moves real money outside any payment rail, and "who asked
// for this credit and from where" must survive even a request that fails.
func (h *AdminHandler) FundWallet(c *gin.Context) {
	actor := strings.TrimSpace(c.GetHeader("X-Admin-Actor"))
	log.Printf("admin fund request actor=%q remote_ip=%s", actor, c.ClientIP())

	var req adminFundRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "request body is not valid JSON")
		return
	}

	customerID, err := uuid.Parse(strings.TrimSpace(req.CustomerID))
	if err != nil {
		badRequest(c, "customer_id must be a valid UUID")
		return
	}
	amountMinor, err := parseAdminAmount(req.AmountMinor)
	if err != nil {
		badRequest(c, "amount_minor must be a positive integer in minor units")
		return
	}
	if strings.TrimSpace(req.Reference) == "" {
		badRequest(c, "reference is required (used for idempotency — retries with the same reference are not double-credited)")
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		badRequest(c, "reason is required")
		return
	}

	res, err := h.admin.FundWallet(c.Request.Context(), service.AdminFundInput{
		CustomerID:  customerID,
		AmountMinor: amountMinor,
		Currency:    req.Currency,
		Reference:   req.Reference,
		Reason:      req.Reason,
		ActorName:   actor,
	})
	if err != nil {
		log.Printf("admin fund failed actor=%q customer_id=%s reference=%q error=%v", actor, customerID, req.Reference, err)
		handleAdminFundError(c, err)
		return
	}

	log.Printf("admin fund ok actor=%q customer_id=%s reference=%q amount_minor=%d transaction_id=%s already_processed=%t",
		actor, customerID, req.Reference, amountMinor, res.TransactionID, res.AlreadyProcessed)

	c.JSON(http.StatusOK, gin.H{
		"transaction_id":    res.TransactionID,
		"wallet_id":         res.WalletID,
		"balance_minor":     res.BalanceMinor,
		"already_processed": res.AlreadyProcessed,
	})
}

func parseAdminAmount(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("amount_minor is required")
	}
	amount, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || amount <= 0 {
		return 0, errors.New("amount_minor must be a positive integer")
	}
	return amount, nil
}

func handleAdminFundError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAdminFundAmountInvalid),
		errors.Is(err, service.ErrAdminFundReferenceRequired),
		errors.Is(err, service.ErrAdminFundReasonRequired),
		errors.Is(err, service.ErrAdminFundCurrencyInvalid):
		badRequest(c, err.Error())
	default:
		log.Printf("admin fund: unexpected error: %v", err)
		if gin.Mode() != gin.ReleaseMode {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not process this credit. Please try again."})
	}
}
