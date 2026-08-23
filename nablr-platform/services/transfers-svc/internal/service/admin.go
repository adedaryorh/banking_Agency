package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"nabla/transfers-svc/internal/models"
	"nabla/transfers-svc/internal/platform/clock"
	"nabla/transfers-svc/internal/platform/outbox"
)

var (
	ErrAdminFundAmountInvalid     = errors.New("admin: amount must be positive")
	ErrAdminFundReferenceRequired = errors.New("admin: reference is required")
	ErrAdminFundReasonRequired    = errors.New("admin: reason is required")
	ErrAdminFundCurrencyInvalid   = errors.New("admin: currency must be a 3-letter code")
)

// AdminService is the operator-facing surface for actions that move money
// outside any payment rail — a support credit, a correction. It is gated
// entirely by its caller (an API-key-checked HTTP handler); nothing in this
// type itself authenticates anyone.
//
// It is deliberately its own service rather than a method on FundingService:
// FundingService (and the routes built on it) only exists when a collections
// provider is configured — commonly false in development — and an admin's
// ability to credit a wallet must not depend on that.
type AdminService struct {
	db      DB
	ledger  *LedgerService
	wallets *WalletService
	clock   clock.Clock
}

func NewAdminService(db DB, ledger *LedgerService, wallets *WalletService, clk clock.Clock) *AdminService {
	return &AdminService{db: db, ledger: ledger, wallets: wallets, clock: clk}
}

// AdminFundInput describes a manual credit to a customer's wallet.
type AdminFundInput struct {
	CustomerID  uuid.UUID
	AmountMinor int64
	// Currency defaults to NGN when empty, and otherwise must be a 3-letter
	// code — see WalletService.OpenWallet. The wallet is opened if the
	// customer does not already have one in this currency.
	Currency string
	// Reference is the caller-supplied idempotency key: a retried request
	// with the same reference is answered from what was already posted
	// instead of crediting the wallet a second time.
	Reference string
	// Reason is a required, human-readable note. It lands on the ledger
	// transaction's description and on the customer's feed item, so "why did
	// this wallet get credited" always has an answer on record.
	Reason string
	// ActorName identifies the admin/operator for the audit trail. Optional;
	// falls back to "Admin" when empty.
	ActorName string
}

type AdminFundResult struct {
	TransactionID    uuid.UUID
	WalletID         uuid.UUID
	LedgerAccountID  uuid.UUID
	BalanceMinor     int64
	AlreadyProcessed bool
}

// FundWallet credits a customer's wallet from a platform-owned adjustment
// account, posting a balanced ledger entry, a feed item the customer sees,
// and a manual sync of the wallet's cached balance column — the same shape
// FundingService.Record uses for a real deposit, so this path is fully
// visible in the ledger rather than a side-channel balance bump.
func (s *AdminService) FundWallet(ctx context.Context, in AdminFundInput) (*AdminFundResult, error) {
	if in.AmountMinor <= 0 {
		return nil, ErrAdminFundAmountInvalid
	}
	if strings.TrimSpace(in.Reference) == "" {
		return nil, ErrAdminFundReferenceRequired
	}
	if strings.TrimSpace(in.Reason) == "" {
		return nil, ErrAdminFundReasonRequired
	}
	// Validated here (rather than left to OpenWallet) so a bad currency code
	// comes back as a typed sentinel the handler can answer with 400, not a
	// bare string error that falls through to a 500.
	requestedCurrency := strings.ToUpper(strings.TrimSpace(in.Currency))
	if requestedCurrency != "" && len(requestedCurrency) != 3 {
		return nil, ErrAdminFundCurrencyInvalid
	}

	wallet, err := s.wallets.OpenWallet(ctx, in.CustomerID, requestedCurrency, "")
	if err != nil {
		return nil, err
	}

	ref := "admin_fund:" + strings.TrimSpace(in.Reference)

	// Idempotency: a retried request with the same reference is answered from
	// what is already posted, never credited twice. The unique index on
	// ledger_transactions.reference is the real guarantee; this is the
	// cheap, friendly path that avoids racing it.
	if existing, err := s.ledger.TransactionByReference(ctx, ref); err == nil {
		return &AdminFundResult{
			TransactionID:    existing.ID,
			WalletID:         wallet.ID,
			LedgerAccountID:  wallet.LedgerAccountID,
			BalanceMinor:     balanceAfterFor(existing, wallet.LedgerAccountID),
			AlreadyProcessed: true,
		}, nil
	} else if !errors.Is(err, ErrLedgerTransactionNotFound) {
		return nil, err
	}

	actor := strings.TrimSpace(in.ActorName)
	if actor == "" {
		actor = "Admin"
	}
	reason := strings.TrimSpace(in.Reason)
	currency := wallet.Currency
	now := s.clock.Now()

	var result *AdminFundResult
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		adjustment, err := s.ledger.OpenAccountInTx(ctx, tx, OpenAccountInput{
			Type: models.AccountAdjustment, Currency: currency,
			OwnerType: "platform",
			Name:      "Admin adjustments " + currency,
			Code:      fmt.Sprintf("platform_adjustment:%s", currency),
		})
		if err != nil {
			return err
		}

		txn, err := s.ledger.PostTransaction(ctx, tx, PostTransactionInput{
			Reference:   ref,
			Kind:        models.KindAdjustment,
			Description: reason,
			Entries: []EntryInput{
				{AccountID: adjustment.ID, Direction: models.Debit, AmountMinor: in.AmountMinor, Currency: currency},
				{AccountID: wallet.LedgerAccountID, Direction: models.Credit, AmountMinor: in.AmountMinor, Currency: currency},
			},
		})
		if err != nil {
			return err
		}

		balanceAfter := balanceAfterFor(txn, wallet.LedgerAccountID)

		if _, err := s.wallets.RecordFeedItem(ctx, tx, FeedInput{
			WalletID:            wallet.ID,
			CustomerID:          in.CustomerID,
			LedgerTransactionID: txn.ID,
			Direction:           "in",
			AmountMinor:         in.AmountMinor,
			Currency:            currency,
			BalanceAfterMinor:   balanceAfter,
			TransactionType:     "admin_credit",
			CounterpartyName:    actor,
			CounterpartyType:    "admin",
			Description:         reason,
			SourceType:          "admin_adjustment",
			SourceID:            txn.ID,
			OccurredAt:          now,
		}); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE wallets SET available_minor = available_minor + $2
			 WHERE id = $1`, wallet.ID, in.AmountMinor); err != nil {
			return err
		}

		if err := outbox.Enqueue(ctx, tx, "admin_adjustment", txn.ID, "notification.money_in",
			map[string]string{
				"customer_id":  in.CustomerID.String(),
				"amount_minor": fmt.Sprintf("%d", in.AmountMinor),
				"currency":     currency,
				"from":         actor,
			}, ""); err != nil {
			return err
		}

		result = &AdminFundResult{
			TransactionID:   txn.ID,
			WalletID:        wallet.ID,
			LedgerAccountID: wallet.LedgerAccountID,
			BalanceMinor:    balanceAfter,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// inTx runs fn inside one database transaction, rolling back on any error —
// identical in shape to FundingService.inTx.
func (s *AdminService) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
