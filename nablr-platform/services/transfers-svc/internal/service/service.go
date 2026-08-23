package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	db "nabla/transfers-svc/db/sqlc"
	"nabla/transfers-svc/internal/metrics"
	"nabla/transfers-svc/internal/models"
	"nabla/transfers-svc/internal/platform/crypto"
	"nabla/transfers-svc/internal/providers"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInsufficientFunds    = models.ErrInsufficientFunds
	ErrLimitExceeded        = models.ErrLimitExceeded
	ErrLimitsMissing        = models.ErrTierLimitsNotSet
	ErrBeneficiaryDuplicate = models.ErrBeneficiaryDuplicate
	ErrBalanceCap           = errors.New("recipient balance cap exceeded")
	ErrHoldAlreadyResolved  = errors.New("transfers: hold already captured or released")
)

func releaseReservation(ctx context.Context, q *db.Queries, holdID, walletID uuid.UUID, amountMinor int64) error {
	n, err := q.ReleaseHold(ctx, holdID)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrHoldAlreadyResolved
	}
	return q.ReleaseWalletReservation(ctx, db.ReleaseWalletReservationParams{WalletID: walletID, AmountMinor: amountMinor})
}

func captureReservation(ctx context.Context, q *db.Queries, holdID, walletID uuid.UUID, amountMinor int64) error {
	n, err := q.CaptureHold(ctx, holdID)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrHoldAlreadyResolved
	}
	return q.ConsumeWalletReservation(ctx, db.ConsumeWalletReservationParams{WalletID: walletID, AmountMinor: amountMinor})
}

func completeTransfer(ctx context.Context, q *db.Queries, id uuid.UUID) error {
	n, err := q.CompleteTransfer(ctx, id)
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: transfer %s is no longer in flight", models.ErrInvalidTransition, id)
	}
	metrics.TransferOutcomes.WithLabelValues("settled").Inc()
	return nil
}

func failTransfer(ctx context.Context, q *db.Queries, id uuid.UUID, code, reason string) error {
	n, err := q.FailTransfer(ctx, db.FailTransferParams{
		ID:            id,
		FailureCode:   pgtype.Text{String: code, Valid: strings.TrimSpace(code) != ""},
		FailureReason: pgtype.Text{String: reason, Valid: strings.TrimSpace(reason) != ""},
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: transfer %s is no longer in flight", models.ErrInvalidTransition, id)
	}
	metrics.TransferOutcomes.WithLabelValues("failed").Inc()
	return nil
}

var clearingUserID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

const newPayeeCoolingPeriod = 24 * time.Hour

type Service struct {
	pool     *pgxpool.Pool
	q        *db.Queries
	payout   providers.PayoutProvider
	enc      *crypto.Encryptor
	blind    *crypto.BlindIndex
	identity IdentityClient
	notifier Notifier
	breaker  *providers.Breaker
}

func New(pool *pgxpool.Pool) *Service { return NewWithProvider(pool, providers.NewMockProvider()) }

func NewWithProvider(pool *pgxpool.Pool, payout providers.PayoutProvider) *Service {
	return &Service{pool: pool, q: db.New(pool), payout: payout, enc: ephemeralEncryptor(), blind: ephemeralBlindIndex(), breaker: providers.NewBreaker(5, 30*time.Second)}
}

// NewWithProviderAndCrypto wires a real rail and the field-level encryptor.
// A nil encryptor is never accepted silently: development boots get an
// ephemeral key rather than letting a nil pointer panic at dispatch time.
func NewWithProviderAndCrypto(pool *pgxpool.Pool, payout providers.PayoutProvider, enc *crypto.Encryptor, blind *crypto.BlindIndex) *Service {
	if enc == nil {
		enc = ephemeralEncryptor()
	}
	if blind == nil {
		blind = ephemeralBlindIndex()
	}
	return &Service{pool: pool, q: db.New(pool), payout: payout, enc: enc, blind: blind, breaker: providers.NewBreaker(5, 30*time.Second)}
}

func (s *Service) WithClients(identity IdentityClient, notifier Notifier) *Service {
	s.identity = identity
	s.notifier = notifier
	return s
}

// ephemeralEncryptor / ephemeralBlindIndex keep constructors that predate encryption working with throwaway dev keys. Production must pass real keys.
func ephemeralEncryptor() *crypto.Encryptor {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		log.Fatalf("crypto: cannot generate ephemeral key: %v", err)
	}
	enc, err := crypto.NewEncryptor([]crypto.KeyMaterial{{Version: 1, Key: key[:]}}, 1)
	if err != nil {
		log.Fatalf("crypto: cannot build ephemeral encryptor: %v", err)
	}
	return enc
}
func ephemeralBlindIndex() *crypto.BlindIndex {
	var pepper [32]byte
	if _, err := rand.Read(pepper[:]); err != nil {
		log.Fatalf("crypto: cannot generate ephemeral pepper: %v", err)
	}
	bi, err := crypto.NewBlindIndex(pepper[:])
	if err != nil {
		log.Fatalf("crypto: cannot build ephemeral blind index: %v", err)
	}
	return bi
}
func currency(v string) (string, error) {
	v = strings.ToUpper(strings.TrimSpace(v))
	if len(v) != 3 {
		return "", errors.New("currency must be ISO-4217")
	}
	return v, nil
}
func (s *Service) CreateWallet(ctx context.Context, u uuid.UUID, c string) (db.Wallet, error) {
	c, e := currency(c)
	if e != nil {
		return db.Wallet{}, e
	}
	return s.q.CreateWallet(ctx, db.CreateWalletParams{UserID: u, Currency: c})
}
func (s *Service) Wallet(ctx context.Context, u uuid.UUID, c string) (db.Wallet, error) {
	c, e := currency(c)
	if e != nil {
		return db.Wallet{}, e
	}
	return s.q.WalletByUser(ctx, db.WalletByUserParams{UserID: u, Currency: c})
}

type BeneficiaryInput struct {
	Type                                           string
	Nickname                                       string
	RecipientUserID                                *uuid.UUID
	RecipientReference                             string
	BankCode, AccountNumber, AccountName, Currency string

	// Save controls whether the payee is kept in the customer's saved list.
	// nil or true saves it (the back-compatible default); false creates an
	// ephemeral pay-once payee — still verified and still subject to the
	// new-payee cooling cap, but hidden from the saved list (ListBeneficiaries
	// filters is_saved). This backs the design's "resolve → pay" flow that does
	// not ask the customer to keep the recipient.
	Save *bool
}

func (s *Service) resolveInternalPayee(ctx context.Context, ref string) (*uuid.UUID, UserProfile, error) {
	ref = strings.TrimSpace(ref)
	if id, err := uuid.Parse(ref); err == nil {

		if s.identity == nil {
			log.Printf("transfer recipient resolution failed step=resolve_internal_payee reason=identity_client_missing ref_type=uuid")
			return nil, UserProfile{}, models.ErrAuthorizationUnavailable
		}
		prof, gerr := s.identity.GetUser(ctx, id)
		if gerr != nil {
			log.Printf("transfer recipient resolution failed step=get_recipient_user ref_type=uuid recipient_user_id=%s error=%v", id, gerr)
			switch {
			case errors.Is(gerr, ErrIdentityUnavailable):
				return nil, UserProfile{}, errors.Join(models.ErrAuthorizationUnavailable, gerr)
			case errors.Is(gerr, ErrIdentityUserNotFound):
				return nil, UserProfile{}, models.ErrBeneficiaryNotFound
			default:
				return nil, UserProfile{}, gerr
			}
		}
		return &id, prof, nil
	}
	if s.identity == nil {
		log.Printf("transfer recipient resolution failed step=resolve_internal_payee reason=identity_client_missing ref_type=handle_or_number")
		return nil, UserProfile{}, models.ErrAuthorizationUnavailable
	}
	digits := toNUBAN(onlyDigits.ReplaceAllString(ref, ""))
	handle := strings.TrimPrefix(ref, "@")
	var u UserProfile
	var err error
	switch {
	case len(digits) == 10:
		u, err = s.identity.GetUserByAccountNumber(ctx, digits)
	case len(digits) > 0:
		// A number that is not a Nablr handle is not this customer's identity.
		return nil, UserProfile{}, models.ErrBeneficiaryNotFound
	case handle != "":
		u, err = s.identity.GetUserByUsername(ctx, handle)
	default:
		return nil, UserProfile{}, models.ErrBeneficiaryNotFound
	}
	if err != nil {
		log.Printf("transfer recipient resolution failed step=resolve_internal_payee ref=%q error=%v", ref, err)
		if errors.Is(err, ErrIdentityUserNotFound) {
			return nil, UserProfile{}, models.ErrBeneficiaryNotFound
		}
		if errors.Is(err, ErrIdentityUnavailable) {
			return nil, UserProfile{}, errors.Join(models.ErrAuthorizationUnavailable, err)
		}
		return nil, UserProfile{}, err
	}
	id, err := uuid.Parse(u.UserID)
	if err != nil || id == uuid.Nil {
		return nil, UserProfile{}, models.ErrBeneficiaryNotFound
	}
	return &id, u, nil
}

func (s *Service) AddBeneficiary(ctx context.Context, u uuid.UUID, in BeneficiaryInput) (db.Beneficiary, error) {
	c, e := currency(in.Currency)
	if e != nil {
		return db.Beneficiary{}, e
	}
	if in.Type != "internal" && in.Type != "bank" {
		return db.Beneficiary{}, errors.New("beneficiary type must be internal or bank")
	}
	recipientID := in.RecipientUserID
	var rprofile UserProfile
	if in.Type == "internal" && recipientID == nil && strings.TrimSpace(in.RecipientReference) != "" {
		id, prof, err := s.resolveInternalPayee(ctx, in.RecipientReference)
		if err != nil {
			return db.Beneficiary{}, err
		}
		recipientID, rprofile = id, prof
	}
	if in.Type == "internal" && recipientID != nil &&
		rprofile.AccountNumber == "" && rprofile.NablrUsername == "" && s.identity != nil {
		if prof, err := s.identity.GetUser(ctx, *recipientID); err == nil {
			rprofile = prof
		}
	}
	benID := uuid.New()
	cooling := time.Now().Add(newPayeeCoolingPeriod)
	// A nil Save defaults to saved, preserving the prior contract; an explicit
	// false creates an ephemeral pay-once payee that ListBeneficiaries hides.
	isSaved := in.Save == nil || *in.Save
	p := db.CreateBeneficiaryParams{ID: benID, UserID: u, Type: in.Type, AccountName: in.AccountName,
		Currency: c, VerificationStatus: "unverified", IsSaved: isSaved,
		CoolingPeriodEndsAt: pgtype.Timestamptz{Time: cooling, Valid: true}}
	if in.Nickname != "" {
		p.Nickname = pgtype.Text{String: in.Nickname, Valid: true}
	}
	if recipientID != nil {
		if *recipientID == u {
			return db.Beneficiary{}, models.ErrSelfBeneficiary
		}
		p.RecipientUserID = pgtype.UUID{Bytes: *recipientID, Valid: true}
	}
	if in.Type == "internal" && !p.RecipientUserID.Valid {
		return db.Beneficiary{}, errors.New("recipient user id is required")
	}
	if in.Type == "internal" {
		if rprofile.AccountNumber != "" {
			p.RecipientAccountNumber = pgtype.Text{String: rprofile.AccountNumber, Valid: true}
		}
		if rprofile.NablrUsername != "" {
			p.RecipientUsername = pgtype.Text{String: rprofile.NablrUsername, Valid: true}
		}
		if rprofile.AvatarURL != "" {
			p.RecipientAvatarUrl = pgtype.Text{String: rprofile.AvatarURL, Valid: true}
		}
	}
	if in.Type == "bank" {
		if in.BankCode == "" || len(in.AccountNumber) < 6 {
			return db.Beneficiary{}, errors.New("bank code and a 6+ digit account number are required")
		}
		verification := "unverified"
		verifiedName := ""
		if s.payout != nil {
			if providerCode, cerr := s.providerBankCode(ctx, in.BankCode); cerr != nil {
				return db.Beneficiary{}, cerr
			} else if v, verr := s.payout.ValidateAccount(ctx, "NG", providerCode, in.AccountNumber); verr == nil {
				verifiedName = v.AccountName
				switch v.MatchResult {
				case "match":
					verification = "verified"
				case "partial_match":
					verification = "partial_match"
				case "no_match":
					verification = "name_mismatch"
				default:
					verification = "not_supported"
				}
			}
		}
		ciphertext, keyVersion, err := s.enc.Encrypt(
			[]byte(in.AccountNumber),
			crypto.AAD("beneficiaries", "account_number", benID.String()))
		if err != nil {
			return db.Beneficiary{}, err
		}
		p.BankCode = pgtype.Text{String: in.BankCode, Valid: true}
		p.AccountNumberCiphertext = ciphertext
		p.AccountNumberLast4 = pgtype.Text{String: in.AccountNumber[len(in.AccountNumber)-4:], Valid: true}
		p.AccountNumberBlindIndex = s.blind.Compute("beneficiary_account", in.AccountNumber+":"+in.BankCode)
		p.KeyVersion = pgtype.Int2{Int16: int16(keyVersion), Valid: true}
		p.VerificationStatus = verification
		p.VerifiedName = pgtype.Text{String: verifiedName, Valid: verifiedName != ""}
		if s.payout != nil {
			p.VerificationProvider = pgtype.Text{String: s.payout.Info().Name, Valid: true}
		}
	}
	b, err := s.q.CreateBeneficiary(ctx, p)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && isDuplicateKey(pgErr) {
			if in.Type == "internal" && p.RecipientUserID.Valid {
				existing, e := s.q.InternalBeneficiaryForRecipient(ctx, db.InternalBeneficiaryForRecipientParams{
					UserID: u, RecipientUserID: p.RecipientUserID,
				})
				if e == nil {
					return existing, nil
				}
			}
			return db.Beneficiary{}, ErrBeneficiaryDuplicate
		}
		return db.Beneficiary{}, err
	}
	return b, nil
}

func isDuplicateKey(e *pgconn.PgError) bool { return e.Code == "23505" }
func (s *Service) Beneficiaries(ctx context.Context, u uuid.UUID) ([]db.Beneficiary, error) {
	return s.q.ListBeneficiaries(ctx, u)
}

type TransferInput struct {
	BeneficiaryID uuid.UUID
	// QuoteID is retained for the (now-commented) quote path and for the worker,
	// but the customer send flow no longer prices a quote — see Transfer.
	QuoteID                  uuid.UUID
	AmountMinor              int64
	Currency, IdempotencyKey string

	// Inline pay-once recipient: when BeneficiaryID is nil the send named its
	// recipient directly (the Figma "resolve → pay" flow that does not ask the
	// customer to save the payee). RecipientType is "internal" or "bank".
	// RecipientReference is an @handle / Nablr number / user id for internal;
	// BankCode+AccountNumber(+AccountName) address a bank payee. Transfer
	// materialises these into an ephemeral (Save=false) beneficiary.
	RecipientType      string
	RecipientReference string
	BankCode           string
	AccountNumber      string
	AccountName        string

	PIN string

	PINAuthorizationID uuid.UUID
	// RecipientDescriptor is the stable string the token is bound to, computed by
	// the handler identically for the authorize-pin and transfer requests so a
	// token minted for one recipient cannot be spent paying another.
	RecipientDescriptor string

	PreAuthorized bool

	Narrative string
}

const pinAuthTTL = 5 * time.Minute

type AuthorizePINInput struct {
	PIN                 string
	AmountMinor         int64
	Currency            string
	RecipientDescriptor string
}

func (s *Service) AuthorizePIN(ctx context.Context, u uuid.UUID, in AuthorizePINInput) (db.TransferPinAuthorization, error) {
	if s.identity == nil {
		return db.TransferPinAuthorization{}, models.ErrAuthorizationUnavailable
	}
	if strings.TrimSpace(in.PIN) == "" {
		return db.TransferPinAuthorization{}, models.ErrPINRequired
	}
	if in.AmountMinor <= 0 {
		return db.TransferPinAuthorization{}, models.ErrZeroAmount
	}
	c, err := currency(in.Currency)
	if err != nil {
		return db.TransferPinAuthorization{}, err
	}
	ok, err := s.identity.VerifyPIN(ctx, u, in.PIN)
	if err != nil {
		// authzError folds an identity outage into ErrAuthorizationUnavailable (503);
		// a not-set / locked verdict passes through unchanged for the handler to word.
		return db.TransferPinAuthorization{}, authzError(err)
	}
	if !ok {
		return db.TransferPinAuthorization{}, models.ErrPINInvalid
	}
	descriptor := strings.TrimSpace(in.RecipientDescriptor)
	log.Printf("transfer pin authorization mint user_id=%s amount_minor=%d currency=%s recipient_descriptor=%q", u, in.AmountMinor, c, descriptor)
	return s.q.CreatePINAuthorization(ctx, db.CreatePINAuthorizationParams{
		UserID: u, AmountMinor: in.AmountMinor, Currency: c,
		RecipientDescriptor: descriptor,
		ExpiresAt:           time.Now().Add(pinAuthTTL),
	})
}

func (s *Service) materializeInlineBeneficiary(ctx context.Context, u uuid.UUID, in TransferInput) (uuid.UUID, error) {
	id, err := s.existingInlinePayee(ctx, u, in)
	switch {
	case err == nil:
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		// A real failure resolving the payee (e.g. identity unreachable) fails the
		// send; only "no such payee yet" falls through to minting one.
		log.Printf("transfer inline beneficiary failed step=existing_inline_payee user_id=%s recipient_type=%s recipient_reference=%q error=%v", u, in.RecipientType, in.RecipientReference, err)
		return uuid.Nil, err
	}

	save := false
	b, err := s.AddBeneficiary(ctx, u, BeneficiaryInput{
		Type: in.RecipientType, Currency: in.Currency, Save: &save,
		RecipientReference: in.RecipientReference,
		BankCode:           in.BankCode,
		AccountNumber:      in.AccountNumber,
		AccountName:        in.AccountName,
	})
	if err != nil {
		log.Printf("transfer inline beneficiary failed step=add_beneficiary user_id=%s recipient_type=%s recipient_reference=%q error=%v", u, in.RecipientType, in.RecipientReference, err)
		// A concurrent pay-once may have minted the row between the lookup and the
		// insert; fall back to the now-existing payee rather than failing the send.
		if errors.Is(err, ErrBeneficiaryDuplicate) {
			if again, ferr := s.existingInlinePayee(ctx, u, in); ferr == nil {
				return again, nil
			}
		}
		return uuid.Nil, err
	}
	return b.ID, nil
}

func (s *Service) existingInlinePayee(ctx context.Context, u uuid.UUID, in TransferInput) (uuid.UUID, error) {
	switch in.RecipientType {
	case "internal":
		id, _, err := s.resolveInternalPayee(ctx, in.RecipientReference)
		if err != nil {
			return uuid.Nil, err
		}
		if id == nil {
			return uuid.Nil, pgx.ErrNoRows
		}
		b, err := s.q.InternalBeneficiaryForRecipient(ctx, db.InternalBeneficiaryForRecipientParams{
			UserID: u, RecipientUserID: pgUUIDOf(*id),
		})
		if err != nil {
			return uuid.Nil, err
		}
		return b.ID, nil
	case "bank":
		if in.BankCode == "" || len(in.AccountNumber) < 6 {
			return uuid.Nil, pgx.ErrNoRows
		}
		b, err := s.q.BankBeneficiaryForAccount(ctx, db.BankBeneficiaryForAccountParams{
			UserID:                  u,
			AccountNumberBlindIndex: s.blind.Compute("beneficiary_account", in.AccountNumber+":"+in.BankCode),
			BankCode:                pgtype.Text{String: in.BankCode, Valid: true},
		})
		if err != nil {
			return uuid.Nil, err
		}
		return b.ID, nil
	}
	return uuid.Nil, pgx.ErrNoRows
}

func (s *Service) Quote(ctx context.Context, u uuid.UUID, in TransferInput) (db.TransferQuote, error) {
	if in.AmountMinor <= 0 {
		return db.TransferQuote{}, models.ErrZeroAmount
	}
	c, err := currency(in.Currency)
	if err != nil {
		return db.TransferQuote{}, err
	}
	ben, err := s.q.BeneficiaryByID(ctx, db.BeneficiaryByIDParams{ID: in.BeneficiaryID, UserID: u})
	if err != nil {
		return db.TransferQuote{}, notFound(models.ErrBeneficiaryNotFound, err)
	}
	if ben.Currency != c {
		return db.TransferQuote{}, models.ErrCurrencyMismatch
	}
	// Rejected at quote time so the customer is told before they confirm rather
	// than after. Transfer re-checks; this is the courtesy, that is the control.
	if err := checkNewPayeeAllowance(ben, in.AmountMinor, c, time.Now()); err != nil {
		return db.TransferQuote{}, err
	}
	w, err := s.q.WalletByUser(ctx, db.WalletByUserParams{UserID: u, Currency: c})
	if err != nil {
		return db.TransferQuote{}, notFound(models.ErrWalletNotFound, err)
	}
	return s.q.CreateQuote(ctx, db.CreateQuoteParams{UserID: u, SourceWalletID: w.ID, BeneficiaryID: ben.ID, Type: ben.Type, SendAmountMinor: in.AmountMinor, SendCurrency: c, ReceiveAmountMinor: in.AmountMinor, ReceiveCurrency: c, TotalDebitMinor: in.AmountMinor, ExpiresAt: time.Now().Add(2 * time.Minute)})
}

func (s *Service) Transfer(ctx context.Context, u uuid.UUID, in TransferInput) (db.Transfer, error) {
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return db.Transfer{}, errors.New("idempotency key is required")
	}
	if x, e := s.q.TransferByKey(ctx, db.TransferByKeyParams{SenderUserID: u, IdempotencyKey: in.IdempotencyKey}); e == nil {
		return x, nil
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return db.Transfer{}, e
	}
	// A PIN authorization token means the payer already proved their PIN on the
	// dedicated authorize-pin step, so the inline PIN check is skipped here — but
	// the token is still CONSUMED inside the transaction below (single-use, bound
	// to this exact amount / currency / recipient), which is the actual gate.
	hasToken := in.PINAuthorizationID != uuid.Nil
	if hasToken {
		auth, err := s.livePINAuthorization(ctx, in.PINAuthorizationID, u)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return db.Transfer{}, models.ErrPINAuthInvalid
			}
			return db.Transfer{}, err
		}
		in.AmountMinor = auth.AmountMinor
		in.Currency = auth.Currency
		in.RecipientDescriptor = auth.RecipientDescriptor
		in.applyRecipientDescriptor()
		in.PreAuthorized = true
	}

	auth, e := s.authorize(ctx, u, in)
	if e != nil {
		return db.Transfer{}, e
	}

	if in.BeneficiaryID == uuid.Nil {
		benID, err := s.materializeInlineBeneficiary(ctx, u, in)
		if err != nil {
			return db.Transfer{}, err
		}
		in.BeneficiaryID = benID
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return db.Transfer{}, e
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	// QUOTE PATH — PARKED (commented, not deleted). The send flow no longer prices
	// a quote: the Figma flow is resolve → amount → confirm → PIN → transfer, so the
	// amount/currency/recipient arrive directly on the request and are gated by the
	// single-use PIN authorization token consumed below (which recovers the
	// amount-lock the quote used to provide). The branch is retained verbatim so the
	// priced-corridor flow (FX, fees) can be restored without reconstructing it.
	//
	// // A quote is the authority for amount, currency, source wallet and payee.
	// // Direct inputs remain for worker-created schedule executions only.
	// if in.QuoteID != uuid.Nil {
	// 	quote, err := q.ConsumeQuote(ctx, db.ConsumeQuoteParams{ID: in.QuoteID, UserID: u})
	// 	if err != nil {
	// 		// ConsumeQuote matches on status='active' AND expires_at>now(), so a
	// 		// miss is a quote that was already used or has lapsed. Both mean the
	// 		// customer must re-price rather than send on stale terms.
	// 		return db.Transfer{}, models.ErrQuoteExpired
	// 	}
	// 	// The body may echo the quote's beneficiary_id (mobile carries it through
	// 	// the flow) or omit it entirely. What it must NOT do is name a DIFFERENT
	// 	// payee: since the line below overwrites the body with the quote's payee,
	// 	// a divergent id would otherwise pay the quote's beneficiary under the
	// 	// caller's mistaken belief they were paying someone else. Refuse it.
	// 	if in.BeneficiaryID != uuid.Nil && in.BeneficiaryID != quote.BeneficiaryID {
	// 		return db.Transfer{}, models.ErrBeneficiaryMismatch
	// 	}
	// 	in.BeneficiaryID, in.AmountMinor, in.Currency = quote.BeneficiaryID, quote.SendAmountMinor, quote.SendCurrency
	// }
	if in.AmountMinor <= 0 {
		return db.Transfer{}, models.ErrZeroAmount
	}
	c, e := currency(in.Currency)
	if e != nil {
		return db.Transfer{}, e
	}

	if hasToken {
		log.Printf("transfer pin authorization consume attempt token_id=%s user_id=%s amount_minor=%d currency=%s recipient_descriptor=%q", in.PINAuthorizationID, u, in.AmountMinor, c, in.RecipientDescriptor)
		if _, err := q.ConsumePINAuthorization(ctx, db.ConsumePINAuthorizationParams{
			ID:                  in.PINAuthorizationID,
			UserID:              u,
			AmountMinor:         in.AmountMinor,
			Currency:            c,
			RecipientDescriptor: in.RecipientDescriptor,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				log.Printf("transfer pin authorization consume failed token_id=%s user_id=%s amount_minor=%d currency=%s recipient_descriptor=%q error=%v", in.PINAuthorizationID, u, in.AmountMinor, c, in.RecipientDescriptor, err)
				s.logPINAuthorizationMismatch(ctx, in.PINAuthorizationID, u, in.AmountMinor, c, in.RecipientDescriptor)
				return db.Transfer{}, models.ErrPINAuthInvalid
			}
			return db.Transfer{}, err
		}
	}

	if e = materializeTierLimits(ctx, q, u, c, auth.tier); e != nil {
		return db.Transfer{}, e
	}
	l, e := q.LimitByUser(ctx, db.LimitByUserParams{UserID: u, Currency: c})
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return db.Transfer{}, ErrLimitsMissing
		}
		return db.Transfer{}, e
	}
	used, e := q.OutboundToday(ctx, db.OutboundTodayParams{SenderUserID: u, SendCurrency: c})
	if e != nil {
		return db.Transfer{}, e
	}
	if in.AmountMinor > l.PerTransactionMinor || used > l.DailyOutboundMinor-in.AmountMinor {
		return db.Transfer{}, ErrLimitExceeded
	}

	if l.WeeklyOutboundMinor > 0 {
		wk, e := q.OutboundThisWeek(ctx, db.OutboundThisWeekParams{SenderUserID: u, SendCurrency: c})
		if e != nil {
			return db.Transfer{}, e
		}
		if wk > l.WeeklyOutboundMinor-in.AmountMinor {
			return db.Transfer{}, ErrLimitExceeded
		}
	}
	if l.MonthlyOutboundMinor > 0 {
		mo, e := q.OutboundThisMonth(ctx, db.OutboundThisMonthParams{SenderUserID: u, SendCurrency: c})
		if e != nil {
			return db.Transfer{}, e
		}
		if mo > l.MonthlyOutboundMinor-in.AmountMinor {
			return db.Transfer{}, ErrLimitExceeded
		}
	}
	if l.DailyCountLimit > 0 {
		n, e := q.TransferCountToday(ctx, db.TransferCountTodayParams{SenderUserID: u, SendCurrency: c})
		if e != nil {
			return db.Transfer{}, e
		}
		if n+1 > int64(l.DailyCountLimit) {
			return db.Transfer{}, ErrLimitExceeded
		}
	}
	b, e := q.BeneficiaryByID(ctx, db.BeneficiaryByIDParams{ID: in.BeneficiaryID, UserID: u})
	if e != nil {
		log.Printf("transfer saved beneficiary lookup failed user_id=%s beneficiary_id=%s currency=%s error=%v", u, in.BeneficiaryID, c, e)
		return db.Transfer{}, notFound(models.ErrBeneficiaryNotFound, e)
	}
	if b.Currency != c {
		return db.Transfer{}, models.ErrCurrencyMismatch
	}

	if e := checkNewPayeeAllowance(b, in.AmountMinor, c, time.Now()); e != nil {
		return db.Transfer{}, e
	}
	src, e := q.WalletByUser(ctx, db.WalletByUserParams{UserID: u, Currency: c})
	if e != nil {
		return db.Transfer{}, notFound(models.ErrWalletNotFound, e)
	}
	if n, e := q.ReserveWallet(ctx, db.ReserveWalletParams{WalletID: src.ID, AmountMinor: in.AmountMinor}); e != nil {
		return db.Transfer{}, e
	} else if n != 1 {
		return db.Transfer{}, ErrInsufficientFunds
	}
	h, e := q.CreateHold(ctx, db.CreateHoldParams{WalletID: src.ID, AmountMinor: in.AmountMinor, ExpiresAt: time.Now().Add(24 * time.Hour)})
	if e != nil {
		return db.Transfer{}, e
	}
	p := db.CreateTransferParams{Reference: "NBL-TRF-" + uuid.NewString(), SenderUserID: u, SourceWalletID: src.ID, BeneficiaryID: b.ID, Type: b.Type,
		SendAmountMinor: in.AmountMinor, SendCurrency: c,
		ReceiveAmountMinor: in.AmountMinor, ReceiveCurrency: c,
		TotalDebitMinor: in.AmountMinor, Status: "pending",
		HoldID: pgtype.UUID{Bytes: h.ID, Valid: true}, IdempotencyKey: in.IdempotencyKey,
		Narrative: pgtype.Text{String: in.Narrative, Valid: in.Narrative != ""}}
	var dst db.Wallet
	if b.Type == "internal" {
		recipientID := uuid.UUID(b.RecipientUserID.Bytes)
		dst, e = q.WalletByUserForUpdate(ctx, db.WalletByUserForUpdateParams{UserID: recipientID, Currency: c})
		if e != nil {
			if !errors.Is(e, pgx.ErrNoRows) {
				return db.Transfer{}, e
			}
			log.Printf("transfer internal recipient wallet missing; creating wallet recipient_user_id=%s currency=%s", recipientID, c)
			dst, e = createWalletWithLedgerInTx(ctx, tx, recipientID, c, "Main wallet")
			if e != nil {
				return db.Transfer{}, e
			}
		}
		if !dst.LedgerAccountID.Valid {
			log.Printf("transfer internal recipient wallet has no ledger account; repairing wallet_id=%s recipient_user_id=%s currency=%s", dst.ID, recipientID, c)
			dst, e = attachWalletLedgerInTx(ctx, tx, dst, recipientID, c, "Main wallet")
			if e != nil {
				return db.Transfer{}, e
			}
		}
		recipientLimit, e := q.LimitByUser(ctx, db.LimitByUserParams{UserID: recipientID, Currency: c})
		if e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return db.Transfer{}, ErrLimitsMissing
			}
			return db.Transfer{}, e
		}

		if recipientLimit.BalanceCapMinor > 0 && dst.AvailableMinor+in.AmountMinor > recipientLimit.BalanceCapMinor {
			return db.Transfer{}, ErrBalanceCap
		}
		p.DestinationWalletID = pgtype.UUID{Bytes: dst.ID, Valid: true}
		p.Status = "pending"
	}
	tr, e := q.CreateTransfer(ctx, p)
	if e != nil {
		// The read at the top of this function catches the ordinary replay. This

		var pgErr *pgconn.PgError
		if errors.As(e, &pgErr) && isDuplicateKey(pgErr) {
			_ = tx.Rollback(ctx)
			return s.q.TransferByKey(ctx, db.TransferByKeyParams{SenderUserID: u, IdempotencyKey: in.IdempotencyKey})
		}
		return db.Transfer{}, e
	}

	if _, e = q.CreateTransferEvent(ctx, db.CreateTransferEventParams{
		TransferID:  tr.ID,
		ToStatus:    "pending",
		ActorType:   "user",
		ActorUserID: pgtype.UUID{Bytes: u, Valid: true},
		Reason:      pgtype.Text{String: "transfer initiated", Valid: true},
	}); e != nil {
		return db.Transfer{}, e
	}

	if auth.pep {
		metrics.ComplianceFlags.WithLabelValues("pep").Inc()
	}
	if b.Type != "internal" {
		payload, _ := json.Marshal(map[string]string{"transfer_id": tr.ID.String()})
		if _, e = q.CreateOutboxEvent(ctx, db.CreateOutboxEventParams{AggregateType: "transfer", AggregateID: tr.ID, EventType: "transfer.execute_payout", Payload: payload}); e != nil {
			return db.Transfer{}, e
		}
		if e = tx.Commit(ctx); e != nil {
			return db.Transfer{}, e
		}
		metrics.TransfersInitiated.WithLabelValues("external").Inc()
		return tr, nil
	}
	lt, e := q.InsertLedgerTransaction(ctx, db.InsertLedgerTransactionParams{Reference: tr.Reference, Kind: "internal_transfer", IdempotencyKey: pgtype.Text{String: in.IdempotencyKey, Valid: true}})
	if e != nil {
		return db.Transfer{}, e
	}
	if _, e = q.InsertLedgerEntry(ctx, db.InsertLedgerEntryParams{TransactionID: lt.ID, WalletID: pgUUIDOf(src.ID), EntryType: pgText("debit"), AmountMinor: in.AmountMinor, Currency: c}); e != nil {
		return db.Transfer{}, e
	}
	if _, e = q.InsertLedgerEntry(ctx, db.InsertLedgerEntryParams{TransactionID: lt.ID, WalletID: pgUUIDOf(dst.ID), EntryType: pgText("credit"), AmountMinor: in.AmountMinor, Currency: c}); e != nil {
		return db.Transfer{}, e
	}
	if e = captureReservation(ctx, q, h.ID, src.ID, in.AmountMinor); e != nil {
		return db.Transfer{}, e
	}
	if e = q.CreditWallet(ctx, db.CreditWalletParams{WalletID: dst.ID, AmountMinor: in.AmountMinor}); e != nil {
		return db.Transfer{}, e
	}
	srcBalance, e := walletLedgerBalance(ctx, tx, src.ID)
	if e != nil {
		return db.Transfer{}, e
	}
	dstBalance, e := walletLedgerBalance(ctx, tx, dst.ID)
	if e != nil {
		return db.Transfer{}, e
	}
	if e = recordWalletTransaction(ctx, tx, walletFeedInput{
		WalletID:            src.ID,
		CustomerID:          u,
		LedgerTransactionID: lt.ID,
		Direction:           "out",
		AmountMinor:         in.AmountMinor,
		Currency:            c,
		BalanceAfterMinor:   srcBalance,
		TransactionType:     "internal_transfer",
		CounterpartyName:    strings.TrimSpace(b.AccountName),
		CounterpartyType:    "internal",
		CounterpartyID:      uuid.UUID(b.RecipientUserID.Bytes),
		Description:         "Transfer to " + internalCounterpartyLabel(b),
		SourceType:          "transfer",
		SourceID:            tr.ID,
	}); e != nil {
		return db.Transfer{}, e
	}
	if e = recordWalletTransaction(ctx, tx, walletFeedInput{
		WalletID:            dst.ID,
		CustomerID:          dst.UserID,
		LedgerTransactionID: lt.ID,
		Direction:           "in",
		AmountMinor:         in.AmountMinor,
		Currency:            c,
		BalanceAfterMinor:   dstBalance,
		TransactionType:     "internal_transfer",
		CounterpartyName:    "Nablr user",
		CounterpartyType:    "internal",
		CounterpartyID:      u,
		Description:         "Transfer from Nablr user",
		SourceType:          "transfer",
		SourceID:            tr.ID,
	}); e != nil {
		return db.Transfer{}, e
	}
	if e = completeTransfer(ctx, q, tr.ID); e != nil {
		return db.Transfer{}, e
	}
	// Settlement is synchronous for an internal transfer, so its completion is
	// recorded in the same transaction — the timeline reads initiated → completed.
	if _, e = q.CreateTransferEvent(ctx, db.CreateTransferEventParams{
		TransferID: tr.ID,
		FromStatus: pgtype.Text{String: "pending", Valid: true},
		ToStatus:   "completed",
		ActorType:  "system",
		Reason:     pgtype.Text{String: "internal transfer settled", Valid: true},
	}); e != nil {
		return db.Transfer{}, e
	}
	// Debit the sender and credit the recipient — one alert each, enqueued in
	// this same transaction so they cannot outlive a rolled-back transfer.
	if e = enqueueNotification(ctx, q, tr.ID, notification{UserID: u, Kind: notifyDebit, AmountMinor: tr.SendAmountMinor, Currency: tr.SendCurrency, Reference: tr.Reference}); e != nil {
		return db.Transfer{}, e
	}
	if e = enqueueNotification(ctx, q, tr.ID, notification{UserID: dst.UserID, Kind: notifyCredit, AmountMinor: tr.SendAmountMinor, Currency: tr.SendCurrency, Reference: tr.Reference}); e != nil {
		return db.Transfer{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return db.Transfer{}, e
	}
	metrics.TransfersInitiated.WithLabelValues("internal").Inc()
	return s.q.TransferByID(ctx, db.TransferByIDParams{ID: tr.ID, SenderUserID: u})
}

func (s *Service) logPINAuthorizationMismatch(ctx context.Context, tokenID, userID uuid.UUID, amountMinor int64, currency, recipientDescriptor string) {
	var storedUser uuid.UUID
	var storedAmount int64
	var storedCurrency, storedDescriptor string
	var expiresAt time.Time
	var consumedAt pgtype.Timestamptz
	err := s.pool.QueryRow(ctx, `
SELECT user_id, amount_minor, currency, recipient_descriptor, expires_at, consumed_at
  FROM transfer_pin_authorizations
 WHERE id = $1
`, tokenID).Scan(&storedUser, &storedAmount, &storedCurrency, &storedDescriptor, &expiresAt, &consumedAt)
	if err != nil {
		log.Printf("transfer pin authorization row lookup failed token_id=%s error=%v", tokenID, err)
		return
	}
	log.Printf("transfer pin authorization mismatch token_id=%s requested_user_id=%s stored_user_id=%s requested_amount_minor=%d stored_amount_minor=%d requested_currency=%s stored_currency=%s requested_recipient_descriptor=%q stored_recipient_descriptor=%q expires_at=%s consumed_at_valid=%t consumed_at=%s",
		tokenID, userID, storedUser, amountMinor, storedAmount, currency, storedCurrency, recipientDescriptor, storedDescriptor, expiresAt.Format(time.RFC3339), consumedAt.Valid, consumedAt.Time.Format(time.RFC3339))
}

type walletFeedInput struct {
	WalletID            uuid.UUID
	CustomerID          uuid.UUID
	LedgerTransactionID uuid.UUID
	Direction           string
	AmountMinor         int64
	Currency            string
	BalanceAfterMinor   int64
	TransactionType     string
	CounterpartyName    string
	CounterpartyType    string
	CounterpartyID      uuid.UUID
	Description         string
	SourceType          string
	SourceID            uuid.UUID
}

func walletLedgerBalance(ctx context.Context, tx pgx.Tx, walletID uuid.UUID) (int64, error) {
	var balance int64
	err := tx.QueryRow(ctx, `
SELECT la.balance_minor
  FROM wallets w
  JOIN ledger_accounts la ON la.id = w.ledger_account_id
 WHERE w.id = $1
`, walletID).Scan(&balance)
	return balance, err
}

func createWalletWithLedgerInTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, currency, name string) (db.Wallet, error) {
	walletID := uuid.New()
	if name == "" {
		name = "Main wallet"
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO wallets (id, user_id, customer_id, currency, name, wallet_type, is_default, status)
VALUES ($1, $2, $2, $3, $4, 'spending', true, 'active')
ON CONFLICT (user_id, currency) DO UPDATE SET updated_at = now()
`, walletID, userID, currency, name); err != nil {
		return db.Wallet{}, err
	}
	var wallet db.Wallet
	if err := tx.QueryRow(ctx, `
SELECT id, user_id, currency, status, available_minor, reserved_minor, created_at, updated_at,
       ledger_account_id, account_id, customer_id, name, wallet_type, is_default
  FROM wallets
 WHERE user_id = $1 AND currency = $2
 FOR UPDATE
`, userID, currency).Scan(&wallet.ID, &wallet.UserID, &wallet.Currency, &wallet.Status, &wallet.AvailableMinor, &wallet.ReservedMinor, &wallet.CreatedAt, &wallet.UpdatedAt, &wallet.LedgerAccountID, &wallet.AccountID, &wallet.CustomerID, &wallet.Name, &wallet.WalletType, &wallet.IsDefault); err != nil {
		return db.Wallet{}, err
	}
	return attachWalletLedgerInTx(ctx, tx, wallet, userID, currency, name)
}

func attachWalletLedgerInTx(ctx context.Context, tx pgx.Tx, wallet db.Wallet, userID uuid.UUID, currency, name string) (db.Wallet, error) {
	if wallet.LedgerAccountID.Valid {
		return wallet, nil
	}
	accountID := uuid.New()
	if name == "" {
		name = "Main wallet"
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO ledger_accounts
	(id, account_type, currency, owner_type, customer_id,
	 name, code, balance_minor, reserved_minor, pending_minor,
	 allows_negative, status)
VALUES ($1, 'wallet', $2, 'customer', $3, $4, $5, 0, 0, 0, false, 'active')
`, accountID, currency, userID, name, "wlt-"+wallet.ID.String()[:8]); err != nil {
		return db.Wallet{}, err
	}
	if _, err := tx.Exec(ctx, `
UPDATE wallets
   SET ledger_account_id = $1,
       account_id = $1,
       customer_id = $2,
       name = coalesce(name, $3),
       updated_at = now()
 WHERE id = $4
`, accountID, userID, name, wallet.ID); err != nil {
		return db.Wallet{}, err
	}
	if err := tx.QueryRow(ctx, `
SELECT id, user_id, currency, status, available_minor, reserved_minor, created_at, updated_at,
       ledger_account_id, account_id, customer_id, name, wallet_type, is_default
  FROM wallets
 WHERE id = $1
 FOR UPDATE
`, wallet.ID).Scan(&wallet.ID, &wallet.UserID, &wallet.Currency, &wallet.Status, &wallet.AvailableMinor, &wallet.ReservedMinor, &wallet.CreatedAt, &wallet.UpdatedAt, &wallet.LedgerAccountID, &wallet.AccountID, &wallet.CustomerID, &wallet.Name, &wallet.WalletType, &wallet.IsDefault); err != nil {
		return db.Wallet{}, err
	}
	return wallet, nil
}

func recordWalletTransaction(ctx context.Context, tx pgx.Tx, in walletFeedInput) error {
	_, err := tx.Exec(ctx, `
INSERT INTO wallet_transactions
	(wallet_id, customer_id, ledger_transaction_id, direction, amount_minor,
	 currency, fee_minor, balance_after_minor, transaction_type,
	 counterparty_name, counterparty_type, counterparty_id,
	 description, source_type, source_id)
VALUES ($1,$2,$3,$4,$5,$6,0,$7,$8,$9,$10,$11,$12,$13,$14)
`, in.WalletID, in.CustomerID, in.LedgerTransactionID, in.Direction, in.AmountMinor,
		in.Currency, in.BalanceAfterMinor, in.TransactionType, in.CounterpartyName,
		in.CounterpartyType, in.CounterpartyID, in.Description, in.SourceType, in.SourceID)
	return err
}

func internalCounterpartyLabel(b db.Beneficiary) string {
	switch {
	case strings.TrimSpace(b.RecipientUsername.String) != "":
		return "@" + strings.TrimPrefix(strings.TrimSpace(b.RecipientUsername.String), "@")
	case strings.TrimSpace(b.RecipientAccountNumber.String) != "":
		return strings.TrimSpace(b.RecipientAccountNumber.String)
	case strings.TrimSpace(b.AccountName) != "":
		return strings.TrimSpace(b.AccountName)
	default:
		return "Nablr user"
	}
}

func (s *Service) livePINAuthorization(ctx context.Context, tokenID, userID uuid.UUID) (db.TransferPinAuthorization, error) {
	row := s.pool.QueryRow(ctx, `
SELECT id, user_id, amount_minor, currency, recipient_descriptor, created_at, expires_at, consumed_at
  FROM transfer_pin_authorizations
 WHERE id = $1
   AND user_id = $2
   AND consumed_at IS NULL
   AND expires_at > now()
`, tokenID, userID)
	var auth db.TransferPinAuthorization
	err := row.Scan(&auth.ID, &auth.UserID, &auth.AmountMinor, &auth.Currency, &auth.RecipientDescriptor, &auth.CreatedAt, &auth.ExpiresAt, &auth.ConsumedAt)
	return auth, err
}

func (in *TransferInput) applyRecipientDescriptor() {
	descriptor := strings.TrimSpace(in.RecipientDescriptor)
	const beneficiaryPrefix = "beneficiary:"
	const bankPrefix = "bank:"
	const internalPrefix = "internal:"

	switch {
	case strings.HasPrefix(descriptor, beneficiaryPrefix):
		if id, err := uuid.Parse(strings.TrimPrefix(descriptor, beneficiaryPrefix)); err == nil {
			in.BeneficiaryID = id
			in.RecipientType, in.RecipientReference = "", ""
			in.BankCode, in.AccountNumber = "", ""
		}
	case strings.HasPrefix(descriptor, bankPrefix):
		rest := strings.TrimPrefix(descriptor, bankPrefix)
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) == 2 {
			in.BeneficiaryID = uuid.Nil
			in.RecipientType = "bank"
			in.BankCode = parts[0]
			in.AccountNumber = parts[1]
		}
	case strings.HasPrefix(descriptor, internalPrefix):
		in.BeneficiaryID = uuid.Nil
		in.RecipientType = "internal"
		in.RecipientReference = strings.TrimPrefix(descriptor, internalPrefix)
	}
}

func (s *Service) TransferByID(ctx context.Context, u, id uuid.UUID) (db.Transfer, error) {
	t, err := s.q.TransferByID(ctx, db.TransferByIDParams{ID: id, SenderUserID: u})
	if err != nil {
		return db.Transfer{}, notFound(models.ErrTransferNotFound, err)
	}
	return t, nil
}

func (s *Service) TransferEvents(ctx context.Context, u, id uuid.UUID) ([]db.TransferEventsByTransferRow, error) {
	if _, err := s.TransferByID(ctx, u, id); err != nil {
		return nil, err
	}
	return s.q.TransferEventsByTransfer(ctx, id)
}

func (s *Service) RecentRecipients(ctx context.Context, u uuid.UUID, limit int32) ([]db.RecentRecipientsRow, error) {
	return s.q.RecentRecipients(ctx, db.RecentRecipientsParams{SenderUserID: u, Limit: limit})
}

// HandlePayoutSettled is idempotent: a repeated authenticated provider event
// sees a completed transfer and performs no second posting.
func (s *Service) HandlePayoutSettled(ctx context.Context, provider, reference string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	t, err := q.TransferByProviderReference(ctx, db.TransferByProviderReferenceParams{ProviderName: pgtype.Text{String: provider, Valid: true}, ProviderReference: pgtype.Text{String: reference, Valid: true}})
	if err != nil {
		return err
	}
	if t.Status == "completed" {
		return tx.Commit(ctx)
	}
	// under_review is settleable. A parked payout is one whose outcome we could
	// not determine, so a signed settlement event for it is not an anomaly — it
	// is the evidence the park was waiting for.
	if t.Status != "processing" && t.Status != "pending" && t.Status != "under_review" {
		return fmt.Errorf("%w: payout in status %q cannot be settled", models.ErrInvalidTransition, t.Status)
	}
	if !t.HoldID.Valid {
		return errors.New("external payout has no hold")
	}
	clearing, err := q.CreateWallet(ctx, db.CreateWalletParams{UserID: clearingUserID, Currency: t.SendCurrency})
	if err != nil {
		return err
	}
	ledger, err := q.InsertLedgerTransaction(ctx, db.InsertLedgerTransactionParams{Reference: "settle:" + t.Reference, Kind: "external_transfer", IdempotencyKey: pgtype.Text{String: "settle:" + t.ID.String(), Valid: true}})
	if err != nil {
		return err
	}
	if _, err = q.InsertLedgerEntry(ctx, db.InsertLedgerEntryParams{TransactionID: ledger.ID, WalletID: pgUUIDOf(t.SourceWalletID), EntryType: pgText("debit"), AmountMinor: t.SendAmountMinor, Currency: t.SendCurrency}); err != nil {
		return err
	}
	if _, err = q.InsertLedgerEntry(ctx, db.InsertLedgerEntryParams{TransactionID: ledger.ID, WalletID: pgUUIDOf(clearing.ID), EntryType: pgText("credit"), AmountMinor: t.SendAmountMinor, Currency: t.SendCurrency}); err != nil {
		return err
	}
	if err = captureReservation(ctx, q, uuid.UUID(t.HoldID.Bytes), t.SourceWalletID, t.SendAmountMinor); err != nil {
		return err
	}
	if err = q.CreditWallet(ctx, db.CreditWalletParams{WalletID: clearing.ID, AmountMinor: t.SendAmountMinor}); err != nil {
		return err
	}
	if err = completeTransfer(ctx, q, t.ID); err != nil {
		return err
	}
	_ = q.MarkBeneficiaryUsed(ctx, t.BeneficiaryID)
	_, _ = q.CreateTransferEvent(ctx, db.CreateTransferEventParams{TransferID: t.ID, FromStatus: pgtype.Text{String: t.Status, Valid: true}, ToStatus: "completed", ActorType: "provider", Reason: pgtype.Text{String: "payout settled", Valid: true}})
	// The money has now left for the payee's bank: this is where an external
	// transfer earns its debit alert (at initiation it was only reserved).
	if err = enqueueNotification(ctx, q, t.ID, notification{UserID: t.SenderUserID, Kind: notifyDebit, AmountMinor: t.SendAmountMinor, Currency: t.SendCurrency, Reference: t.Reference}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) HandlePayoutFailed(ctx context.Context, provider, reference, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	t, err := q.TransferByProviderReference(ctx, db.TransferByProviderReferenceParams{ProviderName: pgtype.Text{String: provider, Valid: true}, ProviderReference: pgtype.Text{String: reference, Valid: true}})
	if err != nil {
		return err
	}
	if t.Status == "failed" {
		return tx.Commit(ctx)
	}
	/*
		Only an in-flight or parked payout may be failed. A "failed" event for a
			transfer that already settled is a RETURN, not a failure: the money left,
			the clearing account was already debited, and releasing the reservation here
			would credit the customer a second time while the transfer stays
			completed — free money and an unbalanced ledger. Returns are a
			completed -> reversed movement with its own ledger posting, so this path
			refuses rather than guessing.
	*/
	if t.Status != "pending" && t.Status != "processing" && t.Status != "under_review" {
		return fmt.Errorf("%w: payout in status %q cannot be failed", models.ErrInvalidTransition, t.Status)
	}
	if !t.HoldID.Valid {
		return errors.New("payout has no hold")
	}
	if err = releaseReservation(ctx, q, uuid.UUID(t.HoldID.Bytes), t.SourceWalletID, t.SendAmountMinor); err != nil {
		return err
	}
	if err = failTransfer(ctx, q, t.ID, "provider_failed", reason); err != nil {
		return err
	}
	_, _ = q.CreateTransferEvent(ctx, db.CreateTransferEventParams{TransferID: t.ID, FromStatus: pgtype.Text{String: t.Status, Valid: true}, ToStatus: "failed", ActorType: "provider", Reason: pgtype.Text{String: reason, Valid: true}})
	if err = enqueueNotification(ctx, q, t.ID, notification{UserID: t.SenderUserID, Kind: notifyFailed, AmountMinor: t.SendAmountMinor, Currency: t.SendCurrency, Reference: t.Reference, Reason: reason}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) RecordProviderWebhook(ctx context.Context, provider, eventID, eventType, digest string, payload []byte, valid bool) (bool, error) {
	_, err := s.q.RecordProviderWebhook(ctx, db.RecordProviderWebhookParams{ProviderName: provider, EventID: eventID, EventType: eventType, RawBodySha256: digest, Payload: payload, SignatureValid: valid, Status: "received"})
	metrics.WebhooksReceived.WithLabelValues(boolLabel(valid)).Inc()
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Cancel only succeeds before a payout is dispatched. It atomically returns
// the hold to the source wallet, then records the terminal state.
func (s *Service) Cancel(ctx context.Context, u, id uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	t, err := q.TransferByID(ctx, db.TransferByIDParams{ID: id, SenderUserID: u})
	if err != nil {
		return notFound(models.ErrTransferNotFound, err)
	}
	if t.Status != "pending" && t.Status != "created" {
		return models.ErrNotCancellable
	}
	n, err := q.CancelTransfer(ctx, db.CancelTransferParams{ID: id, SenderUserID: u})
	if err != nil {
		return err
	}
	// Zero rows means another writer moved the transfer out of a cancellable
	// state between the read above and this update. The loser of that race must
	// not release a hold on a payout that is now dispatched.
	if n != 1 {
		return models.ErrNotCancellable
	}
	if t.HoldID.Valid {
		if err = releaseReservation(ctx, q, uuid.UUID(t.HoldID.Bytes), t.SourceWalletID, t.SendAmountMinor); err != nil {
			return err
		}
	}
	_, err = q.CreateTransferEvent(ctx, db.CreateTransferEventParams{TransferID: id, FromStatus: pgtype.Text{String: t.Status, Valid: true}, ToStatus: "cancelled", ActorType: "user", ActorUserID: pgtype.UUID{Bytes: u, Valid: true}, Reason: pgtype.Text{String: "cancelled by customer", Valid: true}})
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) Transfers(ctx context.Context, u uuid.UUID, n int32) ([]db.Transfer, error) {
	if n < 1 || n > 100 {
		n = 25
	}
	return s.q.ListTransfers(ctx, db.ListTransfersParams{SenderUserID: u, Limit: n})
}

type ScheduleInput struct {
	Name                string
	BeneficiaryID       uuid.UUID
	RecipientType       string
	RecipientReference  string
	BankCode            string
	AccountNumber       string
	AccountName         string
	AmountMinor         int64
	Currency            string
	ScheduleType        string
	Frequency           string
	Narrative           string
	FirstRun            time.Time
	EndDate             *time.Time
	MaxOccurrences      *int32
	PINAuthorizationID  uuid.UUID
	RecipientDescriptor string
}

func (s *Service) CreateSchedule(ctx context.Context, u uuid.UUID, in ScheduleInput) (db.ScheduledPayment, error) {
	if in.AmountMinor <= 0 {
		return db.ScheduledPayment{}, scheduleValidation("invalid_amount", "amount", "amount must be greater than zero")
	}
	c, err := currency(in.Currency)
	if err != nil {
		return db.ScheduledPayment{}, scheduleValidation("invalid_currency", "currency", "currency must be a three-letter ISO code such as NGN")
	}
	in.ScheduleType = strings.ToLower(strings.TrimSpace(in.ScheduleType))
	switch in.ScheduleType {
	case "just_once", "once", "one-time", "one_time":
		in.ScheduleType = "one_off"
	case "repeating", "repeat":
		in.ScheduleType = "recurring"
	}
	if in.ScheduleType != "one_off" && in.ScheduleType != "recurring" {
		return db.ScheduledPayment{}, scheduleValidation("invalid_schedule_type", "schedule_type", "schedule_type must be one_off or recurring")
	}
	in.Frequency, err = normaliseScheduleFrequency(in.ScheduleType, in.Frequency)
	if err != nil {
		return db.ScheduledPayment{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		in.Name = strings.TrimSpace(in.Narrative)
	}
	if in.Name == "" {
		in.Name = "Scheduled payment"
	}
	if len(in.Name) > 120 {
		return db.ScheduledPayment{}, scheduleValidation("schedule_name_too_long", "name", "payment name must not exceed 120 characters")
	}
	if in.FirstRun.IsZero() || in.FirstRun.Before(time.Now().Add(-time.Minute)) {
		return db.ScheduledPayment{}, scheduleValidation("payment_date_in_past", "payment_date", "payment date must not be in the past")
	}
	if in.EndDate != nil && in.EndDate.Before(in.FirstRun) {
		return db.ScheduledPayment{}, scheduleValidation("invalid_end_date", "end_date", "end date must be on or after the first payment date")
	}
	if in.MaxOccurrences != nil && *in.MaxOccurrences < 1 {
		return db.ScheduledPayment{}, scheduleValidation("invalid_max_occurrences", "max_occurrences", "max_occurrences must be greater than zero")
	}
	if in.ScheduleType == "one_off" {
		in.EndDate, in.MaxOccurrences = nil, nil
	}
	if in.BeneficiaryID == uuid.Nil {
		switch in.RecipientType {
		case "bank":
			if strings.TrimSpace(in.BankCode) == "" || len(strings.TrimSpace(in.AccountNumber)) < 6 {
				return db.ScheduledPayment{}, scheduleValidation("invalid_bank_recipient", "recipient", "bank_code and a valid account_number are required")
			}
		case "internal":
			if strings.TrimSpace(in.RecipientReference) == "" {
				return db.ScheduledPayment{}, scheduleValidation("invalid_internal_recipient", "recipient_reference", "Nablr recipient username, account number, or user ID is required")
			}
		default:
			return db.ScheduledPayment{}, scheduleValidation("invalid_recipient_type", "recipient_type", "recipient_type must be internal or bank")
		}
	}
	if in.PINAuthorizationID == uuid.Nil {
		return db.ScheduledPayment{}, models.ErrPINRequired
	}
	auth, err := s.livePINAuthorization(ctx, in.PINAuthorizationID, u)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.ScheduledPayment{}, models.ErrPINAuthInvalid
		}
		return db.ScheduledPayment{}, err
	}
	descriptor := strings.TrimSpace(in.RecipientDescriptor)
	if auth.AmountMinor != in.AmountMinor || auth.Currency != c || auth.RecipientDescriptor != descriptor {
		return db.ScheduledPayment{}, models.ErrPINAuthInvalid
	}
	if in.BeneficiaryID == uuid.Nil {
		in.BeneficiaryID, err = s.materializeInlineBeneficiary(ctx, u, TransferInput{
			RecipientType: in.RecipientType, RecipientReference: in.RecipientReference,
			BankCode: in.BankCode, AccountNumber: in.AccountNumber, AccountName: in.AccountName,
			Currency: c,
		})
		if err != nil {
			return db.ScheduledPayment{}, err
		}
	}
	ben, err := s.q.BeneficiaryByID(ctx, db.BeneficiaryByIDParams{ID: in.BeneficiaryID, UserID: u})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.ScheduledPayment{}, models.ErrBeneficiaryNotFound
		}
		return db.ScheduledPayment{}, err
	}
	if ben.Currency != c {
		return db.ScheduledPayment{}, scheduleValidation("currency_mismatch", "currency", "currency does not match the selected beneficiary")
	}
	w, err := s.q.WalletByUser(ctx, db.WalletByUserParams{UserID: u, Currency: c})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.ScheduledPayment{}, models.ErrWalletNotFound
		}
		return db.ScheduledPayment{}, err
	}
	dayOfMonth := pgtype.Int2{Int16: int16(in.FirstRun.Day()), Valid: in.ScheduleType == "recurring" && scheduleUsesDayOfMonth(in.Frequency)}
	dayOfWeek := pgtype.Int2{Int16: int16(in.FirstRun.Weekday()), Valid: in.ScheduleType == "recurring" && scheduleUsesDayOfWeek(in.Frequency)}
	endDate := pgtype.Timestamptz{}
	if in.EndDate != nil {
		endDate = pgtype.Timestamptz{Time: *in.EndDate, Valid: true}
	}
	maxOccurrences := pgtype.Int4{}
	if in.MaxOccurrences != nil {
		maxOccurrences = pgtype.Int4{Int32: *in.MaxOccurrences, Valid: true}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return db.ScheduledPayment{}, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	if _, err = q.ConsumePINAuthorization(ctx, db.ConsumePINAuthorizationParams{
		ID: in.PINAuthorizationID, UserID: u, AmountMinor: in.AmountMinor,
		Currency: c, RecipientDescriptor: descriptor,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.ScheduledPayment{}, models.ErrPINAuthInvalid
		}
		return db.ScheduledPayment{}, err
	}
	created, err := q.CreateSchedule(ctx, db.CreateScheduleParams{
		UserID: u, SourceWalletID: w.ID, BeneficiaryID: ben.ID, PaymentName: in.Name,
		ScheduleType: in.ScheduleType, Frequency: pgtype.Text{String: in.Frequency, Valid: in.Frequency != ""},
		DayOfMonth: dayOfMonth, DayOfWeek: dayOfWeek, AmountMinor: in.AmountMinor, Currency: c,
		Narrative: pgtype.Text{String: strings.TrimSpace(in.Narrative), Valid: strings.TrimSpace(in.Narrative) != ""},
		NextRunAt: in.FirstRun, EndDate: endDate, MaxOccurrences: maxOccurrences,
	})
	if err != nil {
		return db.ScheduledPayment{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return db.ScheduledPayment{}, err
	}
	return created, nil
}

func normaliseScheduleFrequency(scheduleType, frequency string) (string, error) {
	if scheduleType == "one_off" {
		return "", nil
	}
	frequency = strings.ToLower(strings.TrimSpace(frequency))
	switch frequency {
	case "daily", "weekly", "fortnightly", "monthly", "quarterly", "annually", "every_friday", "ramadan_daily":
		return frequency, nil
	case "every_day":
		return "daily", nil
	case "every_week":
		return "weekly", nil
	case "every_two_weeks", "biweekly":
		return "fortnightly", nil
	case "every_month":
		return "monthly", nil
	case "every_three_months":
		return "quarterly", nil
	case "every_year":
		return "annually", nil
	case "yearly", "annual":
		return "annually", nil
	default:
		return "", scheduleValidation("invalid_frequency", "frequency", "frequency must be daily, weekly, fortnightly, monthly, quarterly, or annually")
	}
}

func scheduleUsesDayOfMonth(frequency string) bool {
	return frequency == "monthly" || frequency == "quarterly" || frequency == "annually"
}

func scheduleUsesDayOfWeek(frequency string) bool {
	return frequency == "weekly" || frequency == "fortnightly" || frequency == "every_friday"
}
func (s *Service) Schedules(ctx context.Context, u uuid.UUID) ([]db.ScheduledPayment, error) {
	return s.q.ListSchedules(ctx, u)
}
func (s *Service) CancelSchedule(ctx context.Context, u, id uuid.UUID) error {
	existing, err := s.GetSchedule(ctx, u, id)
	if err != nil {
		return err
	}
	if existing.Status != "active" && existing.Status != "paused" {
		return scheduleValidation("schedule_cannot_be_cancelled", "status", "only an active or paused scheduled payment can be cancelled")
	}
	n, e := s.q.CancelSchedule(ctx, db.CancelScheduleParams{ID: id, UserID: u})
	if e != nil {
		return e
	}
	if n == 0 {
		return scheduleValidation("schedule_state_changed", "status", "scheduled payment status changed; refresh and try again")
	}
	return nil
}

// DispatchOutbox claims asynchronous work. Provider-specific execution is
// intentionally injected at the boundary; unsupported events remain failed
// for retry/operations rather than silently disappearing.
func (s *Service) DispatchOutbox(ctx context.Context, batch int32) error {
	if batch < 1 {
		batch = 50
	}
	events, err := s.q.ClaimOutboxEvents(ctx, batch)
	if err != nil {
		return err
	}
	for _, event := range events {
		// Notification events are delivered here rather than dropped. This is the
		// fix for the old silent-drop: a debit/credit/failed alert enqueued by the
		// money path is sent via notification-svc, with its own retry/backoff, and
		// never touches money, a ledger or a hold.
		if strings.HasPrefix(event.EventType, "notification.") {
			s.dispatchNotification(ctx, event)
			continue
		}
		if event.EventType != "transfer.execute_payout" {
			_ = s.q.MarkOutboxPublished(ctx, event.ID)
			continue
		}
		// An unparseable or untraceable event can never become payable: drop it
		// from the queue so it does not spin, and leave the (still reserved)
		// transfer to ReleaseUndispatched.
		var body struct {
			TransferID string `json:"transfer_id"`
		}
		if err = json.Unmarshal(event.Payload, &body); err != nil {
			_ = s.q.MarkOutboxPublished(ctx, event.ID)
			continue
		}
		id, err := uuid.Parse(body.TransferID)
		if err != nil {
			_ = s.q.MarkOutboxPublished(ctx, event.ID)
			continue
		}
		t, err := s.q.TransferInternalByID(ctx, id)
		if err != nil {
			_ = s.q.MarkOutboxPublished(ctx, event.ID)
			continue
		}
		if t.Status != "pending" && t.Status != "processing" {
			_ = s.q.MarkOutboxPublished(ctx, event.ID)
			continue
		}
		if !t.HoldID.Valid {
			_ = s.q.MarkOutboxPublished(ctx, event.ID)
			continue
		}
		account, err := s.decryptBankAccount(ctx, t.BeneficiaryID)
		if err != nil {
			if e := s.failDispatch(ctx, t, "payee_details_unreadable",
				"We could not read that payee's account details. Your money has not been taken."); e != nil {
				return e
			}
			_ = s.q.MarkOutboxPublished(ctx, event.ID)
			continue
		}
		b, err := s.q.BeneficiaryInternalByID(ctx, t.BeneficiaryID)
		if err != nil {
			_ = s.q.MarkOutboxPublished(ctx, event.ID)
			continue
		}
		bankCode, err := s.providerBankCode(ctx, b.BankCode.String)
		if err != nil {
			// The rail has no code for this bank and retrying cannot invent
			// one: refuse cleanly and hand the money back.
			if e := s.failDispatch(ctx, t, "bank_unsupported",
				"That bank cannot be reached right now. Your money has not been taken."); e != nil {
				return e
			}
			_ = s.q.MarkOutboxPublished(ctx, event.ID)
			continue
		}
		// Record the INTENT to dispatch, in its own committed transaction, before
		// the rail is called.
		//
		// This row is the only thing that later distinguishes "never sent" from
		// "sent, outcome unknown". Without it a crash between SendPayout and
		// RecordProviderDispatch leaves status='pending' with no provider
		// reference — indistinguishable from a payment that never left — and the
		// release sweep hands the customer money that may already be gone.
		//
		// It is written BEFORE the call and never after, so its existence proves
		// only "we were about to send". That is a weaker claim than "it was sent",
		// and weaker is what makes it safe: we would rather investigate a payment
		// that never left than refund one that did.
		key := t.ID.String()
		if _, err = s.q.RecordDispatchIntent(ctx, db.RecordDispatchIntentParams{
			TransferID:     t.ID,
			ProviderName:   s.payout.Info().Name,
			IdempotencyKey: key,
		}); err != nil {
			// The intent could not be persisted, so the guarantee above does not
			// hold and the payout must not be attempted. Retry later.
			_ = s.q.MarkOutboxFailed(ctx, db.MarkOutboxFailedParams{ID: event.ID, LastError: pgtype.Text{String: "could not record dispatch intent: " + err.Error(), Valid: true}, AvailableAt: time.Now().Add(time.Minute)})
			continue
		}
		// The rail is called under the circuit breaker: a Novac outage opens the
		// circuit after consecutive failures and dispatch parks (retryable) until
		// a probe succeeds. An open circuit is treated exactly like a retryable
		// rail answer — nothing was executed, come back in a minute.
		req := providers.PayoutRequest{
			IdempotencyKey: key,
			AmountMinor:    t.SendAmountMinor,
			Currency:       t.SendCurrency,
			CountryCode:    "NG",
			AccountNumber:  account,
			BankCode:       bankCode,
			BankName:       b.BankName.String,
			AccountName:    b.AccountName,
			Narrative:      t.Narrative.String,
		}
		providerName := s.payout.Info().Name
		var res *providers.PayoutResult
		sendErr := s.breaker.Guard(time.Now, func() error {
			// Metrics around the real rail call only: a circuit-open rejection
			// is booked separately, not counted as a rail RTT.
			start := time.Now()
			r, err := s.payout.SendPayout(ctx, req)
			metrics.ProviderLatency.WithLabelValues(providerName, "send_payout").Observe(time.Since(start).Seconds())
			metrics.ProviderOperations.WithLabelValues(providerName, "send_payout", outcomeOf(err)).Inc()
			if err != nil {
				return err
			}
			res = r
			return nil
		})
		if sendErr != nil {
			err = sendErr
		}
		observeCircuit(providerName, s.breaker)
		if err != nil {
			pe, ok := providers.AsError(err)
			switch {
			case ok && pe.Code == providers.ErrCircuitOpen:
				// Circuit-open is not a rail answer, so there is no provider
				// reference and no provider error code to file. Park and retry.
				metrics.DispatchFailures.WithLabelValues("circuit_open").Inc()
				s.settleIntent(ctx, key, pe.ProviderRef, "retryable", string(pe.Code), pe.Message)
				_ = s.q.MarkOutboxFailed(ctx, db.MarkOutboxFailedParams{ID: event.ID, LastError: pgtype.Text{String: pe.Message, Valid: true}, AvailableAt: time.Now().Add(time.Minute)})
			case ok && pe.Retryable:
				// The rail answered, and its answer was "not now" — rate limited,
				// unavailable, circuit open. Nothing was executed.
				metrics.DispatchFailures.WithLabelValues("retryable").Inc()
				s.settleIntent(ctx, key, pe.ProviderRef, "retryable", string(pe.Code), pe.Message)
				_ = s.q.MarkOutboxFailed(ctx, db.MarkOutboxFailedParams{ID: event.ID, LastError: pgtype.Text{String: pe.Message, Valid: true}, AvailableAt: time.Now().Add(time.Minute)})
			case ok && refundableOnRejection(pe.Code):
				// The rail refused, non-retryably, for a reason that can only mean
				// the instruction was never executed: it was malformed, unauthorised,
				// unsupported, or our own float was short. That is evidence, not
				// silence, and it is the ONLY shape that justifies automatically
				// giving the customer their money back.
				metrics.DispatchFailures.WithLabelValues("rejected").Inc()
				s.settleIntent(ctx, key, pe.ProviderRef, "rejected", string(pe.Code), pe.Message)
				if e := s.failDispatch(ctx, t, "provider_rejected",
					providerRejectedReason(pe.Code)); e != nil {
					return e
				}
				_ = s.q.MarkOutboxPublished(ctx, event.ID)
			default:
				// UNKNOWN — everything else, and the default on purpose.
				//
				// A bare network error, a context deadline, an explicit timeout, or a
				// non-retryable code we have not classified. And critically
				// ErrDuplicateRequest, which says the rail ALREADY HAS this
				// instruction: refunding on that would be a guaranteed double payment,
				// not a possible one.
				//
				// This is what the old code got wrong in the other direction — it fell
				// through to failDispatch and refunded on a timeout alone. Retry with
				// the SAME idempotency key instead, which is what the key is for, and
				// when the retries run out ReleaseUndispatched finds the intent row and
				// resolves it against the rail rather than guessing.
				metrics.DispatchFailures.WithLabelValues("unknown").Inc()
				s.settleIntent(ctx, key, providerRefOf(pe), "unknown", errorCodeOf(pe), err.Error())
				_ = s.q.MarkOutboxFailed(ctx, db.MarkOutboxFailedParams{ID: event.ID, LastError: pgtype.Text{String: "provider outcome unknown: " + err.Error(), Valid: true}, AvailableAt: time.Now().Add(2 * time.Minute)})
			}
			continue
		}
		// The rail accepted it. Everything below is bookkeeping about a payment
		// that has already been instructed, so a failure here is recoverable — the
		// intent row now carries the reference and resolveUndispatched repairs the
		// transfer from it — but it is never silent.
		s.settleIntent(ctx, key, res.ProviderRef, "success", "", "")
		if res.ProviderRef == "" {
			// Accepted without a handle. We cannot query it, cannot match its
			// webhook, and must not refund it: park it where a human will look.
			if e := s.reviewDispatch(ctx, t, "payout rail accepted the instruction without returning a reference"); e != nil {
				return e
			}
			_ = s.q.MarkOutboxPublished(ctx, event.ID)
			continue
		}
		if err = s.q.RecordProviderDispatch(ctx, db.RecordProviderDispatchParams{ID: t.ID, ProviderName: pgtype.Text{String: s.payout.Info().Name, Valid: true}, ProviderReference: pgtype.Text{String: res.ProviderRef, Valid: true}}); err != nil {
			log.Printf("transfers: payout %s dispatched as %s but the transfer row could not be updated: %v", t.ID, res.ProviderRef, err)
		}
		_ = s.q.MarkOutboxPublished(ctx, event.ID)
	}
	return nil
}

// refundableOnRejection is an allowlist of provider error codes that prove the
// instruction was never executed, and is therefore the complete set on which a
// customer's reserved money may be returned automatically.
//
// It is an allowlist rather than a denylist deliberately. A denylist means a new
// code from a rail — or a code an adapter classifies slightly differently —
// defaults to "refund", and the failure mode of a wrong refund is that we pay
// twice and cannot get it back. An allowlist means the same unknown code defaults
// to "investigate", and the failure mode of that is a support ticket.
//
// ErrDuplicateRequest is conspicuously absent: it means the rail already holds
// this instruction, which is the strongest possible signal NOT to refund.
// ErrTimeout, ErrUnknown, ErrUnavailable and ErrCircuitOpen are absent because
// none of them says what happened to the request.
func refundableOnRejection(code providers.ErrorCode) bool {
	switch code {
	case providers.ErrInvalidRequest, providers.ErrRejected,
		providers.ErrInsufficientFunds, providers.ErrUnauthenticated,
		providers.ErrNotSupported:
		return true
	default:
		return false
	}
}

// settleIntent closes a dispatch-intent row with what the rail answered.
//
// Errors are logged and swallowed on purpose. This is an audit write that
// happens after the payout call has already been made; failing the batch here
// would neither un-send the payment nor make the record any more accurate, and
// the intent row is already doing its real job — recording that a dispatch was
// attempted at all.
func (s *Service) settleIntent(ctx context.Context, key, providerRef, outcome, code, message string) {
	p := db.SettleDispatchIntentParams{
		ProviderName:   s.payout.Info().Name,
		IdempotencyKey: key,
		Outcome:        outcome,
	}
	if providerRef != "" {
		p.ProviderReference = pgtype.Text{String: providerRef, Valid: true}
	}
	if code != "" {
		p.ErrorCode = pgtype.Text{String: code, Valid: true}
	}
	if message != "" {
		p.ErrorMessage = pgtype.Text{String: truncate(message, 500), Valid: true}
	}
	if err := s.q.SettleDispatchIntent(ctx, p); err != nil {
		log.Printf("transfers: could not settle dispatch intent %s: %v", key, err)
	}
}

func errorCodeOf(pe *providers.Error) string {
	if pe == nil {
		return string(providers.ErrUnknown)
	}
	return string(pe.Code)
}

// providerRefOf keeps a reference the rail volunteered even while rejecting or
// timing out. On an unknown outcome that reference is the only handle
// reconciliation has, so it is recorded rather than discarded.
func providerRefOf(pe *providers.Error) string {
	if pe == nil {
		return ""
	}
	return pe.ProviderRef
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// decryptBankAccount is the only place a stored account number is read back:
// a worker that needs the plaintext for a provider call decrypts it here and
// nowhere else.
func (s *Service) decryptBankAccount(ctx context.Context, beneficiaryID uuid.UUID) (string, error) {
	ciphertext, err := s.q.BeneficiaryCiphertext(ctx, beneficiaryID)
	if err != nil {
		return "", err
	}
	if len(ciphertext) == 0 {
		return "", errors.New("no account number ciphertext on file for beneficiary")
	}
	plain, err := s.enc.Decrypt(ciphertext, crypto.AAD("beneficiaries", "account_number", beneficiaryID.String()))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// providerCodeColumn names the column holding a rail's own bank codes, or ""
// when the rail uses ours. Adding a rail is one line here plus a column.
func providerCodeColumn(provider string) string {
	switch provider {
	case "novac":
		return "novac_code"
	default:
		return ""
	}
}

// providerBankCode translates our bank code into the one the configured rail
// expects. A bank with no mapping is refused here, before any money moves:
// the alternative is a provider rejection that strands a customer's payment.
func (s *Service) providerBankCode(ctx context.Context, ourCode string) (string, error) {
	provider := s.payout.Info().Name
	column := providerCodeColumn(provider)
	if column == "" {
		return ourCode, nil // this rail speaks our codes
	}
	var code *string
	if err := s.pool.QueryRow(ctx,
		`SELECT `+column+` FROM banks WHERE code = $1 AND is_active`, ourCode).Scan(&code); err != nil {
		return "", err
	}
	if code == nil || *code == "" {
		return "", fmt.Errorf("bank %s is not supported by %s", ourCode, provider)
	}
	return *code, nil
}

func providerRejectedReason(code providers.ErrorCode) string {
	switch code {
	case providers.ErrInvalidRequest:
		return "The transfer details were rejected. Confirm the recipient details and try again. Your money has not been taken."
	case providers.ErrInsufficientFunds:
		return "The transfer could not be processed by the payout service. Your money has not been taken."
	case providers.ErrUnauthenticated:
		return "Bank transfers are temporarily unavailable. Please try again later. Your money has not been taken."
	case providers.ErrNotSupported:
		return "The recipient bank is not currently supported. Your money has not been taken."
	default:
		return "The payout provider rejected this transfer. Your money has not been taken."
	}
}

// failDispatch is the money-safe reaction to a dispatch failure the PROVIDER
// told us about. Because the rail answered — explicitly and non-retryably — the
// instruction demonstrably did not take effect, so the hold and the wallet
// reservation are returned and the transfer is failed.
//
// It must never be reached from a timeout, a dropped connection or any other
// silence. Silence is not a rejection: the request may have arrived, been
// executed, and only the response lost. Those cases go to reviewDispatch.
func (s *Service) failDispatch(ctx context.Context, t db.Transfer, code, reason string) error {
	if !t.HoldID.Valid {
		return errors.New("payout has no hold")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	if err = releaseReservation(ctx, q, uuid.UUID(t.HoldID.Bytes), t.SourceWalletID, t.SendAmountMinor); err != nil {
		return err
	}
	if err = failTransfer(ctx, q, t.ID, code, reason); err != nil {
		return err
	}
	_, _ = q.CreateTransferEvent(ctx, db.CreateTransferEventParams{TransferID: t.ID, FromStatus: pgtype.Text{String: t.Status, Valid: true}, ToStatus: "failed", ActorType: "system", Reason: pgtype.Text{String: code, Valid: true}})
	if err = enqueueNotification(ctx, q, t.ID, notification{UserID: t.SenderUserID, Kind: notifyFailed, AmountMinor: t.SendAmountMinor, Currency: t.SendCurrency, Reference: t.Reference, Reason: reason}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// reviewDispatch parks a transfer whose outcome is unknown.
//
// It is deliberately the opposite of failDispatch: nothing is released, nothing
// is credited, the hold stays exactly where it is. The customer's money remains
// reserved — not spendable, not returned — because either answer would be a
// guess, and the wrong guess is a double payment. The row moves to under_review
// so it stops being polled by the release sweep and starts being visible to
// operations.
func (s *Service) reviewDispatch(ctx context.Context, t db.Transfer, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	n, err := q.ReviewTransfer(ctx, db.ReviewTransferParams{ID: t.ID, FailureReason: pgtype.Text{String: reason, Valid: true}})
	if err != nil {
		return err
	}
	if n != 1 {
		// Something else resolved it first, with better evidence than we have.
		return tx.Rollback(ctx)
	}
	_, _ = q.CreateTransferEvent(ctx, db.CreateTransferEventParams{TransferID: t.ID, FromStatus: pgtype.Text{String: t.Status, Valid: true}, ToStatus: "under_review", ActorType: "system", Reason: pgtype.Text{String: reason, Valid: true}})
	return tx.Commit(ctx)
}

// RunDueSchedules is the worker entry point. A schedule is always executed
// through the same Quote/Transfer path as a manual payment, so limits and
// holds cannot be bypassed by a standing instruction.
func (s *Service) RunDueSchedules(ctx context.Context, batch int32) (int, error) {
	if batch < 1 {
		batch = 50
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	schedules, err := q.ClaimDueSchedules(ctx, batch)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	ran := 0
	for _, sc := range schedules {
		if (sc.MaxOccurrences.Valid && sc.OccurrenceCount >= sc.MaxOccurrences.Int32) ||
			(sc.EndDate.Valid && sc.NextRunAt.After(sc.EndDate.Time)) {
			_ = s.q.CompleteSchedule(ctx, sc.ID)
			_, _ = s.q.RecordScheduleRun(ctx, db.RecordScheduleRunParams{
				ScheduledPaymentID: sc.ID, ScheduledFor: sc.NextRunAt, Outcome: "skipped",
				ErrorMessage: pgtype.Text{String: "schedule end condition reached", Valid: true},
			})
			continue
		}
		key := "schedule:" + sc.ID.String() + ":" + sc.NextRunAt.UTC().Format(time.RFC3339Nano)
		tr, e := s.Transfer(ctx, sc.UserID, TransferInput{BeneficiaryID: sc.BeneficiaryID, AmountMinor: sc.AmountMinor, Currency: sc.Currency, IdempotencyKey: key, PreAuthorized: true, Narrative: sc.Narrative.String})
		if e != nil {
			_, _ = s.q.RecordScheduleRun(ctx, db.RecordScheduleRunParams{
				ScheduledPaymentID: sc.ID, ScheduledFor: sc.NextRunAt, Outcome: "failed",
				ErrorMessage: pgtype.Text{String: e.Error(), Valid: true},
			})
			_ = s.q.FailScheduleRun(ctx, db.FailScheduleRunParams{ID: sc.ID, NextRunAt: time.Now().Add(24 * time.Hour), PauseReason: pgtype.Text{String: e.Error(), Valid: true}})
			continue
		}
		_, _ = s.q.RecordScheduleRun(ctx, db.RecordScheduleRunParams{ScheduledPaymentID: sc.ID, ScheduledFor: sc.NextRunAt, Outcome: "succeeded", TransferID: pgtype.UUID{Bytes: tr.ID, Valid: true}})
		anchorDay := sc.NextRunAt.Day()
		if sc.DayOfMonth.Valid {
			anchorDay = int(sc.DayOfMonth.Int16)
		}
		next := nextScheduleRun(sc.NextRunAt, sc.ScheduleType, sc.Frequency.String, anchorDay)
		status := "active"
		if sc.ScheduleType == "one_off" ||
			(sc.MaxOccurrences.Valid && sc.OccurrenceCount+1 >= sc.MaxOccurrences.Int32) ||
			(sc.EndDate.Valid && next.After(sc.EndDate.Time)) {
			status = "completed"
		}
		_ = s.q.AdvanceSchedule(ctx, db.AdvanceScheduleParams{ID: sc.ID, NextRunAt: next, Status: status})
		ran++
	}
	return ran, nil
}

// ReleaseUndispatched resolves instructions that are old and have no provider
// reference. It is the sweep that stops a customer's money being reserved
// forever after a dispatch went wrong.
//
// The old implementation released on age alone. Age is not evidence: a transfer
// with no provider reference is either one that never reached the rail or one
// whose acknowledgement was lost, and those two look identical in the transfers
// table. Refunding the second is a double payment.
//
// The dispatch-intent row is what separates them:
//
//	no intent row   → the rail was provably never called. Release.
//	intent, no ref  → UNKNOWN. Ask the rail (the NIP TSQ pattern); release only
//	                  on an explicit "no record", otherwise park for review.
//	intent with ref → the send succeeded and only the bookkeeping was lost.
//	                  Repair the transfer row; never release.
//
// It returns the number of transfers whose funds were returned. Transfers parked
// for review are not counted, because nothing was released.
func (s *Service) ReleaseUndispatched(ctx context.Context, age time.Duration, batch int32) (int, error) {
	if age <= 0 {
		age = 10 * time.Minute
	}
	if batch < 1 {
		batch = 50
	}
	items, err := s.q.PendingTransfersBefore(ctx, db.PendingTransfersBeforeParams{CreatedAt: time.Now().Add(-age), Limit: batch})
	if err != nil {
		return 0, err
	}
	released := 0
	for _, t := range items {
		n, err := s.resolveUndispatched(ctx, t)
		if err != nil {
			return released, err
		}
		released += n
	}
	return released, nil
}

// resolveUndispatched decides the fate of one transfer that is pending with no
// provider reference, and reports whether its funds were returned.
func (s *Service) resolveUndispatched(ctx context.Context, t db.Transfer) (int, error) {
	if !t.HoldID.Valid {
		return 0, nil
	}
	intent, err := s.q.DispatchIntentByTransfer(ctx, db.DispatchIntentByTransferParams{
		TransferID:   t.ID,
		ProviderName: s.PayoutRailName(),
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Nothing was ever attempted against the rail. The money is provably
		// still ours and the customer gets it back.
		return 1, s.releaseNeverDispatched(ctx, t)
	case err != nil:
		return 0, err
	}

	// The dispatch was attempted. Whether it took effect is not ours to assume.
	if intent.ProviderReference.Valid && intent.ProviderReference.String != "" {
		// The rail assigned a reference, which it only does for an instruction it
		// has accepted — whether it then acknowledged cleanly, timed out, or came
		// back as a duplicate. Repair the transfer row so ReconcileProcessing owns
		// it from here, and release nothing.
		return 0, s.q.RecordProviderDispatch(ctx, db.RecordProviderDispatchParams{
			ID:                t.ID,
			ProviderName:      pgtype.Text{String: s.PayoutRailName(), Valid: true},
			ProviderReference: intent.ProviderReference,
		})
	}
	return s.resolveAgainstRail(ctx, t, intent.IdempotencyKey)
}

// resolveAgainstRail asks the provider what happened to a payout we cannot
// account for, keyed by the idempotency key we sent — a transaction-status
// query. It is the only thing that can turn an unknown into a fact.
func (s *Service) resolveAgainstRail(ctx context.Context, t db.Transfer, key string) (int, error) {
	if s.payout == nil {
		return 0, s.reviewDispatch(ctx, t, "payout outcome unknown and no rail configured to query")
	}
	r, err := s.payout.PayoutStatus(ctx, key)
	if err != nil {
		pe, ok := providers.AsError(err)
		if ok && pe.Code == providers.ErrNotFound {
			// The rail has looked and has no record of it. This is the ONE answer
			// that makes releasing the customer's money safe, and it is why
			// ErrNotFound is a distinct code rather than folded into
			// ErrInvalidRequest.
			return 1, s.releaseNeverDispatched(ctx, t)
		}
		// Any other error means we still do not know. Park it; do not refund.
		return 0, s.reviewDispatch(ctx, t, "payout outcome could not be determined: "+err.Error())
	}
	switch r.Status {
	case providers.PayoutSettled, providers.PayoutFailed, providers.PayoutReturned,
		providers.PayoutPending, providers.PayoutProcessing:
		// In every one of these the rail HAS the payment. Record the reference so
		// the transfer becomes processing, then let the ordinary settle/fail paths
		// — the same ones a signed webhook uses — apply the outcome. Nothing is
		// released here.
		if r.ProviderRef == "" {
			return 0, s.reviewDispatch(ctx, t, "rail reported status "+string(r.Status)+" without a reference")
		}
		if err = s.q.RecordProviderDispatch(ctx, db.RecordProviderDispatchParams{
			ID:                t.ID,
			ProviderName:      pgtype.Text{String: s.PayoutRailName(), Valid: true},
			ProviderReference: pgtype.Text{String: r.ProviderRef, Valid: true},
		}); err != nil {
			return 0, err
		}
		switch r.Status {
		case providers.PayoutSettled:
			return 0, s.HandlePayoutSettled(ctx, s.PayoutRailName(), r.ProviderRef)
		case providers.PayoutFailed, providers.PayoutReturned:
			return 0, s.HandlePayoutFailed(ctx, s.PayoutRailName(), r.ProviderRef, "rail reported "+string(r.Status)+" on reconciliation")
		}
		return 0, nil
	default:
		return 0, s.reviewDispatch(ctx, t, "rail reported unrecognised status "+string(r.Status))
	}
}

// releaseNeverDispatched returns the reserved money for a transfer we know never
// reached the rail. Every caller must have positive evidence of that — an absent
// intent row, or the provider answering "no record" — never merely the passage
// of time.
func (s *Service) releaseNeverDispatched(ctx context.Context, t db.Transfer) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	if err = releaseReservation(ctx, q, uuid.UUID(t.HoldID.Bytes), t.SourceWalletID, t.SendAmountMinor); err != nil {
		if errors.Is(err, ErrHoldAlreadyResolved) {
			// Another sweep got there first. Not an error, and not ours to redo.
			return nil
		}
		return err
	}
	if err = failTransfer(ctx, q, t.ID, "dispatch_not_attempted", "payout was never dispatched to the payout rail"); err != nil {
		return err
	}
	_, _ = q.CreateTransferEvent(ctx, db.CreateTransferEventParams{TransferID: t.ID, FromStatus: pgtype.Text{String: t.Status, Valid: true}, ToStatus: "failed", ActorType: "system", Reason: pgtype.Text{String: "undispatched transfer released", Valid: true}})
	return tx.Commit(ctx)
}

// ExpireStaleHolds resolves holds that have outlived their window. A processing
// payout's hold is extended, never released — the provider may still settle it.
// A pending one is resolved through the same evidence-based path as the release
// sweep, on the specific transfer whose hold expired.
func (s *Service) ExpireStaleHolds(ctx context.Context, batch int32) (int, error) {
	if batch < 1 {
		batch = 50
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	q := s.q.WithTx(tx)
	holds, err := q.ExpiredActiveTransferHolds(ctx, batch)
	if err != nil {
		_ = tx.Rollback(ctx)
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	resolved := 0
	for _, h := range holds {
		t, err := s.q.TransferInternalByID(ctx, h.TransferID_2)
		if err != nil {
			return resolved, err
		}
		if t.Status == "processing" || t.Status == "under_review" {
			if err = s.q.ExtendHold(ctx, db.ExtendHoldParams{ID: h.ID, ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}); err != nil {
				return resolved, err
			}
			continue
		}
		if t.Status == "pending" {
			// Resolve THIS transfer. The previous implementation called
			// ReleaseUndispatched(ctx, 0, 1), which re-ran the pending query and
			// released whichever transfer came back first — so an expired hold on
			// one payment could refund a different, unrelated one.
			n, err := s.resolveUndispatched(ctx, t)
			if err != nil {
				return resolved, err
			}
			resolved += n
		}
	}
	return resolved, nil
}

// ReconcileProcessing consults the configured provider for old dispatched
// payouts. A terminal result is passed through the same idempotent settlement
// or failure path used by signed webhooks.
func (s *Service) ReconcileProcessing(ctx context.Context, age time.Duration, batch int32) (int, int, error) {
	if age <= 0 {
		age = 5 * time.Minute
	}
	if batch < 1 {
		batch = 50
	}
	items, err := s.q.ProcessingTransfersBefore(ctx, db.ProcessingTransfersBeforeParams{SubmittedAt: pgtype.Timestamptz{Time: time.Now().Add(-age), Valid: true}, Limit: batch})
	if err != nil {
		return 0, 0, err
	}
	settled, failed := 0, 0
	for _, t := range items {
		if !t.ProviderReference.Valid {
			continue
		}
		r, err := s.payout.PayoutStatus(ctx, t.ProviderReference.String)
		if err != nil {
			continue
		}
		switch r.Status {
		case providers.PayoutSettled:
			if err = s.HandlePayoutSettled(ctx, t.ProviderName.String, t.ProviderReference.String); err != nil {
				return settled, failed, err
			}
			settled++
		case providers.PayoutFailed, providers.PayoutReturned:
			if err = s.HandlePayoutFailed(ctx, t.ProviderName.String, t.ProviderReference.String, "provider reconciliation failure"); err != nil {
				return settled, failed, err
			}
			failed++
		}
	}
	return settled, failed, nil
}

// ReconcileUnderReview drains the review queue.
//
// A transfer is parked because the rail could not be reached or could not say
// what happened — both usually temporary. Without this sweep the queue only ever
// grows, and a customer's money stays reserved until somebody notices, which in
// practice means until they complain.
//
// It never releases funds itself. It re-asks the rail through exactly the same
// evidence-based path the release sweep uses, so the only thing that can free a
// parked payment is still a definite answer: the provider saying it has no
// record, or saying what the outcome was.
func (s *Service) ReconcileUnderReview(ctx context.Context, age time.Duration, batch int32) (int, error) {
	if age <= 0 {
		age = 15 * time.Minute
	}
	if batch < 1 {
		batch = 50
	}
	items, err := s.q.UnderReviewTransfersBefore(ctx, db.UnderReviewTransfersBeforeParams{CreatedAt: time.Now().Add(-age), Limit: batch})
	if err != nil {
		return 0, err
	}
	resolved := 0
	for _, t := range items {
		if !t.HoldID.Valid {
			continue
		}
		// A parked transfer that already has a reference belongs to
		// ReconcileProcessing; attach it and let that sweep take over.
		if t.ProviderReference.Valid && t.ProviderReference.String != "" {
			if err = s.q.RecordProviderDispatch(ctx, db.RecordProviderDispatchParams{
				ID:                t.ID,
				ProviderName:      pgtype.Text{String: s.PayoutRailName(), Valid: true},
				ProviderReference: t.ProviderReference,
			}); err != nil {
				return resolved, err
			}
			resolved++
			continue
		}
		intent, err := s.q.DispatchIntentByTransfer(ctx, db.DispatchIntentByTransferParams{
			TransferID:   t.ID,
			ProviderName: s.PayoutRailName(),
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Parked with no intent row. Reaching here means a park happened on a
				// path that never called the rail, which should not occur — but the
				// evidence is the same as the primary case and points the same way, so
				// it is treated the same rather than left to accumulate.
				if e := s.releaseNeverDispatched(ctx, t); e != nil {
					return resolved, e
				}
				resolved++
				continue
			}
			return resolved, err
		}
		n, err := s.resolveAgainstRail(ctx, t, intent.IdempotencyKey)
		if err != nil {
			return resolved, err
		}
		resolved += n
	}
	return resolved, nil
}
