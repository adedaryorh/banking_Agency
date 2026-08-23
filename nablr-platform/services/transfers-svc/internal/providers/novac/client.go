package novac

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nabla/transfers-svc/internal/providers"
)

type decimalNumber float64

func (n *decimalNumber) UnmarshalJSON(data []byte) error {
	var f float64
	if err := json.Unmarshal(data, &f); err == nil {
		*n = decimalNumber(f)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return fmt.Errorf("invalid decimal number %q", s)
	}
	*n = decimalNumber(parsed)
	return nil
}

// Client implements the Novac payout provider and, via collections.go, the
// inbound collection provider.
type Client struct {
	baseURL   string
	apiKey    string
	apiSecret string

	vaBankCode string
	sandbox    bool
	httpClient *http.Client
}

// Config holds Novac provider configuration
type Config struct {
	BaseURL   string
	APIKey    string
	APISecret string

	VirtualAccountBankCode string

	Sandbox bool
	Timeout time.Duration
}

// New creates a new Novac provider client
func New(cfg Config) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &Client{
		baseURL:    cfg.BaseURL,
		apiKey:     cfg.APIKey,
		apiSecret:  cfg.APISecret,
		vaBankCode: cfg.VirtualAccountBankCode,
		sandbox:    cfg.Sandbox || strings.Contains(strings.ToLower(cfg.BaseURL), "sandbox"),
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

func (c *Client) Info() providers.ProviderInfo {
	return providers.ProviderInfo{
		Name:    "novac",
		Version: "1.0",
		Sandbox: c.sandbox,
	}
}

func (c *Client) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/v1/balance/NGN", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiSecret)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("novac health check failed: %d", resp.StatusCode)
	}
	return nil
}

// SendPayout initiates a payout via Novac
// Endpoint: POST /api/v1/transfers
func (c *Client) SendPayout(ctx context.Context, req providers.PayoutRequest) (*providers.PayoutResult, error) {
	payload := map[string]interface{}{
		"currency":      req.Currency,
		"amount":        float64(req.AmountMinor) / 100, // Novac expects major units.
		"bankCode":      req.BankCode,
		"accountNumber": req.AccountNumber,
		"narration":     req.Narrative,
		"reference":     req.IdempotencyKey,
		"bankName":      req.BankName,
		"accountName":   req.AccountName,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v1/transfers", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiSecret) // Novac authorises with the secret (sk) key
	httpReq.Header.Set("Idempotency-Key", req.IdempotencyKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, &providers.Error{
			Code:      providers.ErrTimeout,
			Message:   err.Error(),
			Retryable: true,
		}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, parseNovacError(resp.StatusCode, respBody)
	}

	var result struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			ID        string        `json:"id"`
			Reference string        `json:"reference"`
			Currency  string        `json:"currency"`
			Amount    decimalNumber `json:"amount"`
			Fee       decimalNumber `json:"fee"`
			BankCode  string        `json:"bankCode"`
			BankName  string        `json:"bankName"`
			Status    string        `json:"status"`
		} `json:"data"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	if !result.Success {
		return nil, &providers.Error{
			Code:      providers.ErrRejected,
			Message:   result.Message,
			Retryable: false,
		}
	}

	return &providers.PayoutResult{
		ProviderRef: result.Data.Reference,
		Status:      mapNovacStatus(result.Data.Status),
	}, nil
}

// PayoutStatus retrieves the current status of a payout
// Endpoint: GET /api/v1/transfers/{reference}
func (c *Client) PayoutStatus(ctx context.Context, reference string) (*providers.PayoutStatusResult, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/transfers/%s", c.baseURL, reference), nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiSecret)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &providers.Error{
			Code:      providers.ErrTimeout,
			Message:   err.Error(),
			Retryable: true,
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, parseNovacError(resp.StatusCode, body)
	}

	var result struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			ID        string        `json:"id"`
			Reference string        `json:"reference"`
			Status    string        `json:"status"`
			Amount    decimalNumber `json:"amount"`
			Currency  string        `json:"currency"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	return &providers.PayoutStatusResult{
		ProviderRef: result.Data.Reference,
		Status:      mapNovacStatus(result.Data.Status),
		Raw:         json.RawMessage(body),
	}, nil
}

// ValidateAccount performs account name verification
// Endpoint: POST /api/v1/banks/account/verify
func (c *Client) ValidateAccount(ctx context.Context, countryCode, bankCode, accountNumber string) (*providers.AccountValidation, error) {
	payload := map[string]string{
		"bank_code":      bankCode,
		"account_number": accountNumber,
		"currency":       "NGN", // Default to NGN, extend based on country_code if needed
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v1/banks/account/verify", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiSecret)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, parseNovacError(resp.StatusCode, respBody)
	}

	var result struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    string `json:"data"` // Account name
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	if !result.Success {
		return nil, &providers.Error{
			Code:    providers.ErrNotFound,
			Message: result.Message,
		}
	}

	return &providers.AccountValidation{
		AccountNumber: accountNumber,
		BankCode:      bankCode,
		AccountName:   result.Data,
		MatchResult:   "match", // Novac API doesn't return match result, assume match if success
	}, nil
}

// Helper functions

func mapNovacStatus(status string) providers.PayoutStatus {
	switch status {
	case "processing", "pending":
		return providers.PayoutProcessing
	case "completed", "success", "settled":
		return providers.PayoutSettled
	case "failed", "rejected":
		return providers.PayoutFailed
	case "reversed", "returned":
		return providers.PayoutReturned
	default:
		return providers.PayoutPending
	}
}

func parseNovacError(statusCode int, body []byte) error {
	var errResp struct {
		Code    string `json:"code"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}

	_ = json.Unmarshal(body, &errResp)

	var code providers.ErrorCode
	retryable := false

	switch statusCode {
	case http.StatusBadRequest:
		code = providers.ErrInvalidRequest
	case http.StatusNotFound:
		code = providers.ErrNotFound
	case http.StatusConflict:
		code = providers.ErrRejected
	case http.StatusTooManyRequests:
		code = providers.ErrTimeout
		retryable = true
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		code = providers.ErrUnknown
		retryable = true
	default:
		code = providers.ErrUnknown
	}

	message := errResp.Message
	if message == "" {
		message = errResp.Error
	}
	if message == "" {
		message = fmt.Sprintf("novac error: %d: %s", statusCode, string(body))
	}
	haystack := strings.ToLower(message + " " + string(body))
	if strings.Contains(haystack, "already exist") || strings.Contains(haystack, "duplicate") {
		code = providers.ErrDuplicateRequest
		retryable = false
	}

	return &providers.Error{
		HTTPStatus: statusCode,
		Code:       code,
		Message:    message,
		Raw:        append([]byte(nil), body...),
		Retryable:  retryable,
	}
}

func (c *Client) FetchStatement(ctx context.Context, from, to time.Time, cursor string) (*providers.StatementPage, error) {
	return &providers.StatementPage{}, nil
}
