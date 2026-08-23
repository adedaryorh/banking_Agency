package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "nabla/transfers-svc/db/sqlc"
	"nabla/transfers-svc/internal/models"
)

const defaultPaymentRequestTTL = 7 * 24 * time.Hour

// paymentRequestListDefault bounds a list call that names no limit.
const paymentRequestListDefault = 20

func pgText(s string) pgtype.Text       { return pgtype.Text{String: s, Valid: s != ""} }
func pgUUIDOf(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
func asUUID(p pgtype.UUID) uuid.UUID    { return uuid.UUID(p.Bytes) }

type PaymentRequestInput struct {
	// PayerReference is who to bill: a @handle, a 10-digit Nablr number, or a raw
	// user id. PayerUserID is the pre-parsed id when the client sent a UUID.
	PayerReference string
	PayerUserID    *uuid.UUID
	AmountMinor    int64
	Currency       string
	Note           string
	// ExpiresIn overrides the default TTL; zero means the default.
	ExpiresIn time.Duration
}

func (s *Service) CreatePaymentRequest(ctx context.Context, requester uuid.UUID, in PaymentRequestInput) (db.PaymentRequest, error) {
	if in.AmountMinor <= 0 {
		return db.PaymentRequest{}, models.ErrZeroAmount
	}
	c, err := currency(in.Currency)
	if err != nil {
		return db.PaymentRequest{}, err
	}
	// Resolving the payer DOES fail closed: a request must name a real nablr user
	// (resolveInternalPayee returns ErrBeneficiaryNotFound for an unknown handle,
	// and 503 if identity is unreachable). A raw UUID is taken as-is, matching
	// AddBeneficiary.
	payerID := in.PayerUserID
	var payerProfile UserProfile
	if payerID == nil {
		id, prof, rerr := s.resolveInternalPayee(ctx, in.PayerReference)
		if rerr != nil {
			return db.PaymentRequest{}, rerr
		}
		payerID, payerProfile = id, prof
	}
	if payerID == nil {
		return db.PaymentRequest{}, models.ErrBeneficiaryNotFound
	}
	if *payerID == requester {
		return db.PaymentRequest{}, models.ErrSelfPaymentRequest
	}
	// Display capture is best-effort: a request is a message, so a display lookup
	// that fails must not stop it being created — the row just stores less.
	payerName, payerUsername := s.displayIdentity(ctx, *payerID, payerProfile)
	requesterName, requesterUsername := s.displayIdentity(ctx, requester, UserProfile{})

	ttl := in.ExpiresIn
	if ttl <= 0 {
		ttl = defaultPaymentRequestTTL
	}
	return s.q.CreatePaymentRequest(ctx, db.CreatePaymentRequestParams{
		ID:                uuid.New(),
		RequesterUserID:   requester,
		PayerUserID:       *payerID,
		AmountMinor:       in.AmountMinor,
		Currency:          c,
		Note:              pgText(strings.TrimSpace(in.Note)),
		RequesterName:     pgText(requesterName),
		RequesterUsername: pgText(requesterUsername),
		PayerName:         pgText(payerName),
		PayerUsername:     pgText(payerUsername),
		ExpiresAt:         pgtype.Timestamptz{Time: time.Now().Add(ttl), Valid: true},
	})
}

// displayIdentity returns the customer's real name (from KYC) and @handle (from
// GetUser) to stamp on a request. Best-effort by contract: any identity error
// yields empty strings and the client falls back to whatever identifier it has.
// A profile already in hand (from resolveInternalPayee) supplies the handle
// without a second GetUser call.
func (s *Service) displayIdentity(ctx context.Context, id uuid.UUID, known UserProfile) (name, username string) {
	username = known.NablrUsername
	if s.identity == nil {
		return "", username
	}
	if username == "" {
		if p, err := s.identity.GetUser(ctx, id); err == nil {
			username = p.NablrUsername
		}
	}
	if k, err := s.identity.GetKYCProfile(ctx, id); err == nil {
		name = strings.TrimSpace(k.FirstName + " " + k.LastName)
	}
	return name, username
}

func (s *Service) GetPaymentRequest(ctx context.Context, u, id uuid.UUID) (db.PaymentRequest, error) {
	r, err := s.q.PaymentRequestByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PaymentRequest{}, models.ErrPaymentRequestNotFound
		}
		return db.PaymentRequest{}, err
	}
	if r.RequesterUserID != u && r.PayerUserID != u {
		return db.PaymentRequest{}, models.ErrNotRequestParty
	}
	return r, nil
}

func (s *Service) ListPaymentRequests(ctx context.Context, u uuid.UUID, incoming, outgoing bool, limit int32) ([]db.PaymentRequest, error) {
	// No direction named means both: the send-money screen shows what I owe and
	// what I asked for together.
	if !incoming && !outgoing {
		incoming, outgoing = true, true
	}
	if limit <= 0 {
		limit = paymentRequestListDefault
	}
	return s.q.PaymentRequestsForUser(ctx, db.PaymentRequestsForUserParams{
		Incoming: incoming, Outgoing: outgoing, UserID: u, Lim: limit,
	})
}

// PayPaymentRequest settles a request by routing a transfer from the payer to
// the requester through the single money path. It is idempotent on the request:
// the transfer idempotency key is derived from the request id, so a repeated pay
// returns the SAME transfer rather than sending twice, and a prior partial
// success (transfer landed, paid-link not yet written) is reconciled here rather
// than paid again. The transfer still clears the full authorization control
// plane (PIN, status, sanctions, limits, new-payee cap) inside Service.Transfer.
func (s *Service) PayPaymentRequest(ctx context.Context, u, id uuid.UUID, pin string) (db.Transfer, db.PaymentRequest, error) {
	r, err := s.q.PaymentRequestByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Transfer{}, db.PaymentRequest{}, models.ErrPaymentRequestNotFound
		}
		return db.Transfer{}, db.PaymentRequest{}, err
	}
	if r.PayerUserID != u {
		return db.Transfer{}, r, models.ErrNotRequestPayer
	}
	// Already settled: hand back the linked transfer so a double-tap is a no-op.
	if r.Status == "paid" {
		if r.TransferID.Valid {
			if t, terr := s.TransferByID(ctx, u, asUUID(r.TransferID)); terr == nil {
				return t, r, nil
			}
		}
		return db.Transfer{}, r, models.ErrRequestNotPending
	}
	if r.Status != "pending" {
		return db.Transfer{}, r, models.ErrRequestNotPending
	}
	if r.ExpiresAt.Valid && r.ExpiresAt.Time.Before(time.Now()) {
		return db.Transfer{}, r, models.ErrRequestExpired
	}

	// One request maps to exactly one transfer, forever.
	key := "payreq:" + r.ID.String()

	// Reconcile a prior attempt whose transfer landed but whose paid-link did
	// not: return that transfer and finish the link. No second beneficiary is
	// minted and no second payment is attempted.
	if t, terr := s.q.TransferByKey(ctx, db.TransferByKeyParams{SenderUserID: u, IdempotencyKey: key}); terr == nil {
		if paid, perr := s.q.MarkPaymentRequestPaid(ctx, db.MarkPaymentRequestPaidParams{TransferID: pgUUIDOf(t.ID), ID: r.ID}); perr == nil {
			return t, paid, nil
		}
		return t, r, nil
	} else if !errors.Is(terr, pgx.ErrNoRows) {
		return db.Transfer{}, r, terr
	}

	benID, err := s.beneficiaryForPayment(ctx, u, r.RequesterUserID, r.Currency, requesterDisplayName(r))
	if err != nil {
		return db.Transfer{}, r, err
	}
	t, err := s.Transfer(ctx, u, TransferInput{
		BeneficiaryID:  benID,
		AmountMinor:    r.AmountMinor,
		Currency:       r.Currency,
		IdempotencyKey: key,
		PIN:            pin,
		Narrative:      r.Note.String,
	})
	if err != nil {
		return db.Transfer{}, r, err
	}
	paid, perr := s.q.MarkPaymentRequestPaid(ctx, db.MarkPaymentRequestPaidParams{TransferID: pgUUIDOf(t.ID), ID: r.ID})
	if perr != nil {
		// The money moved and the transfer is real; only the request-side link
		// lagged. Return success — a retry reconciles via the key above.
		return t, r, nil
	}
	return t, paid, nil
}

func (s *Service) DeclinePaymentRequest(ctx context.Context, u, id uuid.UUID, reason string) (db.PaymentRequest, error) {
	r, err := s.q.PaymentRequestByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PaymentRequest{}, models.ErrPaymentRequestNotFound
		}
		return db.PaymentRequest{}, err
	}
	if r.PayerUserID != u {
		return db.PaymentRequest{}, models.ErrNotRequestPayer
	}
	if r.Status != "pending" {
		return db.PaymentRequest{}, models.ErrRequestNotPending
	}
	out, err := s.q.SetPaymentRequestStatus(ctx, db.SetPaymentRequestStatusParams{
		Status: "declined", DeclineReason: pgText(strings.TrimSpace(reason)), ID: id,
	})
	if err != nil {
		// The pending guard lost a race (paid/cancelled between read and write).
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PaymentRequest{}, models.ErrRequestNotPending
		}
		return db.PaymentRequest{}, err
	}
	return out, nil
}

func (s *Service) CancelPaymentRequest(ctx context.Context, u, id uuid.UUID) (db.PaymentRequest, error) {
	r, err := s.q.PaymentRequestByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PaymentRequest{}, models.ErrPaymentRequestNotFound
		}
		return db.PaymentRequest{}, err
	}
	if r.RequesterUserID != u {
		return db.PaymentRequest{}, models.ErrNotRequestRequester
	}
	if r.Status != "pending" {
		return db.PaymentRequest{}, models.ErrRequestNotPending
	}
	out, err := s.q.SetPaymentRequestStatus(ctx, db.SetPaymentRequestStatusParams{
		Status: "cancelled", ID: id,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PaymentRequest{}, models.ErrRequestNotPending
		}
		return db.PaymentRequest{}, err
	}
	return out, nil
}

// beneficiaryForPayment returns a payee the payer can send to for the requester,
// reusing an existing internal payee when there is one and otherwise minting an
// EPHEMERAL (pay-once, hidden from the saved list) one. Reuse-first is what keeps
// a repeated pay from colliding with the unique (user,recipient) payee index and
// keeps request-payments from cluttering the payer's saved list.
func (s *Service) beneficiaryForPayment(ctx context.Context, payer, recipient uuid.UUID, cur, name string) (uuid.UUID, error) {
	existing, err := s.q.InternalBeneficiaryForRecipient(ctx, db.InternalBeneficiaryForRecipientParams{
		UserID: payer, RecipientUserID: pgUUIDOf(recipient),
	})
	if err == nil {
		return existing.ID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	save := false
	rid := recipient
	ben, err := s.AddBeneficiary(ctx, payer, BeneficiaryInput{
		Type:            "internal",
		RecipientUserID: &rid,
		AccountName:     name,
		Currency:        cur,
		Save:            &save,
	})
	if err != nil {
		// A concurrent pay may have minted it between our lookup and insert; fall
		// back to the now-existing row rather than failing the payment.
		if errors.Is(err, ErrBeneficiaryDuplicate) {
			if again, aerr := s.q.InternalBeneficiaryForRecipient(ctx, db.InternalBeneficiaryForRecipientParams{
				UserID: payer, RecipientUserID: pgUUIDOf(recipient),
			}); aerr == nil {
				return again.ID, nil
			}
		}
		return uuid.Nil, err
	}
	return ben.ID, nil
}

// requesterDisplayName is the non-empty name to stamp on a minted payee: the
// requester's captured real name, else their @handle, else a neutral fallback so
// the payee's NOT NULL account_name is never empty.
func requesterDisplayName(r db.PaymentRequest) string {
	if n := strings.TrimSpace(r.RequesterName.String); n != "" {
		return n
	}
	if h := strings.TrimSpace(r.RequesterUsername.String); h != "" {
		return "@" + h
	}
	return "Nablr user"
}
