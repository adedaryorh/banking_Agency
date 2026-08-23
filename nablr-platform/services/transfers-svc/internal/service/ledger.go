package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"nabla/transfers-svc/internal/models"
	"nabla/transfers-svc/internal/platform/clock"
	"nabla/transfers-svc/internal/platform/id"
)

// LedgerService manages double-entry ledger accounts, holds, and transactions
type LedgerService struct {
	db    DB
	clock clock.Clock
}

func NewLedgerService(db DB, clk clock.Clock) *LedgerService {
	return &LedgerService{db: db, clock: clk}
}

type OpenAccountInput struct {
	Type       models.AccountType
	Currency   string
	OwnerType  string
	CustomerID uuid.UUID

	ProviderName string
	PurposeKey   string
	Name         string
	Code         string
}

func (s *LedgerService) OpenAccount(ctx context.Context, in OpenAccountInput) (*models.LedgerAccount, error) {
	acctID := id.New()
	now := s.clock.Now()

	_, err := s.db.Exec(ctx, `
		INSERT INTO ledger_accounts
			(id, account_type, currency, owner_type, customer_id,
			 name, code, balance_minor, reserved_minor, pending_minor,
			 allows_negative, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 0, 0, 0, $8, 'active', $9)`,
		acctID, in.Type, in.Currency, in.OwnerType, in.CustomerID,
		in.Name, in.Code, allowsNegative(in.OwnerType), now)
	if err != nil {
		return nil, fmt.Errorf("ledger: open account: %w", err)
	}

	return s.AccountByID(ctx, acctID)
}

// allowsNegative reports whether an account may go below zero.
//
// Platform and provider accounts hold POSITIONS: the collections clearing
// account goes negative the instant money arrives, because the provider is
// holding it for us until they settle. Customer money may never go negative —
// the ledger_accounts_no_overdraft CHECK is the last line of defence, and this
// is what decides who it applies to.
func allowsNegative(ownerType string) bool { return ownerType != "customer" }

func (s *LedgerService) AccountByID(ctx context.Context, accountID uuid.UUID) (*models.LedgerAccount, error) {
	var a models.LedgerAccount
	err := s.db.QueryRow(ctx, `
		SELECT id, account_type, currency, owner_type, customer_id,
		       coalesce(provider_name, ''), name, code,
		       balance_minor, reserved_minor, pending_minor, available_minor,
		       status, created_at
		FROM ledger_accounts
		WHERE id = $1`, accountID).
		Scan(&a.ID, &a.Type, &a.Currency, &a.OwnerType, &a.CustomerID,
			&a.ProviderName, &a.Name, &a.Code,
			&a.BalanceMinor, &a.ReservedMinor, &a.PendingMinor, &a.AvailableMinor,
			&a.Status, &a.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, models.ErrLedgerAccountNotFound
		}
		return nil, err
	}
	return &a, nil
}

// ---------------------------------------------------------------------------
// Holds
// ---------------------------------------------------------------------------

type PlaceHoldInput struct {
	AccountID   uuid.UUID
	AmountMinor int64
	Currency    string
	HoldType    models.HoldType
	SourceType  string
	SourceID    uuid.UUID
	TTL         time.Duration
}

func (s *LedgerService) PlaceHold(ctx context.Context, in PlaceHoldInput) (*models.Hold, error) {
	if in.AmountMinor <= 0 {
		return nil, models.ErrLedgerZeroAmount
	}

	holdID := id.New()
	now := s.clock.Now()
	expiresAt := now.Add(in.TTL)
	ref := fmt.Sprintf("hold:%s", holdID.String()[:8])

	// Check available balance and reserve funds atomically
	var available int64
	err := s.db.QueryRow(ctx, `
		WITH balance_check AS (
			SELECT available_minor
			FROM ledger_accounts
			WHERE id = $1 AND status = 'active'
			FOR UPDATE
		)
		INSERT INTO holds
			(id, account_id, reference, amount_minor, currency, hold_type,
			 status, source_type, source_id, expires_at, created_at)
		SELECT $2, $1, $3, $4, $5, $6, 'active', $7, $8, $9, $10
		FROM balance_check
		WHERE available_minor >= $4
		RETURNING (SELECT available_minor FROM balance_check)`,
		in.AccountID, holdID, ref, in.AmountMinor, in.Currency, in.HoldType,
		in.SourceType, in.SourceID, expiresAt, now).Scan(&available)

	if err != nil {
		if err == pgx.ErrNoRows {
			// Check if account exists or insufficient funds
			var exists bool
			_ = s.db.QueryRow(ctx, "SELECT true FROM ledger_accounts WHERE id = $1", in.AccountID).Scan(&exists)
			if !exists {
				return nil, models.ErrLedgerAccountNotFound
			}
			return nil, models.ErrInsufficientFunds
		}
		return nil, err
	}

	// Raise the reservation. available_minor is GENERATED from
	// balance_minor - reserved_minor (migration 005), so it follows from this
	// and must not be written: a stored copy is a second answer that can drift.
	_, err = s.db.Exec(ctx, `
		UPDATE ledger_accounts
		SET reserved_minor = reserved_minor + $2, updated_at = now()
		WHERE id = $1`, in.AccountID, in.AmountMinor)
	if err != nil {
		return nil, err
	}

	return s.HoldByID(ctx, holdID)
}

func (s *LedgerService) HoldByID(ctx context.Context, holdID uuid.UUID) (*models.Hold, error) {
	var h models.Hold
	err := s.db.QueryRow(ctx, `
		SELECT id, account_id, reference, amount_minor, currency, hold_type,
		       status, source_type, source_id, expires_at, created_at,
		       captured_at, released_at
		FROM holds
		WHERE id = $1`, holdID).
		Scan(&h.ID, &h.AccountID, &h.Reference, &h.AmountMinor, &h.Currency, &h.HoldType,
			&h.Status, &h.SourceType, &h.SourceID, &h.ExpiresAt, &h.CreatedAt,
			&h.CapturedAt, &h.ReleasedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, models.ErrHoldNotFound
		}
		return nil, err
	}
	return &h, nil
}

func (s *LedgerService) CaptureHold(ctx context.Context, tx pgx.Tx, holdID uuid.UUID) error {
	now := s.clock.Now()
	var accountID uuid.UUID
	var amount int64

	err := tx.QueryRow(ctx, `
		UPDATE holds
		SET status = 'captured', captured_at = $2
		WHERE id = $1 AND status = 'active'
		RETURNING account_id, amount_minor`,
		holdID, now).Scan(&accountID, &amount)

	if err != nil {
		if err == pgx.ErrNoRows {
			return models.ErrHoldNotActive
		}
		return err
	}

	// Release the reservation only. The posting that follows debits
	// balance_minor, and available_minor = balance - reserved falls out of the
	// two together: the customer's spendable figure does not move at capture,
	// because the money was already spoken for.
	_, err = tx.Exec(ctx, `
		UPDATE ledger_accounts
		SET reserved_minor = reserved_minor - $2, updated_at = now()
		WHERE id = $1`, accountID, amount)

	return err
}

func (s *LedgerService) ReleaseHold(ctx context.Context, holdID uuid.UUID) error {
	now := s.clock.Now()
	var accountID uuid.UUID
	var amount int64

	err := s.db.QueryRow(ctx, `
		UPDATE holds
		SET status = 'released', released_at = $2
		WHERE id = $1 AND status = 'active'
		RETURNING account_id, amount_minor`,
		holdID, now).Scan(&accountID, &amount)

	if err != nil {
		if err == pgx.ErrNoRows {
			return models.ErrHoldNotActive
		}
		return err
	}

	// Drop the reservation; the money becomes spendable again because
	// available_minor is derived from it.
	_, err = s.db.Exec(ctx, `
		UPDATE ledger_accounts
		SET reserved_minor = reserved_minor - $2, updated_at = now()
		WHERE id = $1`, accountID, amount)

	return err
}

// ---------------------------------------------------------------------------
// Transactions
// ---------------------------------------------------------------------------

type PostTransactionInput struct {
	Reference   string
	Kind        models.TransactionKind
	Description string
	Entries     []EntryInput
}

type EntryInput struct {
	AccountID   uuid.UUID
	Direction   models.Direction
	AmountMinor int64
	Currency    string
	Description string
}

func (s *LedgerService) PostTransaction(ctx context.Context, tx pgx.Tx, in PostTransactionInput) (*models.Transaction, error) {
	// Validate transaction balances
	if err := validateTransaction(in.Entries); err != nil {
		return nil, err
	}

	txnID := id.New()
	now := s.clock.Now()

	_, err := tx.Exec(ctx, `
		INSERT INTO ledger_transactions
			(id, reference, kind, description, status, posted_at)
		VALUES ($1, $2, $3, $4, 'posted', $5)`,
		txnID, in.Reference, in.Kind, in.Description, now)
	if err != nil {
		return nil, err
	}

	// Post entries and update account balances
	var entries []models.Entry
	for _, e := range in.Entries {
		entryID := id.New()

		// Get current balance and lock account
		var currentBalance int64
		err := tx.QueryRow(ctx, `
			SELECT balance_minor FROM ledger_accounts
			WHERE id = $1 FOR UPDATE`, e.AccountID).Scan(&currentBalance)
		if err != nil {
			return nil, err
		}

		// Calculate new balance
		var newBalance int64
		if e.Direction == models.Debit {
			newBalance = currentBalance - e.AmountMinor
		} else {
			newBalance = currentBalance + e.AmountMinor
		}

		// Insert entry
		_, err = tx.Exec(ctx, `
			INSERT INTO ledger_entries
				(id, transaction_id, account_id, direction, amount_minor,
				 currency, balance_after_minor, description, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			entryID, txnID, e.AccountID, e.Direction, e.AmountMinor,
			e.Currency, newBalance, e.Description, now)
		if err != nil {
			return nil, err
		}

		// Only the balance moves. available_minor is derived from it, and the
		// no-overdraft CHECK on the account is what refuses a debit that would
		// take a customer below their reservations — in the database, where a
		// concurrent debit cannot slip past it.
		_, err = tx.Exec(ctx, `
			UPDATE ledger_accounts
			SET balance_minor = $2, updated_at = now()
			WHERE id = $1`, e.AccountID, newBalance)
		if err != nil {
			return nil, err
		}

		entries = append(entries, models.Entry{
			ID:                entryID,
			TransactionID:     txnID,
			AccountID:         e.AccountID,
			Direction:         e.Direction,
			AmountMinor:       e.AmountMinor,
			Currency:          e.Currency,
			BalanceAfterMinor: newBalance,
			Description:       e.Description,
			CreatedAt:         now,
		})
	}

	return &models.Transaction{
		ID:          txnID,
		Reference:   in.Reference,
		Kind:        in.Kind,
		Description: in.Description,
		Status:      "posted",
		PostedAt:    now,
		Entries:     entries,
	}, nil
}

func validateTransaction(entries []EntryInput) error {
	if len(entries) == 0 {
		return fmt.Errorf("ledger: transaction must have at least one entry")
	}

	// Group by currency and check balance
	balances := make(map[string]int64)
	for _, e := range entries {
		if e.AmountMinor <= 0 {
			return models.ErrLedgerZeroAmount
		}
		if e.Direction == models.Debit {
			balances[e.Currency] -= e.AmountMinor
		} else {
			balances[e.Currency] += e.AmountMinor
		}
	}

	// Each currency must balance to zero
	for curr, bal := range balances {
		if bal != 0 {
			return fmt.Errorf("%w: %s is off by %d", models.ErrUnbalancedTransaction, curr, bal)
		}
	}

	return nil
}
