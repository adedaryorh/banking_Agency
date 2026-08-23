package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nabla/transfers-svc/internal/service"
)

const authBudget = 2500 * time.Millisecond

const (
	isoApproved                = "00"
	isoInsufficientFunds       = "51"
	isoExceedsLimit            = "61"
	isoRestrictedCard          = "62" // control or country blocked
	isoDoNotHonour             = "05" // the honest default
	isoSuspectedFraud          = "59"
	isoInvalidCard             = "14"
	isoIssuerUnavailable       = "91" // we could not decide in time
	isoTransactionNotPermitted = "57"
)

func declineCode(reason string) string {
	switch reason {
	case "insufficient_funds":
		return isoInsufficientFunds
	case "limit_exceeded":
		return isoExceedsLimit
	case "control_blocked", "country_blocked", "ethical_control_blocked":
		return isoRestrictedCard
	case "card_frozen":
		return isoSuspectedFraud
	case "card_inactive", "card_closed":
		return isoInvalidCard
	case "issuer_declined":
		return isoTransactionNotPermitted

	case "compliance_restriction":
		return isoRestrictedCard
	default:
		return isoDoNotHonour
	}
}

type CardWebhookHandler struct {
	cards    *service.CardsService
	secret   string
	provider string
}

func NewCardWebhookHandler(cards *service.CardsService, secret string) *CardWebhookHandler {
	return &CardWebhookHandler{cards: cards, secret: strings.TrimSpace(secret), provider: "sudo"}
}

type sudoAuthPayload struct {
	Data struct {
		ID     string `json:"_id"`
		Amount int64  `json:"amount"`

		MerchantAmount int64  `json:"merchantAmount"`
		Currency       string `json:"currency"`
		Card           struct {
			ID string `json:"_id"`
		} `json:"card"`
		Merchant struct {
			Name     string `json:"name"`
			Category string `json:"merchantCategoryCode"`
			Country  string `json:"country"`
		} `json:"merchant"`
		PendingRequest struct {
			Amount   int64  `json:"amount"`
			Currency string `json:"currency"`
		} `json:"pendingRequest"`
		TransactionMetadata struct {
			Channel string `json:"channel"`
		} `json:"transactionMetadata"`
	} `json:"data"`
}

func replyAuth(c *gin.Context, approve bool, code string) {
	status := http.StatusOK
	if !approve {
		status = http.StatusBadRequest
	}
	// The transport is fine; the decision is in the body.
	c.JSON(http.StatusOK, gin.H{
		"statusCode":   status,
		"responseCode": code,
		"data":         gin.H{"responseCode": code},
	})
}

// HandleAuthorization — POST /webhooks/cards/authorization/:secret
func (h *CardWebhookHandler) HandleAuthorization(c *gin.Context) {

	if h.secret == "" || !h.authorised(c) {
		log.Print("card authorisation webhook rejected: bad secret")
		replyAuth(c, false, isoDoNotHonour)
		return
	}
	if h.cards == nil {
		replyAuth(c, false, isoIssuerUnavailable)
		return
	}

	var body sudoAuthPayload
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
		log.Print("card authorisation webhook: body could not be read")
		replyAuth(c, false, isoDoNotHonour)
		return
	}

	providerAuthID := strings.TrimSpace(body.Data.ID)
	providerCardID := strings.TrimSpace(body.Data.Card.ID)
	if providerAuthID == "" || providerCardID == "" {
		replyAuth(c, false, isoInvalidCard)
		return
	}

	amountMinor := body.Data.Amount
	if body.Data.MerchantAmount > amountMinor {
		amountMinor = body.Data.MerchantAmount
	}
	if body.Data.PendingRequest.Amount > amountMinor {
		amountMinor = body.Data.PendingRequest.Amount
	}
	if amountMinor <= 0 {
		replyAuth(c, false, isoDoNotHonour)
		return
	}

	currency := body.Data.Currency
	if currency == "" {
		currency = body.Data.PendingRequest.Currency
	}
	currency = currencyOrNGN(currency)

	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), authBudget)
	defer cancel()

	cardID, err := h.cards.CardIDForProvider(ctx, providerCardID)
	if err != nil {
		log.Printf("card authorisation for an unknown card %s", providerCardID)
		replyAuth(c, false, isoInvalidCard)
		return
	}

	started := time.Now()
	decision, err := h.cards.Authorize(ctx, service.AuthRequest{
		ProviderAuthID:  providerAuthID,
		CardID:          cardID,
		AmountMinor:     amountMinor,
		Currency:        currency,
		MerchantName:    body.Data.Merchant.Name,
		MerchantMCC:     body.Data.Merchant.Category,
		EntryMode:       entryMode(body.Data.TransactionMetadata.Channel),
		IsInternational: isInternational(body.Data.Merchant.Country),
	})
	took := time.Since(started)

	if err != nil {

		log.Printf("card authorisation could not be decided: auth=%s took=%s error=%v",
			providerAuthID, took, err)
		replyAuth(c, false, isoIssuerUnavailable)
		return
	}

	log.Printf("card authorisation decided: auth=%s approved=%t reason=%q took=%s",
		providerAuthID, decision.Approved, decision.DeclineReason, took)

	if decision.Approved {
		replyAuth(c, true, isoApproved)
		return
	}
	replyAuth(c, false, declineCode(decision.DeclineReason))
}

type sudoEvent struct {
	Type        string `json:"type"`
	Environment string `json:"environment"`
	Data        struct {
		Object struct {
			ID             string  `json:"_id"`
			Card           string  `json:"card"`
			Authorization  string  `json:"authorization"`
			Amount         float64 `json:"amount"`
			Fee            float64 `json:"fee"`
			Currency       string  `json:"currency"`
			Type           string  `json:"type"`
			MerchantAmount float64 `json:"merchantAmount"`
		} `json:"object"`
	} `json:"data"`
}

func minorUnits(v float64) (int64, bool) {
	if v < 0 {
		v = -v
	}
	if v != float64(int64(v)) {
		return 0, false
	}
	return int64(v), true
}

// HandleCardEvent — POST /webhooks/cards/events/:secret
func (h *CardWebhookHandler) HandleCardEvent(c *gin.Context) {
	if h.secret == "" || !h.authorised(c) {
		log.Print("card event webhook rejected: bad secret")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorised"})
		return
	}
	if h.cards == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Cards are not switched on."})
		return
	}

	var evt sudoEvent
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	if err := json.NewDecoder(c.Request.Body).Decode(&evt); err != nil {
		badRequest(c, "the event body is not valid JSON")
		return
	}
	obj := evt.Data.Object
	ctx := c.Request.Context()

	switch evt.Type {
	case "transaction.created":

		if strings.TrimSpace(obj.Authorization) == "" {
			log.Printf("card settlement with no authorisation to match: txn=%s card=%s",
				obj.ID, obj.Card)

			c.JSON(http.StatusOK, gin.H{"status": "unmatched"})
			return
		}
		minor, ok := minorUnits(obj.Amount)
		if !ok {
			log.Printf("card settlement amount is not in minor units: auth=%s amount=%v",
				obj.Authorization, obj.Amount)
			badRequest(c, "the settlement amount could not be read")
			return
		}
		if err := h.cards.SettleCard(ctx, h.provider, obj.Authorization, minor, currencyOrNGN(obj.Currency)); err != nil {
			if errors.Is(err, service.ErrAuthNotFound) {
				log.Printf("settlement for an authorisation we do not hold: auth=%s", obj.Authorization)
				c.JSON(http.StatusOK, gin.H{"status": "unknown_authorisation"})
				return
			}
			// A 5xx asks Sudo to try again, which is what we want for a
			// database that was briefly unavailable.
			log.Printf("card settlement failed: auth=%s error=%v", obj.Authorization, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settlement failed"})
			return
		}

	case "transaction.refund":
		if strings.TrimSpace(obj.Authorization) == "" {
			log.Printf("card refund with no authorisation to match: txn=%s", obj.ID)
			c.JSON(http.StatusOK, gin.H{"status": "unmatched"})
			return
		}
		minor, ok := minorUnits(obj.Amount)
		if !ok {
			badRequest(c, "the refund amount could not be read")
			return
		}
		if err := h.cards.RefundCard(ctx, h.provider, obj.Authorization, minor, currencyOrNGN(obj.Currency)); err != nil {
			if errors.Is(err, service.ErrAuthNotFound) {
				c.JSON(http.StatusOK, gin.H{"status": "unknown_authorisation"})
				return
			}
			log.Printf("card refund failed: auth=%s error=%v", obj.Authorization, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "refund failed"})
			return
		}
		log.Printf("issuer confirmed a decline we made: auth=%s", obj.ID)

	case "card.terminated", "card.cancelled", "card.canceled":
		if err := h.cards.CloseByProviderCard(ctx, obj.Card, "issuer_terminated"); err != nil {
			log.Printf("card termination not recorded: card=%s error=%v", obj.Card, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not record termination"})
			return
		}

	default:

		log.Printf("unhandled card event: %s", evt.Type)
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *CardWebhookHandler) authorised(c *gin.Context) bool {
	pathOK := subtle.ConstantTimeCompare([]byte(c.Param("secret")), []byte(h.secret)) == 1

	given := strings.TrimSpace(c.GetHeader("Authorization"))
	given = strings.TrimPrefix(given, "Bearer ")
	headerOK := subtle.ConstantTimeCompare([]byte(given), []byte(h.secret)) == 1

	return pathOK && headerOK
}

// entryMode translates Sudo's channel onto the values card_authorisations
// records. An unrecognised channel is not guessed at: the controls that turn
// on entry mode should not be satisfied by a word we did not understand.
func entryMode(channel string) string {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "web", "ecommerce", "online":
		return "ecommerce"
	case "pos", "chip", "emv":
		return "chip"
	case "contactless", "nfc":
		return "contactless"
	case "atm":
		return "atm"
	default:
		return "ecommerce"
	}
}

func isInternational(country string) bool {
	c := strings.ToUpper(strings.TrimSpace(country))
	return c != "" && c != "NG" && c != "NGA" && c != "NIGERIA"
}

func currencyOrNGN(c string) string {
	if strings.TrimSpace(c) == "" {
		return "NGN"
	}
	return strings.ToUpper(strings.TrimSpace(c))
}

func AllowListMatches(list string, identifiers ...string) bool {
	for _, entry := range strings.Split(list, ",") {
		e := strings.ToLower(strings.TrimSpace(entry))
		if e == "" {
			continue
		}
		if e == "*" || e == "all" {
			return true
		}
		for _, given := range identifiers {
			if given != "" && e == strings.ToLower(strings.TrimSpace(given)) {
				return true
			}
		}
	}
	return false
}
