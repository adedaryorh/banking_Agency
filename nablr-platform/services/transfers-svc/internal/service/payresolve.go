package service

import (
	"context"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var onlyDigits = regexp.MustCompile(`\D`)

const nablrResolveLimit = 6

// PayResolveMatch is one customer the payer could send to instantly.
type PayResolveMatch struct {
	CustomerID    string `json:"customer_id"`
	Name          string `json:"name"`
	Username      string `json:"username"`
	AccountNumber string `json:"account_number"`
	// Why we think this is them, in words the app can show: "that phone
	// number", "@handle". It is the difference between a confident answer and
	// a coincidence.
	MatchedOn string `json:"matched_on"`
}

// CallerWallet is one of the payer's own wallets, served with live balances so
// the "who are you paying" screen can price the payment without a second call.
type CallerWallet struct {
	ID             uuid.UUID `json:"id"`
	Currency       string    `json:"currency"`
	Name           string    `json:"name"`
	IsDefault      bool      `json:"is_default"`
	BalanceMinor   int64     `json:"balance_minor"`
	AvailableMinor int64     `json:"available_minor"`
	ReservedMinor  int64     `json:"reserved_minor"`
	PendingMinor   int64     `json:"pending_minor"`
}

// PayResolveResult is returned to the app while it renders the "who are you
// paying" screen.
type PayResolveResult struct {
	Query  string            `json:"query"`
	Digits string            `json:"digits"`
	Kind   string            `json:"kind"`
	Nablr  []PayResolveMatch `json:"nablr"`
	// BankReady says a NUBAN enquiry is possible from what was typed. The app
	// moves the person on to name enquiry when neither a Nablr match nor this
	// boolean has produced a confident answer.
	BankReady bool `json:"bank_ready"`
	// Caller carries the payer's own wallets so the screen can render "you have
	// N..." on the same round-trip instead of a second API call. Degrades to an
	// empty list when there is no pool or the parse fails, never an error.
	Caller []CallerWallet `json:"caller"`
}

// ResolvePay classifies what was typed and finds any Nablr customer it names.
func (s *Service) ResolvePay(ctx context.Context, q, me string) PayResolveResult {
	q = strings.TrimSpace(q)
	digits := onlyDigits.ReplaceAllString(q, "")
	nuban := toNUBAN(digits)

	result := PayResolveResult{
		Query:     q,
		Digits:    nuban,
		Kind:      classifyPay(q, digits),
		Nablr:     s.findNablr(ctx, q, nuban, me),
		BankReady: len(nuban) == 10,
	}
	result.Caller = s.callerWallets(ctx, me)
	return result
}

// callerWallets lists the payer's own active wallets with live ledger balances.
// Balances come from the backing ledger account — the single spendable truth —
// never the mirror columns on wallets. Fail-open: an empty list rather than
// failing the whole resolve screen for a data problem on the payer's own side.
func (s *Service) callerWallets(ctx context.Context, me string) []CallerWallet {
	if s.pool == nil {
		return nil
	}
	customerID, err := uuid.Parse(me)
	if err != nil {
		return nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT w.id, w.currency, w.name, w.is_default,
		       la.balance_minor, la.reserved_minor, la.pending_minor, la.available_minor
		  FROM wallets w
		  JOIN ledger_accounts la ON la.id = w.ledger_account_id
		 WHERE (w.customer_id = $1 OR w.user_id = $1) AND w.status <> 'closed'
		 ORDER BY w.is_default DESC, w.created_at`, customerID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := []CallerWallet{}
	for rows.Next() {
		var w CallerWallet
		if err := rows.Scan(&w.ID, &w.Currency, &w.Name, &w.IsDefault,
			&w.BalanceMinor, &w.ReservedMinor, &w.PendingMinor, &w.AvailableMinor); err != nil {
			return nil
		}
		out = append(out, w)
	}
	if rows.Err() != nil {
		return nil
	}
	return out
}

// toNUBAN reduces what somebody typed to the ten digits a Nigerian account
// number actually is. 08031234567, +2348031234567 and 8031234567 are the same
// person; only one of those is what gets matched.
func toNUBAN(digits string) string {
	switch {
	case len(digits) == 10:
		return digits
	case len(digits) == 11 && strings.HasPrefix(digits, "0"):
		return digits[1:]
	case len(digits) == 13 && strings.HasPrefix(digits, "234"):
		return digits[3:]
	case len(digits) == 14 && strings.HasPrefix(digits, "2340"):
		return digits[4:]
	}
	return digits
}

// classifyPay says what the app should show while somebody is still typing, so
// the screen can be useful before the answer arrives.
func classifyPay(q, digits string) string {
	switch {
	case strings.HasPrefix(q, "@"):
		return "handle"
	case strings.Contains(q, "@"):
		return "email"
	case digits == "" && q != "":
		return "name"
	case len(toNUBAN(digits)) == 10:
		// Ten digits is genuinely both. Saying so is more honest than picking
		// one and being wrong half the time.
		return "number"
	case digits != "":
		return "partial_number"
	default:
		return "empty"
	}
}

// findNablr looks for a customer the payer could send to instantly.
//
// The customer store is identity-svc's, not ours. transfers-svc deliberately
// carries no users/customers tables, so resolution is a gRPC call rather than a
// local join: an exact Nablr number (which is also what a phone number
// normalises to) is resolved by account number, and a handle by username.
// Matching by name fragment is deliberately not attempted — identity-svc has no
// search endpoint, and a guessed name shown as if it were an answer is worse
// than none. When identity is down the Nablr half degrades to empty rather
// than failing the whole screen: the money-critical paths fail closed, this
// read-only box does not.
//
// Never returns the payer themselves: offering somebody the option of paying
// their own account is a bug that looks like a feature until they try it.
func (s *Service) findNablr(ctx context.Context, q, nuban, me string) []PayResolveMatch {
	out := []PayResolveMatch{}
	if s.identity == nil || strings.TrimSpace(q) == "" {
		return out
	}

	// Ten digits is the account handle, and toNUBAN has already normalised the
	// leading-zero and +234 forms of a phone number onto it. Resolving the
	// number IS resolving the person: their Nablr number is their phone number.
	if nuban != "" {
		if u, err := s.identity.GetUserByAccountNumber(ctx, nuban); err == nil && u.UserID != "" && u.UserID != me {
			out = append(out, matchOf(ctx, s, u, "their Nablr number"))
		}
		return out
	}

	// A handle typed with or without the @. Exact surname/username only — a
	// fragment cannot be a confident answer.
	if handle := strings.TrimPrefix(strings.TrimSpace(q), "@"); handle != "" {
		if u, err := s.identity.GetUserByUsername(ctx, handle); err == nil && u.UserID != "" && u.UserID != me {
			out = append(out, matchOf(ctx, s, u, "their @handle"))
		}
	}
	return out
}

func matchOf(ctx context.Context, s *Service, u UserProfile, matchedOn string) PayResolveMatch {
	m := PayResolveMatch{
		CustomerID:    u.UserID,
		Name:          u.NablrUsername,
		Username:      u.NablrUsername,
		AccountNumber: u.AccountNumber,
		MatchedOn:     matchedOn,
	}
	// The screen reads the name out loud, so prefer the legal name when it is
	// reachable, falling back to the handle. A KYC miss degrades the label,
	// never the match.
	id, err := uuid.Parse(u.UserID)
	if err != nil {
		return m
	}
	if p, err := s.identity.GetKYCProfile(ctx, id); err == nil {
		if name := strings.TrimSpace(p.FirstName + " " + p.LastName); name != "" {
			m.Name = name
		}
	}
	return m
}
