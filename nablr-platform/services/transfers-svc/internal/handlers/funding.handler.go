package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"nabla/transfers-svc/internal/middleware"
	"nabla/transfers-svc/internal/service"
)

type FundingHandler struct {
	funding *service.FundingService
	guard   *WebhookSourceGuard
}

func NewFundingHandler(f *service.FundingService, guards ...*WebhookSourceGuard) *FundingHandler {
	h := &FundingHandler{funding: f}
	if len(guards) > 0 {
		h.guard = guards[0]
	}
	return h
}

// GetFundingAccount — GET /api/v1/funding-account
func (h *FundingHandler) GetFundingAccount(c *gin.Context) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	acct, err := h.funding.EnsureAccount(c.Request.Context(), customerID)
	if err != nil {
		handleFundingError(c, err)
		return
	}

	handle, handleErr := h.funding.HandleOf(c.Request.Context(), customerID)
	resp := gin.H{
		"account_number": acct.AccountNumber,
		"account_name":   acct.AccountName,
		"bank_name":      acct.BankName,
		"bank_code":      acct.BankCode,
		"currency":       acct.Currency,
		"permanent":      acct.Permanent,
		// The app must be able to tell a customer the truth about whether this
		// number takes real money.
		"live": acct.Live,
	}
	if handleErr != nil || handle == nil {
		c.JSON(http.StatusOK, gin.H{"data": resp})
		return
	}
	resp["nablr_account_number"] = handle.NablrAccountNumber
	resp["nablr_username"] = handle.NablrUsername
	c.JSON(http.StatusOK, gin.H{"data": resp})
}

type collectionCallback struct {
	Notify               string `json:"notify"`
	NotifyType           string `json:"notifyType"`
	Reference            string `json:"reference"`
	TransactionReference string `json:"transactionReference"`
	Data                 struct {
		Reference            string `json:"reference"`
		TransactionReference string `json:"transactionReference"`
	} `json:"data"`
}

func (b collectionCallback) reference() string {
	for _, v := range []string{
		b.Reference, b.TransactionReference,
		b.Data.Reference, b.Data.TransactionReference,
	} {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func (b collectionCallback) isCollection() bool {
	notify := strings.ToLower(strings.TrimSpace(b.Notify + " " + b.NotifyType))
	if notify == "" {
		// An event with no discriminator at all is treated as a collection:
		// this endpoint's URL is the collections one, and the service verifies
		// the reference with the provider before crediting anything, so a
		// misrouted payout reference simply fails to match a funding account.
		return true
	}
	if strings.Contains(notify, "payout") {
		return false
	}
	return true
}

// HandleCollectionWebhook — POST /webhooks/collections
func (h *FundingHandler) HandleCollectionWebhook(c *gin.Context) {
	if h.guard != nil && !h.guard.Allowed(c) {
		log.Printf("collection webhook rejected: source IP is not allowlisted")
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		badRequest(c, "the callback body is not valid JSON")
		return
	}
	var body collectionCallback
	if err := json.Unmarshal(raw, &body); err != nil {
		badRequest(c, "the callback body is not valid JSON")
		return
	}

	if !body.isCollection() {
		// Acknowledged, not refused: it is a real event, just not ours.
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
		return
	}

	ref := body.reference()
	if ref == "" {
		log.Print("collection webhook carried no reference")
		// 200 deliberately: retrying will not add the field, and a webhook
		// redelivered for ever hides the ones that matter.
		c.JSON(http.StatusOK, gin.H{"status": "no_reference"})
		return
	}

	err = h.funding.Record(c.Request.Context(), ref)

	h.funding.RecordCollectionCallback(c.Request.Context(), ref, raw, err)

	switch {
	case err == nil:
		c.JSON(http.StatusOK, gin.H{"status": "recorded"})

	case errors.Is(err, service.ErrAlreadyRecorded):
		// The point of the unique index. Answer 200 so the provider stops.
		c.JSON(http.StatusOK, gin.H{"status": "already_recorded"})

	case errors.Is(err, service.ErrNotSettled):
		// The provider told us about a payment it has not settled. Not an
		// error on either side; there is simply nothing to credit yet.
		c.JSON(http.StatusOK, gin.H{"status": "not_settled"})

	case errors.Is(err, service.ErrUnknownAccount):
		// Money arrived at a number we do not hold. Loud, because it means
		// either a misconfigured issuer or a payment that will need finding by
		// hand — and silent is how that becomes a customer's lost money.
		log.Printf("collection %s credited an account we do not hold", ref)
		c.JSON(http.StatusOK, gin.H{"status": "unknown_account"})

	case errors.Is(err, service.ErrCollectionsNotConfigured):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "collections are not switched on"})

	default:

		log.Printf("collection %s could not be recorded: %v", ref, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not record the collection"})
	}
}

// CollectionCheck is the JSON shape of the customer-driven check.
type CollectionCheck struct {
	Applied int    `json:"applied"`
	Checked int    `json:"checked"`
	Message string `json:"message"`
}

func (h *FundingHandler) CheckFundingAccount(c *gin.Context) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	res, err := h.funding.CheckForPendingCollections(c.Request.Context(), customerID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrCollectionsNotConfigured):
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "Adding money by bank transfer is not switched on yet."})
		default:
			// The vendor's own words reach the log, never the customer.
			log.Printf("funding check: %v", err)
			if gin.Mode() != gin.ReleaseMode {
				c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusBadGateway, gin.H{
				"error": "We could not check for incoming money just now. Please try again."})
		}
		return
	}
	c.JSON(http.StatusOK, CollectionCheck{Applied: res.Applied, Checked: res.Checked, Message: res.Message})
}

func (h *FundingHandler) SimulateDeposit(c *gin.Context) {
	customerID, ok := middleware.GetCustomerID(c)
	if !ok {
		unauthorised(c)
		return
	}
	var req struct {
		Amount string `json:"amount"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Amount) == "" {
		badRequest(c, "amount is required (minor units, e.g. \"500\" for NGN 5.00)")
		return
	}
	amountMinor, err := strconv.ParseInt(strings.TrimSpace(req.Amount), 10, 64)
	if err != nil || amountMinor <= 0 {
		badRequest(c, "amount must be a positive integer in minor units")
		return
	}
	balance, err := h.funding.SimulateDeposit(c.Request.Context(), customerID, amountMinor)
	if err != nil {
		handleFundingError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"status":        "recorded",
		"balance_minor": balance,
		"balance":       fmt.Sprintf("NGN %.2f", float64(balance)/100),
	}})
}

func handleFundingError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNoWallet):
		c.JSON(http.StatusConflict, gin.H{
			"error": "Your wallet is still being set up. Try again in a moment."})
	case errors.Is(err, service.ErrUnknownAccount):
		c.JSON(http.StatusNotFound, gin.H{"error": "no funding account yet"})
	case errors.Is(err, service.ErrCollectionsNotConfigured):
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Adding money by bank transfer is not switched on yet."})
	default:
		// The vendor's own words reach the log, never the customer.
		log.Printf("funding account: %v", err)
		if gin.Mode() != gin.ReleaseMode {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{
			"error": "We could not set up your account number just now. Please try again."})
	}
}
