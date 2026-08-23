package novac

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"nabla/transfers-svc/internal/providers"
)

type createVARequest struct {
	Reference   string `json:"reference"`
	AccountType string `json:"accountType"`
	AccountName string `json:"accountName"`
	BankCode    string `json:"bankCode,omitempty"`

	ExpiryInMinutes int    `json:"expiryInMinutes"`
	FirstName       string `json:"firstName,omitempty"`
	LastName        string `json:"lastName,omitempty"`
	CustomerEmail   string `json:"customerEmail,omitempty"`
}

type virtualAccountResponse struct {
	Success bool               `json:"success"`
	Status  bool               `json:"status"`
	Message string             `json:"message"`
	Data    virtualAccountData `json:"data"`
}

// The read-back returns `data` as an ARRAY even for a single account number.
type virtualAccountListResponse struct {
	Success bool                 `json:"success"`
	Status  bool                 `json:"status"`
	Message string               `json:"message"`
	Data    []virtualAccountData `json:"data"`
}

type virtualAccountData struct {
	Reference       string `json:"reference"`
	AccountNumber   string `json:"accountNumber"`
	AccountName     string `json:"accountName"`
	BankName        string `json:"bankName"`
	BankCode        string `json:"bankCode"`
	AccountType     string `json:"accountType"`
	AccountStatus   string `json:"accountStatus"`
	ExpiryInMinutes int    `json:"expiryInMinutes"`
}

func (c *Client) CreateVirtualAccount(ctx context.Context, req providers.VirtualAccountRequest) (*providers.VirtualAccount, error) {
	if strings.TrimSpace(req.Reference) == "" {
		return nil, fmt.Errorf("novac: virtual account requires a reference")
	}

	bank := c.vaBankCode
	reference := vaReference(req.Reference, bank)

	body := createVARequest{
		Reference:   reference,
		AccountType: "reserved",
		AccountName: req.AccountName,

		BankCode:        bank,
		ExpiryInMinutes: 0,
		FirstName:       req.FirstName,
		LastName:        req.LastName,
		CustomerEmail:   req.Email,
	}

	var out virtualAccountResponse
	err := c.do(ctx, "POST", "/api/v1/virtual-account", body, &out)
	if err != nil {

		if isDuplicateReference(err) {
			return c.virtualAccountByReference(ctx, reference)
		}
		return nil, err
	}
	if out.Data.AccountNumber == "" {
		if strings.Contains(strings.ToLower(out.Message), "already exists") {
			return c.virtualAccountByReference(ctx, reference)
		}
		return nil, &providers.Error{
			Code:    providers.ErrRejected,
			Message: firstNonEmpty(out.Message, "novac did not return an account"),
		}
	}
	return fromVAData(out.Data), nil
}

func (c *Client) GetVirtualAccount(ctx context.Context, accountNumber string) (*providers.VirtualAccount, error) {
	var out virtualAccountListResponse
	if err := c.do(ctx, "GET", "/api/v1/virtual-accounts/"+accountNumber, nil, &out); err != nil {
		return nil, err
	}
	for _, d := range out.Data {
		if d.AccountNumber == accountNumber || accountNumber == "" {
			return fromVAData(d), nil
		}
	}
	return nil, &providers.Error{
		Code: providers.ErrNotFound, Message: "virtual account not found",
	}
}

func fromVAData(d virtualAccountData) *providers.VirtualAccount {
	return &providers.VirtualAccount{
		ProviderRef:   d.Reference,
		AccountNumber: d.AccountNumber,
		AccountName:   d.AccountName,
		BankName:      d.BankName,
		BankCode:      d.BankCode,
		// An account that expires is not a funding account. Report what the
		// vendor actually said rather than what we hope.
		Permanent: d.ExpiryInMinutes == 0 &&
			!strings.EqualFold(strings.TrimSpace(d.AccountType), "checkout"),
	}
}

type collectionResponse struct {
	Status bool `json:"status"`
	Data   struct {
		TransactionReference string  `json:"transactionReference"`
		Amount               float64 `json:"amount"`
		ChargedAmount        float64 `json:"chargedAmount"`
		TransactionFee       float64 `json:"transactionFee"`
		Currency             string  `json:"currency"`
		Status               string  `json:"status"`
		Channel              string  `json:"channel"`
		TransactionType      string  `json:"transactionType"`
		CreatedAt            string  `json:"createdAt"`

		// Where a bank transfer into a virtual account describes itself.
		TransferDetail struct {
			AccountNumber           string `json:"accountNumber"`
			OriginatorName          string `json:"originatorName"`
			OriginatorAccountNumber string `json:"originatorAccountNumber"`
			BankName                string `json:"bankName"`
			Narration               string `json:"narration"`
			SessionID               string `json:"sessionId"`
			CreditAccountName       string `json:"creditAccountName"`
		} `json:"transferDetail"`
	} `json:"data"`
}

func (c *Client) GetCollection(ctx context.Context, providerRef string) (*providers.Collection, error) {
	var out collectionResponse
	if err := c.do(ctx, "GET", "/api/v1/transaction/"+providerRef+"/verify", nil, &out); err != nil {
		return nil, err
	}
	d := out.Data
	currency := d.Currency
	if currency == "" {
		currency = "NGN"
	}
	return &providers.Collection{
		ProviderRef:   firstNonEmpty(d.TransactionReference, providerRef),
		AccountNumber: strings.TrimSpace(d.TransferDetail.AccountNumber),
		AmountMinor:   nairaToMinor(d.Amount),
		FeeMinor:      nairaToMinor(d.TransactionFee),
		Currency:      strings.ToUpper(currency),
		Status:        collectionStatus(d.Status),
		SenderName:    strings.TrimSpace(d.TransferDetail.OriginatorName),
		Narrative:     strings.TrimSpace(d.TransferDetail.Narration),
		OccurredAt:    parseTime(d.CreatedAt),
	}, nil
}

func collectionStatus(v string) providers.CollectionStatus {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "success", "successful", "completed", "settled", "paid":
		return providers.CollectionSettled
	case "failed", "reversed", "cancelled", "canceled":
		return providers.CollectionFailed
	default:
		return providers.CollectionPending
	}
}

func nairaToMinor(naira float64) int64 {
	if naira < 0 {
		return -int64(-naira*100 + 0.5)
	}
	return int64(naira*100 + 0.5)
}

func parseTime(v string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, strings.TrimSpace(v)); err == nil {
			return t
		}
	}
	return time.Time{}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (c *Client) virtualAccountByReference(ctx context.Context, reference string) (*providers.VirtualAccount, error) {

	var out virtualAccountResponse
	if err := c.do(ctx, "GET", "/api/v1/virtual-account/"+reference, nil, &out); err != nil {
		return nil, err
	}
	if out.Data.AccountNumber == "" {
		return nil, &providers.Error{
			Code: providers.ErrNotFound, Message: "virtual account not found for reference",
		}
	}
	return fromVAData(out.Data), nil
}

func isDuplicateReference(err error) bool {
	pe, ok := providers.AsError(err)
	if !ok {
		return false
	}
	if pe.Code == providers.ErrDuplicateRequest {
		return true
	}
	haystack := strings.ToLower(pe.Message + " " + string(pe.Raw))
	return strings.Contains(haystack, "already exists") ||
		strings.Contains(haystack, "duplicate")
}

func vaReference(base, bankCode string) string {
	if strings.TrimSpace(bankCode) == "" {
		return base
	}
	return base + "-" + bankCode
}

// VirtualAccountBank is one issuer that can host a funding account.
type VirtualAccountBank struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type vaBankListResponse struct {
	Success bool                 `json:"success"`
	Status  bool                 `json:"status"`
	Data    []VirtualAccountBank `json:"data"`
}

func (c *Client) VirtualAccountBanks(ctx context.Context) ([]VirtualAccountBank, error) {
	var out vaBankListResponse
	if err := c.do(ctx, "GET", "/api/v1/virtual-account/banks", nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiSecret)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &providers.Error{Code: providers.ErrTimeout, Message: err.Error(), Retryable: true}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		// The vendor's own wording is kept in Raw: isDuplicateReference reads
		// it to tell "you already made this" apart from a real refusal, and a
		// masked message would make that undecidable.
		err := parseNovacError(resp.StatusCode, raw)
		if pe, ok := providers.AsError(err); ok {
			pe.Raw = raw
		}
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
