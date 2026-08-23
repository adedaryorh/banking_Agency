// Package sudo issues cards through Sudo Africa.
//
// Ported from usenablr-1.0 internal/providers/sudo, with httpkit replaced by
// the plain net/http style the rest of transfers-svc's providers use.
//
// Sudo is the first real implementation of providers.CardIssuer; the other one
// is the mock. The decision path it plugs into already exists — the cards
// service's Authorize places a ledger hold and declines on insufficient funds —
// which is why the funding model matters more here than the endpoints do.
//
// Funding source: gateway.
//
// Sudo offers three. "default" spends a wallet Sudo holds for the customer and
// "account" spends the business settlement account, and in both of those SUDO
// decides whether a transaction is approved. "gateway" is the one where Sudo
// asks us, in real time, and settles against the settlement account. That is
// the only one compatible with a double-entry ledger being the truth: the
// alternative is a second balance per customer inside a card processor, and
// two balances for one person's money is a reconciliation problem that never
// ends.
//
// The cost is a four-second budget on the authorisation webhook. See
// internal/handlers/card_webhook.handler.go, which is where that clock is
// enforced.
package sudo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"nabla/transfers-svc/internal/providers"
)

const (
	defaultSandboxURL = "https://api.sandbox.sudo.africa"
	defaultLiveURL    = "https://api.sudo.africa"
	defaultTimeout    = 20 * time.Second
)

type Config struct {
	BaseURL string
	// APIKey is the bearer token from the Sudo dashboard.
	APIKey string
	// FundingSourceID is the gateway funding source every card is issued
	// against. Without it Sudo falls back to a funding model that decides
	// authorisations without asking us, so an empty value is refused rather
	// than defaulted — a card that spends money we never authorised is worse
	// than a card that could not be created.
	FundingSourceID string
	// the settlement account Sudo charges.
	DebitAccountID string
	// card scheme to issue on: Verve, AfriGo, MasterCard, Visa.
	Brand   string
	Sandbox bool
	Timeout time.Duration
}

type Client struct {
	settings Config
	http     *http.Client
	mu       sync.RWMutex
}

func New(cfg Config) (*Client, error) {
	cfg = normalise(cfg)
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("sudo: an api key is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Client{
		settings: cfg,
		http:     &http.Client{Timeout: cfg.Timeout},
	}, nil
}

func normalise(s Config) Config {
	if strings.TrimSpace(s.BaseURL) == "" {
		s.BaseURL = defaultLiveURL
		if s.Sandbox {
			s.BaseURL = defaultSandboxURL
		}
	}
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")
	if strings.TrimSpace(s.Brand) == "" {
		// Verve is the domestic scheme and the one that works everywhere in
		// Nigeria, which is where these cards are used.
		s.Brand = "Verve"
	}
	return s
}

func (c *Client) config() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.settings
}

// Reconfigure swaps the credentials in force, so a key rotated in the console
// takes effect on the next call rather than the next deploy.
func (c *Client) Reconfigure(cfg Config) {
	cfg = normalise(cfg)
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings = cfg
	c.http = &http.Client{Timeout: cfg.Timeout}
}

func (c *Client) Info() providers.CardIssuerInfo {
	s := c.config()
	return providers.CardIssuerInfo{
		Name: "sudo", Kind: "card_issuer", Sandbox: s.Sandbox,
		Capabilities: []string{"issue", "freeze", "terminate", providers.CapabilityCardholderKYC},
	}
}

func (c *Client) HealthCheck(ctx context.Context) error {
	var out envelope[[]any]
	// Listing customers is the cheapest authenticated read; it proves the key
	// works without creating anything.
	return c.do(ctx, "GET", "/customers?page=0&limit=1", nil, "", &out)
}

// envelope is Sudo's response wrapper: every body is {statusCode, message, data}.
type envelope[T any] struct {
	StatusCode int    `json:"statusCode"`
	Message    string `json:"message"`
	Data       T      `json:"data"`
}

type sudoCard struct {
	ID          string `json:"_id"`
	Type        string `json:"type"`
	Brand       string `json:"brand"`
	Currency    string `json:"currency"`
	MaskedPan   string `json:"maskedPan"`
	ExpiryMonth string `json:"expiryMonth"`
	ExpiryYear  string `json:"expiryYear"`
	Status      string `json:"status"`
}

// IssueCard creates the cardholder if needed, then the card.
//
// Sudo will not issue against a customer it does not hold, and that customer
// carries the KYC identity — so a card here is two calls, not one. The
// cardholder is created first and its id returned on the issued card, so a
// second card for the same person does not create a second customer.
func (c *Client) IssueCard(ctx context.Context, req providers.IssueCardRequest) (*providers.IssuedCard, error) {
	s := c.config()

	if strings.TrimSpace(s.FundingSourceID) == "" {
		// Refused rather than defaulted: without a gateway funding source Sudo
		// approves spending without asking us, and our ledger would learn
		// about the money afterwards.
		return nil, &providers.Error{Code: providers.ErrInvalidRequest,
			Message: "sudo: no gateway funding source configured"}
	}
	// Required, not optional. The docs list it among the optional fields and
	// the API refuses without it — verified against the sandbox, which
	// answers 400 "debitAccountId must be longer than or equal to 24
	// characters". Caught here so the failure names the missing setting
	// rather than arriving as a validation error about a field nobody set.
	if strings.TrimSpace(s.DebitAccountID) == "" {
		return nil, &providers.Error{Code: providers.ErrInvalidRequest,
			Message: "sudo: no settlement account configured"}
	}
	if req.CardType != "virtual" && req.CardType != "physical" {
		return nil, &providers.Error{Code: providers.ErrInvalidRequest,
			Message: "sudo: card type must be virtual or physical"}
	}

	customerID := strings.TrimSpace(req.Cardholder.ProviderCustomerID)
	if customerID == "" {
		var err error
		customerID, err = c.createCustomer(ctx, req.Cardholder)
		if err != nil {
			return nil, err
		}
	}

	body := map[string]any{
		"customerId":      customerID,
		"type":            req.CardType,
		"currency":        currencyOr(req.Currency),
		"status":          "active",
		"brand":           s.Brand,
		"fundingSourceId": s.FundingSourceID,
		"debitAccountId":  s.DebitAccountID,
	}

	var out envelope[sudoCard]
	if err := c.do(ctx, "POST", "/cards", body, req.IdempotencyKey, &out); err != nil {
		return nil, err
	}
	if out.Data.ID == "" {
		return nil, &providers.Error{Code: providers.ErrUnknown,
			Message: "sudo: card was created without an id"}
	}

	return &providers.IssuedCard{
		ProviderCardID:     out.Data.ID,
		ProviderCustomerID: customerID,
		// Sudo has no separate token: the card id is the handle, and the PAN
		// is only ever reachable through their vault.
		ProviderToken: out.Data.ID,
		Last4:         last4(out.Data.MaskedPan),
		ExpiryMonth:   atoiMonth(out.Data.ExpiryMonth),
		ExpiryYear:    atoiYear(out.Data.ExpiryYear),
		Brand:         strings.ToLower(out.Data.Brand),
	}, nil
}

func (c *Client) createCustomer(ctx context.Context, ch providers.Cardholder) (string, error) {
	if ch.FirstName == "" || ch.LastName == "" || ch.Phone == "" {
		return "", &providers.Error{Code: providers.ErrInvalidRequest,
			Message: "sudo: a cardholder needs a name and a phone number"}
	}
	if ch.IdentityType == "" || ch.IdentityNumber == "" {
		// Sudo's KYC, not ours. A card issued without it is one the scheme can
		// take back later, which is worse than not issuing it now.
		return "", &providers.Error{Code: providers.ErrInvalidRequest,
			Message: "sudo: a cardholder needs a verified identity number"}
	}

	body := map[string]any{
		"type":        "individual",
		"name":        strings.TrimSpace(ch.FirstName + " " + ch.LastName),
		"phoneNumber": ch.Phone,
		"status":      "active",
		"individual": map[string]any{
			"firstName": ch.FirstName,
			"lastName":  ch.LastName,
			"dob":       ch.DateOfBirth,
			"identity": map[string]any{
				"type":   strings.ToUpper(ch.IdentityType),
				"number": ch.IdentityNumber,
			},
		},
		"billingAddress": map[string]any{
			"line1":      ch.Address.Line1,
			"city":       ch.Address.City,
			"state":      ch.Address.State,
			"postalCode": ch.Address.PostalCode,
			"country":    ch.Address.Country,
		},
	}
	if ch.Email != "" {
		body["emailAddress"] = ch.Email
	}

	var out envelope[struct {
		ID string `json:"_id"`
	}]
	if err := c.do(ctx, "POST", "/customers", body, "", &out); err != nil {
		return "", err
	}
	if out.Data.ID == "" {
		return "", &providers.Error{Code: providers.ErrUnknown,
			Message: "sudo: cardholder was created without an id"}
	}
	return out.Data.ID, nil
}

// SetCardState maps our three states onto Sudo's card status.
//
// Sudo has no "terminated" distinct from "cancelled"; a card is disabled and
// stays disabled. Freezing and terminating therefore look the same on their
// side, and the difference is kept on ours, where the customer can unfreeze
// one and not the other.
func (c *Client) SetCardState(ctx context.Context, providerCardID, state string) error {
	var status string
	switch state {
	case "active", "unfrozen":
		status = "active"
	case "frozen", "inactive":
		status = "inactive"
	case "terminated", "closed", "cancelled":
		status = "canceled"
	default:
		return &providers.Error{Code: providers.ErrInvalidRequest,
			Message: "sudo: unknown card state " + state}
	}
	var out envelope[sudoCard]
	return c.do(ctx, "PUT", "/cards/"+providerCardID,
		map[string]any{"status": status}, "", &out)
}

// RevealCard is not implemented, deliberately.
//
// Sudo requires card details to be read through their vault from the customer's
// own device, with a token minted for that one read. Proxying a PAN through
// this process to satisfy the existing interface would put the whole service
// inside PCI DSS scope for the sake of one screen — so this refuses, and the
// app is expected to call the vault directly.
func (c *Client) RevealCard(ctx context.Context, providerCardID string) (*providers.CardSecrets, error) {
	return nil, &providers.Error{
		Code: providers.ErrNotSupported,
		Message: "sudo: card details are read from the customer's device through " +
			"Sudo's vault, never through this server",
	}
}

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

func (c *Client) do(ctx context.Context, method, path string, body any, idempotencyKey string, out any) error {
	c.mu.RLock()
	s, hc := c.settings, c.http
	c.mu.RUnlock()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, s.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "nablr/1.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return &providers.Error{Code: providers.ErrTimeout, Message: err.Error(), Retryable: true}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return parseSudoError(resp.StatusCode, raw)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func parseSudoError(statusCode int, body []byte) error {
	var errResp struct {
		StatusCode int    `json:"statusCode"`
		Message    string `json:"message"`
	}
	_ = json.Unmarshal(body, &errResp)

	var code providers.ErrorCode
	retryable := false
	switch statusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		code = providers.ErrInvalidRequest
	case http.StatusUnauthorized, http.StatusForbidden:
		code = providers.ErrUnauthenticated
	case http.StatusNotFound:
		code = providers.ErrNotFound
	case http.StatusConflict:
		code = providers.ErrDuplicateRequest
	case http.StatusTooManyRequests:
		code = providers.ErrRateLimited
		retryable = true
	case http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		code = providers.ErrUnavailable
		retryable = true
	default:
		code = providers.ErrUnknown
	}

	message := errResp.Message
	if message == "" {
		message = fmt.Sprintf("sudo error: %d", statusCode)
	}
	return &providers.Error{
		Code: code, Message: message, HTTPStatus: statusCode, Retryable: retryable,
	}
}

func currencyOr(c string) string {
	if strings.TrimSpace(c) == "" {
		return "NGN"
	}
	return strings.ToUpper(c)
}

func last4(maskedPan string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, maskedPan)
	if len(digits) < 4 {
		return ""
	}
	return digits[len(digits)-4:]
}

func atoiMonth(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 12 {
		return 0
	}
	return n
}

// atoiYear widens a two-digit year. Kept apart from the month on purpose: one
// shared helper turned "03" into the year 2003 rather than March.
func atoiYear(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0
	}
	if n < 100 {
		n += 2000
	}
	return n
}
