package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"nabla/transfers-svc/internal/models"
	"nabla/transfers-svc/internal/platform/clock"
	"nabla/transfers-svc/internal/platform/outbox"
	"nabla/transfers-svc/internal/providers"
)

var (
	ErrNoWallet        = errors.New("funding: customer has no wallet")
	ErrUnknownAccount  = errors.New("funding: no funding account with that number")
	ErrNotSettled      = errors.New("funding: provider has not settled this collection")
	ErrAlreadyRecorded = errors.New("funding: collection already recorded")
	// ErrCollectionsNotConfigured: no money-in rail is wired into this
	// deployment, so there is nothing to provision an account against.
	ErrCollectionsNotConfigured = errors.New("funding: no collection provider is configured")
)

// FundingService owns the inbound rail.
type FundingService struct {
	db       DB
	provider providers.CollectionProvider
	ledger   *LedgerService
	wallets  *WalletService
	limits   *LimitsService
	clock    clock.Clock
	identity IdentityClient
}

func NewFundingService(db DB, p providers.CollectionProvider, ledger *LedgerService,
	wallets *WalletService, limits *LimitsService, clk clock.Clock) *FundingService {
	return &FundingService{
		db: db, provider: p, ledger: ledger, wallets: wallets,
		limits: limits, clock: clk,
	}
}

// WithIdentity supplies the client the cardholder name on the account is read
// from. Optional: without it the account is named from the customer id, which
// is ugly but not wrong, and money still arrives.
func (s *FundingService) WithIdentity(c IdentityClient) { s.identity = c }

func (s *FundingService) providerName() string {
	if s.provider == nil {
		return ""
	}
	return s.provider.Info().Name
}

// FundingAccount is the customer's own deposit number, as the app shows it.
type FundingAccount struct {
	AccountNumber string `json:"account_number"`
	AccountName   string `json:"account_name"`
	BankName      string `json:"bank_name"`
	BankCode      string `json:"bank_code,omitempty"`
	Currency      string `json:"currency"`
	Permanent     bool   `json:"permanent"`

	Live bool `json:"live"`
}

func (s *FundingService) EnsureAccount(ctx context.Context, customerID uuid.UUID) (*FundingAccount, error) {
	if s.provider == nil {
		return nil, ErrCollectionsNotConfigured
	}
	if acct, err := s.accountOf(ctx, customerID); err == nil {
		return acct, nil
	} else if !errors.Is(err, ErrUnknownAccount) {
		return nil, err
	}

	var walletID uuid.UUID
	var currency string
	err := s.db.QueryRow(ctx, `
		SELECT id, currency FROM wallets
		 WHERE user_id = $1 AND status = 'active'
		 ORDER BY is_default DESC, created_at
		 LIMIT 1`, customerID).Scan(&walletID, &currency)
	if err != nil {
		return nil, ErrNoWallet
	}

	name, first, last, email := s.cardholderName(ctx, customerID)

	va, err := s.provider.CreateVirtualAccount(ctx, providers.VirtualAccountRequest{
		Reference:   customerID.String(),
		AccountName: name,
		FirstName:   first,
		LastName:    last,
		Email:       email,
		Currency:    currency,
	})
	if err != nil {
		// The vendor's own words, or nobody can act on this. A funding account
		// that cannot be issued is a customer who cannot put money in, and
		// "provider rejected the request" is not a diagnosis.
		if pe, ok := providers.AsError(err); ok {
			log.Printf("funding: could not issue an account for %s via %s: code=%s http=%d body=%s",
				customerID, s.providerName(), pe.Code, pe.HTTPStatus, string(pe.Raw))
		} else {
			log.Printf("funding: could not issue an account for %s via %s: %v",
				customerID, s.providerName(), err)
		}
		return nil, err
	}

	if _, err := s.db.Exec(ctx, `
		INSERT INTO funding_accounts
			(customer_id, wallet_id, provider_name, provider_ref, account_number,
			 account_name, bank_name, bank_code, currency, is_permanent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (customer_id, currency, provider_name) DO NOTHING`,
		customerID, walletID, s.providerName(), va.ProviderRef,
		va.AccountNumber, va.AccountName, va.BankName, va.BankCode,
		currency, va.Permanent); err != nil {
		return nil, err
	}
	return s.accountOf(ctx, customerID)
}

// FundingAccountOf reads the account without provisioning one.
func (s *FundingService) FundingAccountOf(ctx context.Context, customerID uuid.UUID) (*FundingAccount, error) {
	return s.accountOf(ctx, customerID)
}

func (s *FundingService) SimulateDeposit(ctx context.Context, customerID uuid.UUID, amountMinor int64) (int64, error) {
	if s.provider == nil {
		return 0, ErrCollectionsNotConfigured
	}
	if amountMinor <= 0 {
		return 0, fmt.Errorf("funding: simulated deposit amount must be positive")
	}
	sim, ok := s.provider.(providers.CollectionSimulator)
	if !ok {
		return 0, fmt.Errorf("funding: the configured rail cannot simulate deposits (set MOCK_COLLECTIONS=true)")
	}
	acct, err := s.EnsureAccount(ctx, customerID)
	if err != nil {
		return 0, err
	}
	ref, err := sim.SimulateDeposit(ctx, acct.AccountNumber, amountMinor, "Simulated GTB deposit", "simulated bank transfer")
	if err != nil {
		return 0, err
	}
	if err := s.Record(ctx, ref); err != nil && !errors.Is(err, ErrAlreadyRecorded) {
		return 0, err
	}
	var balance int64
	err = s.db.QueryRow(ctx, `
		SELECT la.balance_minor
		  FROM funding_accounts f
		  JOIN wallets w ON w.id = f.wallet_id
		  JOIN ledger_accounts la ON la.id = w.ledger_account_id
		 WHERE f.customer_id = $1 AND f.provider_name = $2
		 ORDER BY f.created_at LIMIT 1`,
		customerID, s.providerName()).Scan(&balance)
	if err != nil {
		return 0, err
	}
	return balance, nil
}

func (s *FundingService) accountOf(ctx context.Context, customerID uuid.UUID) (*FundingAccount, error) {
	var a FundingAccount
	// Scoped to the active provider: an account issued by a rail we no longer
	// use cannot receive anything, so showing it would be a lie.
	err := s.db.QueryRow(ctx, `
		SELECT account_number, account_name, bank_name, bank_code, currency, is_permanent
		  FROM funding_accounts
		 WHERE customer_id = $1 AND provider_name = $2
		 ORDER BY created_at LIMIT 1`, customerID, s.providerName()).
		Scan(&a.AccountNumber, &a.AccountName, &a.BankName, &a.BankCode,
			&a.Currency, &a.Permanent)
	if err != nil {
		return nil, ErrUnknownAccount
	}
	sandbox := false
	if s.provider != nil {
		sandbox = s.provider.Info().Sandbox
	}
	a.Live = isLiveIssuer(sandbox, a.BankName)
	return &a, nil
}

func isLiveIssuer(providerSandbox bool, bankName string) bool {
	if providerSandbox {
		return false
	}
	name := strings.ToLower(bankName)
	for _, marker := range []string{"test", "mock", "sandbox", "demo"} {
		if strings.Contains(name, marker) {
			return false
		}
	}
	return true
}

func (s *FundingService) cardholderName(ctx context.Context, customerID uuid.UUID) (name, first, last, email string) {
	if s.identity == nil {
		return customerID.String(), "", "", ""
	}
	if kyc, err := s.identity.GetKYCProfile(ctx, customerID); err == nil {
		first, last = strings.TrimSpace(kyc.FirstName), strings.TrimSpace(kyc.LastName)
	}
	if user, err := s.identity.GetUser(ctx, customerID); err == nil {
		email = user.Email
	}
	name = strings.TrimSpace(first + " " + last)
	if name == "" {
		name = customerID.String()
	}
	return name, first, last, email
}

type NablrHandle struct {
	NablrAccountNumber string `json:"nablr_account_number"`
	NablrUsername      string `json:"nablr_username"`
}

func (s *FundingService) HandleOf(ctx context.Context, customerID uuid.UUID) (*NablrHandle, error) {
	if s.identity == nil {
		return nil, ErrIdentityUnavailable
	}
	user, err := s.identity.GetUser(ctx, customerID)
	if err != nil {
		return nil, err
	}
	return &NablrHandle{
		NablrAccountNumber: user.AccountNumber,
		NablrUsername:      user.NablrUsername,
	}, nil
}

func (s *FundingService) Record(ctx context.Context, providerRef string) error {
	if s.provider == nil {
		return ErrCollectionsNotConfigured
	}
	if strings.TrimSpace(providerRef) == "" {
		return fmt.Errorf("funding: provider reference required")
	}
	provider := s.providerName()

	// Cheap early exit for a repeated delivery; the unique index on
	// (provider_name, provider_ref) is the real guarantee.
	var exists bool
	_ = s.db.QueryRow(ctx, `
		SELECT true FROM collections WHERE provider_name = $1 AND provider_ref = $2`,
		provider, providerRef).Scan(&exists)
	if exists {
		return ErrAlreadyRecorded
	}

	col, err := s.provider.GetCollection(ctx, providerRef)
	if err != nil {
		return err
	}
	if col.Status != providers.CollectionSettled {
		// Not money yet. Say so rather than crediting optimistically.
		return ErrNotSettled
	}
	if col.AmountMinor <= 0 {
		return fmt.Errorf("funding: collection %s has no positive amount", providerRef)
	}

	var fundingID, customerID, walletID, ledgerAccountID uuid.UUID
	var currency string
	err = s.db.QueryRow(ctx, `
		SELECT f.id, f.customer_id, f.wallet_id, w.ledger_account_id, f.currency
		  FROM funding_accounts f
		  JOIN wallets w ON w.id = f.wallet_id
		 WHERE f.provider_name = $1 AND f.account_number = $2`,
		provider, col.AccountNumber).
		Scan(&fundingID, &customerID, &walletID, &ledgerAccountID, &currency)
	if err != nil {
		return ErrUnknownAccount
	}
	if col.Currency != "" && col.Currency != currency {
		return fmt.Errorf("funding: collection %s is in %s but the account is %s",
			providerRef, col.Currency, currency)
	}

	occurredAt := col.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = s.clock.Now()
	}

	description := collectionDescription(col)

	return s.inTx(ctx, func(tx pgx.Tx) error {

		clearing, err := s.ledger.OpenAccountInTx(ctx, tx, OpenAccountInput{
			Type: models.AccountProviderClearing, Currency: currency,
			OwnerType: "provider", ProviderName: provider,
			Name: provider + " collections clearing " + currency,
			Code: fmt.Sprintf("provider_clearing:collections:%s:%s", currency, provider),
		})
		if err != nil {
			return err
		}

		txn, err := s.ledger.PostTransaction(ctx, tx, PostTransactionInput{
			Reference:   "collection:" + provider + ":" + providerRef,
			Kind:        models.KindDeposit,
			Description: description,
			Entries: []EntryInput{
				{AccountID: clearing.ID, Direction: models.Debit, AmountMinor: col.AmountMinor, Currency: currency},
				{AccountID: ledgerAccountID, Direction: models.Credit, AmountMinor: col.AmountMinor, Currency: currency},
			},
		})
		if err != nil {
			return err
		}

		balanceAfter := balanceAfterFor(txn, ledgerAccountID)

		if _, err := s.wallets.RecordFeedItem(ctx, tx, FeedInput{
			WalletID:            walletID,
			CustomerID:          customerID,
			LedgerTransactionID: txn.ID,
			Direction:           "in",
			AmountMinor:         col.AmountMinor,
			Currency:            currency,
			FeeMinor:            col.FeeMinor,
			BalanceAfterMinor:   balanceAfter,
			TransactionType:     "deposit",
			CounterpartyName:    col.SenderName,

			CounterpartyType: "provider",
			Description:      description,
			SourceType:       "collection",
			SourceID:         fundingID,
			OccurredAt:       occurredAt,
		}); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE wallets SET available_minor = available_minor + $2
			 WHERE id = $1`, walletID, col.AmountMinor); err != nil {
			return err
		}

		if s.limits != nil {
			if err := s.limits.FlagIfOverBalanceCap(ctx, tx, customerID, currency, balanceAfter); err != nil {
				return err
			}
		}

		if err := outbox.Enqueue(ctx, tx, "collection", fundingID, "notification.money_in",
			map[string]string{
				"customer_id":  customerID.String(),
				"amount_minor": fmt.Sprintf("%d", col.AmountMinor),
				"currency":     currency,
				"from":         col.SenderName,
			}, ""); err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO collections
				(funding_account_id, customer_id, wallet_id, provider_name, provider_ref,
				 amount_minor, fee_minor, currency, sender_name, narrative,
				 ledger_transaction_id, occurred_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (provider_name, provider_ref) DO NOTHING`,
			fundingID, customerID, walletID, provider, providerRef,
			col.AmountMinor, col.FeeMinor, currency, col.SenderName, col.Narrative,
			txn.ID, occurredAt)
		return err
	})
}

func collectionDescription(col *providers.Collection) string {
	if col.SenderName != "" {
		return "Transfer from " + col.SenderName
	}
	if col.Narrative != "" {
		return col.Narrative
	}
	return "Money in"
}

func CollectionCallbackOutcome(outcomeErr error) (status, note string) {
	switch {
	case outcomeErr == nil, errors.Is(outcomeErr, ErrAlreadyRecorded):
		return "processed", ""
	case errors.Is(outcomeErr, ErrNotSettled):
		return "received", "the provider has not settled this yet"
	case errors.Is(outcomeErr, ErrUnknownAccount):
		return "failed", "no funding account matched this payment"
	default:
		return "failed", outcomeErr.Error()
	}
}

func (s *FundingService) RecordCollectionCallback(ctx context.Context, ref string, payload []byte, outcomeErr error) {
	status, note := CollectionCallbackOutcome(outcomeErr)
	if err := s.persistCollectionCallback(ctx, ref, payload, status, note); err != nil {
		log.Printf("funding: could not persist callback %s: %v", ref, err)
	}
}

func (s *FundingService) persistCollectionCallback(ctx context.Context, ref string, payload []byte, status, note string) error {
	if strings.TrimSpace(ref) == "" {
		return nil
	}
	provider := s.providerName()
	sum := sha256.Sum256(payload)
	// A 'processed' row must never be downgraded by a redelivery that (say)
	// slipped in before the first one committed; anything else is upgraded so a
	// later, settled redelivery clears a stuck 'received' row.
	_, err := s.db.Exec(ctx, `
		INSERT INTO provider_webhooks
			(provider_name,event_id,event_type,raw_body_sha256,payload,signature_valid,status,error_message,processing_attempts)
		VALUES ($1,$2,'collection',$3,$4,false,$5,$6,1)
		ON CONFLICT(provider_name,event_id) DO UPDATE SET
			payload = EXCLUDED.payload,
			status  = CASE WHEN provider_webhooks.status = 'processed'
			              THEN provider_webhooks.status ELSE EXCLUDED.status END,
			error_message = EXCLUDED.error_message,
			processing_attempts = provider_webhooks.processing_attempts + 1`,
		provider, ref, hex.EncodeToString(sum[:]), string(payload), status, note)
	return err
}

// CollectionCheckResult is the answer to "I have paid — look for it".
type CollectionCheckResult struct {
	Applied int    `json:"applied"`
	Checked int    `json:"checked"`
	Message string `json:"message"`
}

// ReconcilePendingCollections retries collection callbacks that were received
// before the provider had settled them or that previously failed processing.
// Record is idempotent, so the same provider reference can be checked safely.
func (s *FundingService) ReconcilePendingCollections(ctx context.Context, limit int) (checked, applied int, err error) {
	if s.provider == nil {
		return 0, 0, ErrCollectionsNotConfigured
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, event_id FROM provider_webhooks
		 WHERE provider_name = $1 AND event_type = 'collection'
		   AND status IN ('received','failed','ignored')
		   AND created_at > now() - interval '7 days'
		 ORDER BY created_at LIMIT $2`, s.providerName(), limit)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	type pending struct {
		id  uuid.UUID
		ref string
	}
	var items []pending
	for rows.Next() {
		var item pending
		if err := rows.Scan(&item.id, &item.ref); err != nil {
			return checked, applied, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return checked, applied, err
	}
	for _, item := range items {
		checked++
		status, note, didApply := s.applyCollection(ctx, item.ref)
		if didApply {
			applied++
		}
		if _, err := s.db.Exec(ctx, `
			UPDATE provider_webhooks
			   SET status = $1, error_message = NULLIF($2, ''), processed_at = now(),
			       processing_attempts = processing_attempts + 1
			 WHERE id = $3`, status, note, item.id); err != nil {
			return checked, applied, err
		}
	}
	return checked, applied, nil
}

func (s *FundingService) CheckForPendingCollections(ctx context.Context, customerID uuid.UUID) (*CollectionCheckResult, error) {
	if s.provider == nil {
		return nil, ErrCollectionsNotConfigured
	}
	provider := s.providerName()

	rows, err := s.db.Query(ctx, `
		SELECT id, event_id FROM provider_webhooks
		 WHERE provider_name = $1 AND event_type = 'collection'
		   AND status IN ('received','failed','ignored')
		   AND created_at > now() - interval '7 days'
		 ORDER BY created_at DESC LIMIT 50`, provider)
	if err != nil {
		return nil, err
	}
	type pending struct {
		id  uuid.UUID
		ref string
	}
	var ids []pending
	for rows.Next() {
		var p pending
		if rows.Scan(&p.id, &p.ref) == nil {
			ids = append(ids, p)
		}
	}
	rows.Close()

	before := s.customerBalance(ctx, customerID)
	result := &CollectionCheckResult{}
	for _, item := range ids {
		result.Checked++
		status, note, _ := s.applyCollection(ctx, item.ref)
		if _, err := s.db.Exec(ctx, `
			UPDATE provider_webhooks
			   SET status = $1, error_message = NULLIF($2, ''), processed_at = now(),
			       processing_attempts = processing_attempts + 1
			 WHERE id = $3`, status, note, item.id); err != nil {
			log.Printf("funding: check could not update callback %s: %v", item.ref, err)
		}
	}
	if s.customerBalance(ctx, customerID) > before {
		result.Applied = 1
	}
	result.Message = collectionCheckMessage(result.Applied, result.Checked)
	return result, nil
}

func collectionCheckMessage(applied, checked int) string {
	switch {
	case applied > 0:
		return "Found it. Your balance has been updated."
	case checked > 0:
		return "We found a payment but the bank has not released it yet. " +
			"We will keep checking and credit it the moment it settles."
	default:
		return "Nothing has arrived yet. Bank transfers usually land within " +
			"minutes — if it has been longer, keep your bank's receipt and contact support."
	}
}

// applyCollection is the single place that turns a reference into money, so
// the webhook, the persistence verdict and both replay paths cannot drift apart
// in how they judge an outcome.
func (s *FundingService) applyCollection(ctx context.Context, ref string) (status, note string, applied bool) {
	if s.provider == nil {
		return "failed", "collections are not configured on this instance", false
	}
	err := s.Record(ctx, ref)
	status, note = CollectionCallbackOutcome(err)
	return status, note, err == nil
}

// customerBalance is the sum of this customer's ledger balances, the before/after
// probe for the "did MY balance move" answer.
func (s *FundingService) customerBalance(ctx context.Context, customerID uuid.UUID) int64 {
	var minor int64
	_ = s.db.QueryRow(ctx, `
		SELECT coalesce(sum(la.balance_minor), 0)
		  FROM wallets w JOIN ledger_accounts la ON la.id = w.ledger_account_id
		 WHERE w.user_id = $1`, customerID).Scan(&minor)
	return minor
}

// inTx runs fn inside one database transaction, rolling back on any error.
func (s *FundingService) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
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
