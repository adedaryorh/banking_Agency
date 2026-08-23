package service

import (
	"context"
	"errors"
	"sort"
	"time"

	db "nabla/transfers-svc/db/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func checkNuban(bankCode, accountNumber string) bool {
	if (len(bankCode) != 3 && len(bankCode) != 6) || len(accountNumber) != 10 {
		return false
	}
	digits := bankCode + accountNumber[:9]
	weights := []int{3, 7, 3}
	sum := 0
	for i, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
		sum += int(r-'0') * weights[i%3]
	}
	check := (10 - sum%10) % 10
	return int(accountNumber[9]-'0') == check
}

// ListBanks returns the active bank catalogue, non-interest banks first like
// the reference (a customer asking "which bank" is shown the ethical options
// first, not last).
func (s *Service) ListBanks(ctx context.Context) ([]db.Bank, error) {
	return s.q.ListBanks(ctx)
}

// BankSuggestion is one entry in the /banks/suggest response.
type BankSuggestion struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	NUBANMatch bool   `json:"nuban_match"`
	Supported  bool   `json:"supported"`
}

func (s *Service) SuggestBanks(ctx context.Context, accountNumber string) ([]BankSuggestion, error) {
	if len(accountNumber) != 10 {
		return nil, errors.New("account number must be 10 digits")
	}
	for _, r := range accountNumber {
		if r < '0' || r > '9' {
			return nil, errors.New("account number must be 10 digits")
		}
	}
	banks, err := s.q.SuggestBanksCatalog(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(banks, func(i, j int) bool {
		return checkNuban(banks[i].Code, accountNumber) && !checkNuban(banks[j].Code, accountNumber)
	})
	out := make([]BankSuggestion, 0, len(banks))
	for _, b := range banks {
		out = append(out, BankSuggestion{
			Code:       b.Code,
			Name:       b.Name,
			NUBANMatch: checkNuban(b.Code, accountNumber),
			Supported:  b.NovacCode.Valid && b.NovacCode.String != "",
		})
	}
	return out, nil
}

// BankTimingEstimate is the observed settlement time distribution for one bank.
type BankTimingEstimate struct {
	BankCode  string  `json:"bank_code"`
	BankName  string  `json:"bank_name"`
	Samples   int     `json:"samples"`
	Confident bool    `json:"confident"`
	Median    int     `json:"median_seconds"`
	P90       int     `json:"p90_seconds"`
	SlowShare float64 `json:"slow_share"`
}

const (
	slowShareThreshold   = 5 * time.Minute
	minSamplesConfidence = 8
	maxTimingSamples     = 500
)

func (s *Service) BankTiming(ctx context.Context, bankCode string) (*BankTimingEstimate, error) {
	samples, err := s.q.BankTimingSamples(ctx, db.BankTimingSamplesParams{
		BankCode: pgtype.Text{String: bankCode, Valid: bankCode != ""},
		Limit:    int32(maxTimingSamples),
	})
	if err != nil {
		return nil, err
	}
	out := &BankTimingEstimate{BankCode: bankCode}
	if len(samples) == 0 {
		out.BankName = bankCode
		return out, nil
	}
	out.BankName = samples[0].BankName.String
	out.Samples = len(samples)

	secs := make([]float64, 0, len(samples))
	slow := 0
	for _, sm := range samples {
		v := sm.DurationSeconds
		secs = append(secs, v)
		if time.Duration(v*float64(time.Second)) > slowShareThreshold {
			slow++
		}
	}
	sort.Float64s(secs)
	out.Median = int(percentileOf(secs, 0.5))
	out.P90 = int(percentileOf(secs, 0.9))
	if len(secs) > 0 {
		out.SlowShare = float64(slow) / float64(len(secs))
	}
	out.Confident = len(secs) >= minSamplesConfidence
	return out, nil
}

func percentileOf(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	rank := p * float64(n-1)
	lo := int(rank)
	hi := lo + 1
	if hi >= n {
		hi = n - 1
	}
	frac := rank - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// PayeeSuggestions summarises the customer's history with one payee: how much,
// how often, and the cadence between payments. This is the amount-prefill and
// schedule-hint the reference serves from /transfers/suggestions.
type PayeeSuggestions struct {
	TimesPaid          int     `json:"times_paid"`
	TotalAmountMinor   int64   `json:"total_amount_minor"`
	AverageAmountMinor int64   `json:"average_amount_minor"`
	LastAmountMinor    int64   `json:"last_amount_minor"`
	CadenceHours       *int    `json:"cadence_hours,omitempty"`
	FirstPaidAt        *string `json:"first_paid_at,omitempty"`
	LastPaidAt         *string `json:"last_paid_at,omitempty"`
}

// Suggestions derives pay history for one payee over the last six months.
func (s *Service) Suggestions(ctx context.Context, userID, beneficiaryID uuid.UUID) (*PayeeSuggestions, error) {
	rows, err := s.q.BeneficiaryTransferHistory(ctx, db.BeneficiaryTransferHistoryParams{
		SenderUserID:  userID,
		BeneficiaryID: beneficiaryID,
	})
	if err != nil {
		return nil, err
	}
	out := &PayeeSuggestions{TimesPaid: len(rows)}
	if len(rows) == 0 {
		return out, nil
	}
	var gaps []int
	for i, r := range rows {
		out.TotalAmountMinor += r.SendAmountMinor
		out.LastAmountMinor = r.SendAmountMinor
		if i > 0 {
			h := int(rows[i].CompletedAt.Time.Sub(rows[i-1].CompletedAt.Time).Hours())
			if h < 0 {
				h = 0
			}
			gaps = append(gaps, h)
		}
	}
	if len(rows) > 0 {
		t := rows[0].CompletedAt.Time.UTC().Format(time.RFC3339)
		out.FirstPaidAt = &t
		t = rows[len(rows)-1].CompletedAt.Time.UTC().Format(time.RFC3339)
		out.LastPaidAt = &t
	}
	out.AverageAmountMinor = out.TotalAmountMinor / int64(len(rows))
	if len(gaps) > 0 {
		sort.Ints(gaps)
		med := gaps[len(gaps)/2]
		out.CadenceHours = &med
	}
	return out, nil
}

func serviceDigits(s string) bool {
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
