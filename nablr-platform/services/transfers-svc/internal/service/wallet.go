package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"nabla/transfers-svc/internal/models"
	"nabla/transfers-svc/internal/platform/clock"
	"nabla/transfers-svc/internal/platform/id"
)

// WalletService manages customer wallets
type WalletService struct {
	db    DB
	clock clock.Clock
}

func NewWalletService(db DB, clk clock.Clock) *WalletService {
	return &WalletService{db: db, clock: clk}
}

const walletColumns = `
	w.id, w.account_id, w.customer_id, w.ledger_account_id, w.currency,
	w.name, w.wallet_type, w.is_default, w.status, w.created_at,
	la.balance_minor, la.reserved_minor, la.pending_minor, la.available_minor`

func scanWallet(row pgx.Row) (*models.Wallet, error) {
	var w models.Wallet
	err := row.Scan(&w.ID, &w.AccountID, &w.CustomerID, &w.LedgerAccountID,
		&w.Currency, &w.Name, &w.Type, &w.IsDefault, &w.Status, &w.CreatedAt,
		&w.BalanceMinor, &w.ReservedMinor, &w.PendingMinor, &w.AvailableMinor)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, models.ErrWalletNotFound
		}
		return nil, err
	}
	return &w, nil
}

// WalletByID retrieves a wallet with ownership check. Balances come from the
// backing ledger account — the single spendable truth — never from the mirror
// columns on wallets.
func (s *WalletService) WalletByID(ctx context.Context, walletID, customerID uuid.UUID) (*models.Wallet, error) {
	return scanWallet(s.db.QueryRow(ctx, `
		SELECT `+walletColumns+`
		  FROM wallets w
		  JOIN ledger_accounts la ON la.id = w.ledger_account_id
		 WHERE w.id = $1 AND w.customer_id = $2 AND w.status = 'active'`,
		walletID, customerID))
}

// WalletsForCustomer lists a customer's wallets with live balances.
// Ported from usenablr-1.0 modules/wallet/app/service.go.
func (s *WalletService) WalletsForCustomer(ctx context.Context, customerID uuid.UUID) ([]models.Wallet, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+walletColumns+`
		  FROM wallets w
		  JOIN ledger_accounts la ON la.id = w.ledger_account_id
		 WHERE w.customer_id = $1 AND w.status <> 'closed'
		 ORDER BY w.is_default DESC, w.created_at`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.Wallet
	for rows.Next() {
		var w models.Wallet
		if err := rows.Scan(&w.ID, &w.AccountID, &w.CustomerID, &w.LedgerAccountID,
			&w.Currency, &w.Name, &w.Type, &w.IsDefault, &w.Status, &w.CreatedAt,
			&w.BalanceMinor, &w.ReservedMinor, &w.PendingMinor, &w.AvailableMinor); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *WalletService) OpenWallet(ctx context.Context, customerID uuid.UUID, currency, name string) (*models.Wallet, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "NGN"
	} else if len(currency) != 3 {
		return nil, errors.New("currency must be a 3-letter code")
	}
	if existing, err := s.WalletsForCustomer(ctx, customerID); err == nil {
		for i := range existing {
			if existing[i].Currency == currency {
				return &existing[i], nil
			}
		}
	}

	walletID := id.New()
	accountID := id.New()
	now := s.clock.Now()
	if name == "" {
		name = "Main wallet"
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO ledger_accounts
			(id, account_type, currency, owner_type, customer_id,
			 name, code, balance_minor, reserved_minor, pending_minor,
			 allows_negative, status, created_at)
		VALUES ($1, 'customer_asset', $2, 'customer', $3, $4, $5, 0, 0, 0, false, 'active', $6)`,
		accountID, currency, customerID, name,
		"wlt-"+walletID.String()[:8], now)
	if err != nil {
		return nil, err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO wallets
			(id, user_id, customer_id, currency, ledger_account_id, account_id,
			 name, wallet_type, is_default, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'spending', true, 'active', $8)
		ON CONFLICT (user_id, currency) DO UPDATE SET updated_at = now()`,
		walletID, customerID, customerID, currency, accountID, accountID,
		name, now)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.WalletByID(ctx, walletID, customerID)
}

// RecordFeedItem adds an entry to the customer's transaction feed
type FeedInput struct {
	WalletID            uuid.UUID
	CustomerID          uuid.UUID
	LedgerTransactionID uuid.UUID
	Direction           string
	AmountMinor         int64
	Currency            string
	FeeMinor            int64
	BalanceAfterMinor   int64
	TransactionType     string
	CounterpartyName    string
	CounterpartyType    string
	CounterpartyID      uuid.UUID
	Description         string
	SourceType          string
	SourceID            uuid.UUID
	OccurredAt          time.Time
}

func (s *WalletService) RecordFeedItem(ctx context.Context, tx pgx.Tx, in FeedInput) (uuid.UUID, error) {
	feedID := id.New()
	_, err := tx.Exec(ctx, `
		INSERT INTO wallet_transactions
			(id, wallet_id, customer_id, ledger_transaction_id, direction,
			 amount_minor, currency, fee_minor, balance_after_minor,
			 transaction_type, counterparty_name, counterparty_type,
			 description, source_type, source_id, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		feedID, in.WalletID, in.CustomerID, in.LedgerTransactionID, in.Direction,
		in.AmountMinor, in.Currency, in.FeeMinor, in.BalanceAfterMinor,
		in.TransactionType, in.CounterpartyName, in.CounterpartyType,
		in.Description, in.SourceType, in.SourceID, in.OccurredAt)
	return feedID, err
}

const feedColumns = `
	wt.id, wt.wallet_id, wt.ledger_transaction_id, wt.direction, wt.amount_minor,
	wt.currency, wt.fee_minor, wt.balance_after_minor, wt.transaction_type,
	coalesce(wt.counterparty_name, ''), coalesce(wt.counterparty_type, ''),
	coalesce(wt.description, ''),
	coalesce(tr.status, 'completed'),
	wt.occurred_at, coalesce(wt.source_type, ''),
	coalesce(wt.source_id, '00000000-0000-0000-0000-000000000000'::uuid),
	coalesce(lt.reference, '')`

func scanFeedItem(row pgx.Row) (models.FeedItem, error) {
	var f models.FeedItem
	err := row.Scan(&f.ID, &f.WalletID, &f.LedgerTransactionID, &f.Direction,
		&f.AmountMinor, &f.Currency, &f.FeeMinor, &f.BalanceAfterMinor,
		&f.TransactionType, &f.CounterpartyName, &f.CounterpartyType,
		&f.Description, &f.Status, &f.OccurredAt, &f.SourceType, &f.SourceID,
		&f.Reference)
	return f, err
}

// Feed pages a wallet's transaction feed with cursor pagination. Ported from
// usenablr-1.0 modules/wallet/app/service.go.
func (s *WalletService) Feed(ctx context.Context, walletID, customerID uuid.UUID, before time.Time, beforeID uuid.UUID, limit int) ([]models.FeedItem, error) {
	if _, err := s.WalletByID(ctx, walletID, customerID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	if before.IsZero() {
		before = s.clock.Now().Add(time.Hour)
	}
	if beforeID == uuid.Nil {
		beforeID = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	}

	rows, err := s.db.Query(ctx, `
		SELECT `+feedColumns+`
		  FROM wallet_transactions wt
		  LEFT JOIN ledger_transactions lt ON lt.id = wt.ledger_transaction_id
		  LEFT JOIN transfers tr ON wt.source_type = 'transfer' AND tr.id = wt.source_id
		 WHERE wt.wallet_id = $1 AND (wt.occurred_at, wt.id) < ($2, $3)
		 ORDER BY wt.occurred_at DESC, wt.id DESC
		 LIMIT $4`, walletID, before, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.FeedItem
	for rows.Next() {
		f, err := scanFeedItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Statement returns the wallet's activity within [from, to), oldest first.
func (s *WalletService) Statement(ctx context.Context, walletID, customerID uuid.UUID, from, to time.Time) ([]models.FeedItem, error) {
	if _, err := s.WalletByID(ctx, walletID, customerID); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(ctx, `
		SELECT `+feedColumns+`
		  FROM wallet_transactions wt
		  LEFT JOIN ledger_transactions lt ON lt.id = wt.ledger_transaction_id
		  LEFT JOIN transfers tr ON wt.source_type = 'transfer' AND tr.id = wt.source_id
		 WHERE wt.wallet_id = $1 AND wt.occurred_at >= $2 AND wt.occurred_at < $3
		 ORDER BY wt.occurred_at ASC, wt.id ASC`, walletID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.FeedItem
	for rows.Next() {
		f, err := scanFeedItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Insights summarises one wallet's month. Ported from usenablr-1.0
// modules/wallet/app/insights.go; the only deviation is that transfers-svc has
// no charity rail, so giving is always zero until one exists.
func (s *WalletService) Insights(ctx context.Context, walletID, customerID uuid.UUID) (*models.WalletInsights, error) {
	w, err := s.WalletByID(ctx, walletID, customerID)
	if err != nil {
		return nil, err
	}

	now := s.clock.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	lastMonthStart := monthStart.AddDate(0, -1, 0)
	nextMonthStart := monthStart.AddDate(0, 1, 0)

	out := &models.WalletInsights{
		Currency:       w.Currency,
		AvailableMinor: w.AvailableMinor,
		OnHoldMinor:    w.ReservedMinor,
		DaysElapsed:    now.Day(),
		DaysInMonth:    nextMonthStart.AddDate(0, 0, -1).Day(),
	}

	err = s.db.QueryRow(ctx, `
		SELECT
		  coalesce(sum(amount_minor) FILTER (WHERE direction = 'in'  AND occurred_at >= $2), 0),
		  coalesce(sum(amount_minor) FILTER (WHERE direction = 'out' AND occurred_at >= $2), 0),
		  coalesce(sum(amount_minor) FILTER (WHERE direction = 'out'
		           AND occurred_at >= $3 AND occurred_at < $2), 0),
		  coalesce(sum(amount_minor) FILTER (WHERE direction = 'out' AND occurred_at >= $2
		           AND transaction_type IN ('zakat', 'donation')), 0)
		  FROM wallet_transactions
		 WHERE wallet_id = $1`,
		walletID, monthStart, lastMonthStart).
		Scan(&out.MonthIn, &out.MonthOut, &out.LastMonthOut, &out.GivingMinor)
	if err != nil {
		return nil, err
	}

	if out.LastMonthOut > 0 {
		pct := int(float64(out.MonthOut-out.LastMonthOut) / float64(out.LastMonthOut) * 100)
		out.OutChangePct = &pct
	}
	if out.DaysElapsed > 0 {
		out.DailyOut = out.MonthOut / int64(out.DaysElapsed)
		out.ProjectedOut = out.DailyOut * int64(out.DaysInMonth)
	}

	rows, err := s.db.Query(ctx, `
		SELECT transaction_type, sum(amount_minor), count(*)
		  FROM wallet_transactions
		 WHERE wallet_id = $1 AND direction = 'out' AND occurred_at >= $2
		 GROUP BY 1 ORDER BY 2 DESC`, walletID, monthStart)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t models.TypeSpend
		if err := rows.Scan(&t.Type, &t.AmountMinor, &t.Count); err != nil {
			return nil, err
		}
		if out.MonthOut > 0 {
			t.SharePct = int(t.AmountMinor * 100 / out.MonthOut)
		}
		out.ByType = append(out.ByType, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var biggest models.BiggestSpend
	err = s.db.QueryRow(ctx, `
		SELECT coalesce(nullif(counterparty_name, ''), description), amount_minor, occurred_at
		  FROM wallet_transactions
		 WHERE wallet_id = $1 AND direction = 'out' AND occurred_at >= $2
		 ORDER BY amount_minor DESC LIMIT 1`, walletID, monthStart).
		Scan(&biggest.Description, &biggest.AmountMinor, &biggest.OccurredAt)
	if err == nil {
		out.Biggest = &biggest
	}

	crows, err := s.db.Query(ctx, `
		SELECT sp.id,
		       coalesce(nullif(sp.narrative, ''), coalesce(b.nickname, 'Scheduled payment')),
		       sp.amount_minor, sp.next_run_at, sp.frequency
		  FROM scheduled_payments sp
		  LEFT JOIN beneficiaries b ON b.id = sp.beneficiary_id
		 WHERE sp.source_wallet_id = $1 AND sp.status = 'active'
		   AND sp.next_run_at IS NOT NULL AND sp.next_run_at < $2
		 ORDER BY sp.next_run_at`, walletID, nextMonthStart)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	for crows.Next() {
		var c models.Commitment
		if err := crows.Scan(&c.ID, &c.Name, &c.AmountMinor, &c.DueAt, &c.Frequency); err != nil {
			return nil, err
		}
		out.Commitments = append(out.Commitments, c)
		out.CommittedMinor += c.AmountMinor
	}
	if err := crows.Err(); err != nil {
		return nil, err
	}

	out.SafeToSpendMinor = out.AvailableMinor - out.CommittedMinor
	if out.SafeToSpendMinor < 0 {
		out.SafeToSpendMinor = 0
	}
	return out, nil
}
