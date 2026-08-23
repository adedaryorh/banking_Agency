package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"nabla/transfers-svc/internal/models"
	"nabla/transfers-svc/internal/platform/id"
)

func (s *LedgerService) OpenAccountInTx(ctx context.Context, tx pgx.Tx, in OpenAccountInput) (*models.LedgerAccount, error) {
	if in.Code == "" {
		return nil, fmt.Errorf("ledger: an account needs a code")
	}
	acctID := id.New()
	now := s.clock.Now()

	var out uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO ledger_accounts
			(id, account_type, currency, owner_type, customer_id, provider_name,
			 name, code, balance_minor, reserved_minor, pending_minor,
			 allows_negative, status, created_at)
		VALUES ($1, $2, $3, $4, NULLIF($5, '00000000-0000-0000-0000-000000000000'::uuid),
		        NULLIF($6, ''), $7, $8, 0, 0, 0, $9, 'active', $10)
		ON CONFLICT (code) DO UPDATE SET updated_at = EXCLUDED.created_at
		RETURNING id`,
		acctID, in.Type, in.Currency, in.OwnerType, in.CustomerID, in.ProviderName,
		in.Name, in.Code, allowsNegative(in.OwnerType), now).Scan(&out)
	if err != nil {
		return nil, fmt.Errorf("ledger: open account: %w", err)
	}
	return s.accountByIDInTx(ctx, tx, out)
}

func (s *LedgerService) accountByIDInTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) (*models.LedgerAccount, error) {
	var a models.LedgerAccount
	err := tx.QueryRow(ctx, `
		SELECT id, account_type, currency, owner_type,
		       coalesce(customer_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       coalesce(provider_name, ''), name, code,
		       balance_minor, reserved_minor, pending_minor, available_minor,
		       status, created_at
		  FROM ledger_accounts WHERE id = $1`, accountID).
		Scan(&a.ID, &a.Type, &a.Currency, &a.OwnerType, &a.CustomerID,
			&a.ProviderName, &a.Name, &a.Code,
			&a.BalanceMinor, &a.ReservedMinor, &a.PendingMinor, &a.AvailableMinor,
			&a.Status, &a.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrLedgerAccountNotFound
		}
		return nil, err
	}
	return &a, nil
}

// CaptureHoldInTx turns a hold into a settled posting, in one transaction.
//
// The hold is resolved FIRST and the posting only happens if this caller was
// the one that resolved it: `AND status = 'active'` means a second capture
// updates zero rows, and without that check a redelivered settlement webhook
// would post the journal twice against a hold that was already spent.
func (s *LedgerService) CaptureHoldInTx(ctx context.Context, tx pgx.Tx, holdID uuid.UUID,
	in PostTransactionInput) (*models.Transaction, error) {

	if err := s.CaptureHold(ctx, tx, holdID); err != nil {
		return nil, err
	}
	return s.PostTransaction(ctx, tx, in)
}

// ReleaseHoldInTx hands a hold's money back, bound to a caller's transaction.
func (s *LedgerService) ReleaseHoldInTx(ctx context.Context, tx pgx.Tx, holdID uuid.UUID) error {
	now := s.clock.Now()
	var accountID uuid.UUID
	var amount int64

	err := tx.QueryRow(ctx, `
		UPDATE holds
		   SET status = 'released', released_at = $2
		 WHERE id = $1 AND status = 'active'
		RETURNING account_id, amount_minor`, holdID, now).Scan(&accountID, &amount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.ErrHoldNotActive
		}
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE ledger_accounts
		   SET reserved_minor = reserved_minor - $2, updated_at = now()
		 WHERE id = $1`, accountID, amount)
	return err
}

// TransactionByReference reads a posted transaction and its entries by the
// reference the poster chose.
//
// The reference is unique, so this is how a caller that crashed after posting
// finds what it already did rather than refusing forever — see Refund.
func (s *LedgerService) TransactionByReference(ctx context.Context, reference string) (*models.Transaction, error) {
	var t models.Transaction
	err := s.db.QueryRow(ctx, `
		SELECT id, reference, kind, coalesce(description, ''), status, posted_at
		  FROM ledger_transactions WHERE reference = $1`, reference).
		Scan(&t.ID, &t.Reference, &t.Kind, &t.Description, &t.Status, &t.PostedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrLedgerTransactionNotFound
		}
		return nil, err
	}
	entries, err := s.entriesOf(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	t.Entries = entries
	return &t, nil
}

func (s *LedgerService) entriesOf(ctx context.Context, txnID uuid.UUID) ([]models.Entry, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, transaction_id,
		       coalesce(account_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       coalesce(direction, entry_type), amount_minor, currency,
		       coalesce(balance_after_minor, 0), coalesce(description, ''), created_at
		  FROM ledger_entries WHERE transaction_id = $1 ORDER BY created_at`, txnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Entry
	for rows.Next() {
		var e models.Entry
		if err := rows.Scan(&e.ID, &e.TransactionID, &e.AccountID, &e.Direction,
			&e.AmountMinor, &e.Currency, &e.BalanceAfterMinor, &e.Description,
			&e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ErrLedgerTransactionNotFound is returned when no transaction carries the
// reference asked for.
var ErrLedgerTransactionNotFound = errors.New("ledger: transaction not found")

// ReverseInTx undoes a posted transaction by mirroring its entries.
//
// Mirroring rather than posting a fresh pair: a refund reversing the
// settlement's OWN entries cannot drift from what it undoes, and the two are
// visibly one event. The reversal carries its own reference, which is unique —
// so a webhook delivered twice fails the second time rather than paying twice.
func (s *LedgerService) ReverseInTx(ctx context.Context, tx pgx.Tx, originalID uuid.UUID,
	reference, description string) (*models.Transaction, error) {

	original, err := s.transactionByIDInTx(ctx, tx, originalID)
	if err != nil {
		return nil, err
	}
	if original.Status == "reversed" {
		return nil, fmt.Errorf("ledger: transaction %s is already reversed", originalID)
	}

	mirrored := make([]EntryInput, 0, len(original.Entries))
	for _, e := range original.Entries {
		flipped := models.Credit
		if e.Direction == models.Credit {
			flipped = models.Debit
		}
		mirrored = append(mirrored, EntryInput{
			AccountID:   e.AccountID,
			Direction:   flipped,
			AmountMinor: e.AmountMinor,
			Currency:    e.Currency,
			Description: description,
		})
	}

	reversal, err := s.PostTransaction(ctx, tx, PostTransactionInput{
		Reference:   reference,
		Kind:        original.Kind,
		Description: description,
		Entries:     mirrored,
	})
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE ledger_transactions SET status = 'reversed' WHERE id = $1`, originalID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ledger_transactions SET reversal_of = $2 WHERE id = $1`,
		reversal.ID, originalID); err != nil {
		return nil, err
	}
	return reversal, nil
}

// transactionByReferenceInTx is TransactionByReference bound to a caller's
// transaction. A refund needs it there: the check for "did a previous attempt
// already post this reversal?" and the write that follows must see the same
// snapshot, or two attempts both conclude nothing was posted.
func (s *LedgerService) transactionByReferenceInTx(ctx context.Context, tx pgx.Tx,
	reference string) (*models.Transaction, error) {

	var txnID uuid.UUID
	err := tx.QueryRow(ctx,
		`SELECT id FROM ledger_transactions WHERE reference = $1`, reference).Scan(&txnID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrLedgerTransactionNotFound
		}
		return nil, err
	}
	return s.transactionByIDInTx(ctx, tx, txnID)
}

func (s *LedgerService) transactionByIDInTx(ctx context.Context, tx pgx.Tx, txnID uuid.UUID) (*models.Transaction, error) {
	var t models.Transaction
	err := tx.QueryRow(ctx, `
		SELECT id, reference, kind, coalesce(description, ''), status, posted_at
		  FROM ledger_transactions WHERE id = $1`, txnID).
		Scan(&t.ID, &t.Reference, &t.Kind, &t.Description, &t.Status, &t.PostedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrLedgerTransactionNotFound
		}
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT id, transaction_id,
		       coalesce(account_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       coalesce(direction, entry_type), amount_minor, currency,
		       coalesce(balance_after_minor, 0), coalesce(description, ''), created_at
		  FROM ledger_entries WHERE transaction_id = $1 ORDER BY created_at`, txnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e models.Entry
		if err := rows.Scan(&e.ID, &e.TransactionID, &e.AccountID, &e.Direction,
			&e.AmountMinor, &e.Currency, &e.BalanceAfterMinor, &e.Description,
			&e.CreatedAt); err != nil {
			return nil, err
		}
		t.Entries = append(t.Entries, e)
	}
	return &t, rows.Err()
}
