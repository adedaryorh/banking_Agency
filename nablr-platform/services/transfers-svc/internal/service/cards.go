package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"nabla/transfers-svc/internal/models"
	"nabla/transfers-svc/internal/platform/clock"
	"nabla/transfers-svc/internal/platform/id"
	"nabla/transfers-svc/internal/providers"
)

var (
	ErrCardNotFound  = errors.New("cards: not found")
	ErrCardNotActive = errors.New("cards: card is not active")
	ErrAuthNotFound  = errors.New("cards: authorisation not found")

	ErrIdentityRequired  = errors.New("cards: a NIN or BVN is required to issue a card")
	ErrProfileIncomplete = errors.New("cards: the cardholder's details are incomplete")
	ErrAddressRequired   = errors.New("cards: a billing address is required to issue a card")

	ErrCardLimit = errors.New("cards: one card of this type already exists")

	ErrCurrencyUnsupported = errors.New("cards: cards are issued in naira only")

	ErrDeliveryRequired = errors.New("cards: a physical card needs a delivery address")

	ErrNotPhysical  = errors.New("cards: delivery is for physical cards")
	ErrDeliveryOpen = errors.New("cards: a delivery is already on its way")

	// ErrIssuerNotConfigured: no card rail is wired into this deployment.
	ErrIssuerNotConfigured = errors.New("cards: no card issuer is configured")
)

// idNumberPattern is the shape of a Nigerian NIN or BVN.
var idNumberPattern = regexp.MustCompile(`^[0-9]{11}$`)

// dobPattern is the date of birth as an issuer wants it written.
var dobPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

const authHoldTTL = 7 * 24 * time.Hour

const deliveryWorkingDays = 28

const (
	virtualCardFeeMinor  = 100_00   // ₦100, and it exists the same minute
	physicalCardFeeMinor = 2_000_00 // ₦2,000, and it arrives in about a month
)

// CardCurrency is the only currency a card is issued and priced in.
const CardCurrency = "NGN"

// CardFeeMinor is what this kind of card costs.
func CardFeeMinor(cardType string) int64 {
	if cardType == "physical" {
		return physicalCardFeeMinor
	}
	return virtualCardFeeMinor
}

// CardsService is the card rail: issuing, controls, and the authorisation
// decision.
type CardsService struct {
	db       DB
	issuer   providers.CardIssuer
	ledger   *LedgerService
	wallets  *WalletService
	controls *ControlsService
	limits   *LimitsService
	clock    clock.Clock
	identity IdentityClient

	// preferred is the real issuer, used only for the customers named by
	// allowed. Everyone else keeps `issuer`, which is what they have today.
	//
	// A card rollout begins with somebody, not with everybody: a real card
	// issued to a customer nobody expected to give one to is much harder to
	// take back than it is to withhold.
	preferred providers.CardIssuer
	allowed   func(ctx context.Context, customerID uuid.UUID) bool
}

func NewCardsService(db DB, issuer providers.CardIssuer, ledger *LedgerService,
	wallets *WalletService, ctrl *ControlsService, limits *LimitsService,
	clk clock.Clock) *CardsService {
	return &CardsService{
		db: db, issuer: issuer, ledger: ledger, wallets: wallets,
		controls: ctrl, limits: limits, clock: clk,
	}
}

// WithIdentity supplies the identity-svc client the cardholder is assembled
// from. Optional: an issuer that asks for no KYC of its own (the mock) needs
// none of it, and a nil client is only a failure when a real issuer does.
func (s *CardsService) WithIdentity(c IdentityClient) { s.identity = c }

// SetPreferredIssuer names the real issuer and who may have it. A nil issuer
// or a nil predicate leaves every customer on the existing one.
func (s *CardsService) SetPreferredIssuer(issuer providers.CardIssuer,
	allowed func(ctx context.Context, customerID uuid.UUID) bool) {
	s.preferred, s.allowed = issuer, allowed
}

// issuerFor is the issuer this customer's card comes from.
func (s *CardsService) issuerFor(ctx context.Context, customerID uuid.UUID) providers.CardIssuer {
	if s.preferred != nil && s.allowed != nil && s.allowed(ctx, customerID) {
		return s.preferred
	}
	return s.issuer
}

// DefaultIssuerName is the rail a card is issued on when the customer is not
// on the real-issuer allow list. It is NOT the right value to look an existing
// authorisation up by — see providerOfCard.
func (s *CardsService) DefaultIssuerName() string {
	if s.issuer == nil {
		return ""
	}
	return s.issuer.Info().Name
}

// Every read and write of card_authorisations is keyed on the rail that
// actually issued THAT card, read from the cards row — see Authorize, which
// loads it before anything else, and SettleCard/RefundCard, which take it from
// the webhook endpoint that received the advice.
//
// The original this was ported from used the service's default issuer instead,
// which is wrong the moment a rollout is gated: a card issued on the real rail
// would have its authorisations recorded — and then looked up — under the
// mock's name, so the settlement webhook would find nothing to settle and the
// customer's money would stay held until the hold expired.

// Card is one card as the customer sees it.
type Card struct {
	ID          uuid.UUID `json:"id"`
	WalletID    uuid.UUID `json:"wallet_id"`
	Type        string    `json:"type"`
	Brand       string    `json:"brand"`
	Currency    string    `json:"currency"`
	Last4       string    `json:"last4"`
	ExpiryMonth int16     `json:"expiry_month"`
	ExpiryYear  int16     `json:"expiry_year"`
	Label       string    `json:"label"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	// Physical cards only. A physical card works the moment it is issued —
	// these say where the plastic is, not whether the card can be used.
	DeliveryStatus    string     `json:"delivery_status,omitempty"`
	DeliveryTracking  string     `json:"delivery_tracking_ref,omitempty"`
	DeliveryEstimated *time.Time `json:"delivery_estimated_at,omitempty"`
}

// Delivery is the address a physical card is couriered to. Everything here is
// typed by the customer; nothing is inferred from their profile.
type Delivery struct {
	Line1 string
	City  string
	State string
	Phone string
}

// BillingAddress is the address the issuer prints against the cardholder.
//
// Supplied on the request rather than read from a table, because transfers-svc
// holds no addresses: identity-svc does, and a card is not a reason for the
// money service to start keeping a copy.
type BillingAddress struct {
	Line1      string
	City       string
	State      string
	PostalCode string
	Country    string
}

// IssueInput is everything a card needs.
//
// A struct rather than eight positional arguments because the cardholder's
// identity joined the list, and an identity number in the wrong position is
// not the sort of mistake to leave to argument order.
type IssueInput struct {
	CustomerID uuid.UUID
	WalletID   uuid.UUID
	CardType   string
	Label      string
	NameOnCard string
	Delivery   *Delivery

	// Identity is the national number the ISSUER needs to satisfy its own KYC.
	//
	// It is passed through and never stored. This platform verifies a NIN and
	// then deliberately throws the number away, and issuing a card is not a
	// reason to start keeping it. So the customer supplies it at the moment
	// they ask for the card, it travels to the issuer, and it stops there.
	IdentityType   string // nin | bvn
	IdentityNumber string

	// DateOfBirth (yyyy-mm-dd) and BillingAddress travel the same way and for
	// the same reason: the issuer needs them, this service does not hold them,
	// and nothing here writes them down.
	DateOfBirth    string
	BillingAddress *BillingAddress
}

// Issue creates a card against a wallet through the issuer port.
func (s *CardsService) Issue(ctx context.Context, in IssueInput) (*Card, error) {
	if s.issuer == nil {
		return nil, ErrIssuerNotConfigured
	}

	customerID, walletID := in.CustomerID, in.WalletID
	cardType, label, nameOnCard, delivery := in.CardType, in.Label, in.NameOnCard, in.Delivery
	if cardType != "virtual" && cardType != "physical" {
		cardType = "virtual"
	}

	// The limit is enforced here, where every path to a new card passes,
	// rather than in the screen that happens to ask for one.
	var live int
	if err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM cards
		 WHERE customer_id = $1 AND card_type = $2 AND status <> 'terminated'`,
		customerID, cardType).Scan(&live); err != nil {
		return nil, err
	}
	if live > 0 {
		return nil, ErrCardLimit
	}

	if cardType == "physical" {
		if delivery == nil || delivery.Line1 == "" || delivery.City == "" ||
			delivery.State == "" || delivery.Phone == "" {
			return nil, ErrDeliveryRequired
		}
	}

	wallet, err := s.wallets.WalletByID(ctx, walletID, customerID)
	if err != nil {
		return nil, err
	}

	// A card is a naira card. Refused rather than issued against a wallet it
	// cannot be priced or settled in — the alternative is a card whose fee,
	// limits and settlement are each in a different money. Checked BEFORE the
	// issuer is called, so nobody's card is created and then refused.
	if wallet.Currency != CardCurrency {
		return nil, ErrCurrencyUnsupported
	}

	issuer := s.issuerFor(ctx, customerID)

	cardholder, err := s.cardholder(ctx, customerID, in, delivery, issuer)
	if err != nil {
		return nil, err
	}

	cardID := id.New()
	issued, err := issuer.IssueCard(ctx, providers.IssueCardRequest{
		IdempotencyKey: cardID.String(),
		CardType:       cardType,
		Currency:       wallet.Currency,
		NameOnCard:     nameOnCard,
		Cardholder:     cardholder,
	})
	if err != nil {
		return nil, err
	}

	// Remember who the issuer thinks this person is, so a second card does not
	// create a second cardholder. Best effort: the card exists either way, and
	// failing the issue over a bookkeeping row would be the wrong trade.
	if issued.ProviderCustomerID != "" && issued.ProviderCustomerID != cardholder.ProviderCustomerID {
		if _, uerr := s.db.Exec(ctx, `
			INSERT INTO card_cardholders (customer_id, provider_name, provider_customer_id)
			VALUES ($1, $2, $3)
			ON CONFLICT (customer_id, provider_name)
			DO UPDATE SET provider_customer_id = EXCLUDED.provider_customer_id,
			              updated_at = now()`,
			customerID, issuer.Info().Name, issued.ProviderCustomerID); uerr != nil {
			log.Printf("cards: cardholder id not recorded for customer %s: %v", customerID, uerr)
		}
	}

	if label == "" {
		if cardType == "physical" {
			label = "Physical card"
		} else {
			label = "Virtual card"
		}
	}

	// A physical card is live from the moment it is issued — the plastic is a
	// second way to use a card that already works, so the delivery state is
	// recorded alongside it rather than gating it.
	deliveryStatus := "not_applicable"
	var tracking string
	var estimated *time.Time
	if cardType == "physical" {
		deliveryStatus = "requested"
		// Derived from the id's random tail, not its timestamp prefix: two
		// cards issued in the same millisecond would otherwise share a
		// tracking number.
		tracking = "NBL" + strings.ToUpper(strings.ReplaceAll(cardID.String()[24:], "-", ""))
		eta := s.clock.Now().AddDate(0, 0, deliveryWorkingDays)
		estimated = &eta
	}

	feeMinor := CardFeeMinor(cardType)

	err = s.inTx(ctx, func(tx pgx.Tx) error {
		// The fee, in the same transaction as the card. A card without its fee
		// is one somebody got free; a fee without a card is money taken for
		// nothing. Neither can happen if they commit together.
		//
		// Posted after the issuer has already said yes, so nobody is charged
		// for a card the issuer refused.
		if err := s.chargeIssueFee(ctx, tx, customerID, walletID, wallet.LedgerAccountID,
			cardID, cardType, feeMinor, wallet.Currency); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO cards
				(id, customer_id, wallet_id, card_type, card_purpose, brand, currency,
				 provider_name, provider_card_id, provider_token, last4,
				 expiry_month, expiry_year, name_on_card, label, status, activated_at,
				 delivery_status, delivery_tracking_ref, delivery_estimated_at)
			VALUES ($1, $2, $3, $14, 'personal', $4, $5, $6, $7, $8, $9,
			        $10, $11, NULLIF($12, ''), $13, 'active', now(), $15, NULLIF($16, ''), $17)`,
			cardID, customerID, walletID, nullIfEmpty(issued.Brand), wallet.Currency,
			issuer.Info().Name, issued.ProviderCardID, issued.ProviderToken,
			nullIfEmpty(issued.Last4), int16(issued.ExpiryMonth), int16(issued.ExpiryYear),
			nameOnCard, label, cardType, deliveryStatus, tracking, estimated); err != nil {
			return err
		}
		if cardType == "physical" && delivery != nil {
			if _, err := tx.Exec(ctx, `
				INSERT INTO card_delivery_requests
					(card_id, customer_id, address_line1, city, state, phone)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				cardID, customerID, delivery.Line1, delivery.City,
				delivery.State, delivery.Phone); err != nil {
				return err
			}
		}
		// Default controls: online on, international off — conservative until
		// the customer opts in.
		_, err := tx.Exec(ctx, `INSERT INTO card_controls (card_id) VALUES ($1)`, cardID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.CardByID(ctx, cardID, customerID)
}

// nullIfEmpty keeps an empty issuer field out of a column with a CHECK on it:
// brand and last4 both accept NULL and reject "".
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

const cardColumns = `
	id, wallet_id, card_type, coalesce(brand, ''), currency, coalesce(last4, ''),
	coalesce(expiry_month, 0), coalesce(expiry_year, 0), label, status, created_at,
	coalesce(delivery_status, ''), coalesce(delivery_tracking_ref, ''),
	delivery_estimated_at`

func scanCard(row interface{ Scan(...any) error }) (*Card, error) {
	var c Card
	err := row.Scan(&c.ID, &c.WalletID, &c.Type, &c.Brand, &c.Currency, &c.Last4,
		&c.ExpiryMonth, &c.ExpiryYear, &c.Label, &c.Status, &c.CreatedAt,
		&c.DeliveryStatus, &c.DeliveryTracking, &c.DeliveryEstimated)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCardNotFound
		}
		return nil, err
	}
	return &c, nil
}

// CardByID reads one card. Ownership is in the query, as everywhere.
func (s *CardsService) CardByID(ctx context.Context, cardID, customerID uuid.UUID) (*Card, error) {
	return scanCard(s.db.QueryRow(ctx, `
		SELECT `+cardColumns+` FROM cards
		 WHERE id = $1 AND customer_id = $2`, cardID, customerID))
}

// Cards lists the customer's live cards. Terminated cards are gone from the
// list; their transactions remain queryable history.
func (s *CardsService) Cards(ctx context.Context, customerID uuid.UUID) ([]Card, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+cardColumns+` FROM cards
		 WHERE customer_id = $1 AND status <> 'terminated'
		 ORDER BY created_at`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Card{}
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// Reveal fetches the card's own numbers from the issuer for display.
//
// The caller must have completed step-up authentication; nothing here is
// written down, logged or cached, and the values are handed to exactly one
// response.
func (s *CardsService) Reveal(ctx context.Context, cardID, customerID uuid.UUID) (*providers.CardSecrets, error) {
	if s.issuer == nil {
		return nil, ErrIssuerNotConfigured
	}
	var providerCardID, status string
	err := s.db.QueryRow(ctx,
		`SELECT provider_card_id, status FROM cards WHERE id = $1 AND customer_id = $2`,
		cardID, customerID).Scan(&providerCardID, &status)
	if err != nil {
		return nil, ErrCardNotFound
	}
	if status == "terminated" {
		return nil, ErrCardNotActive
	}
	return s.issuerFor(ctx, customerID).RevealCard(ctx, providerCardID)
}

// SetCardFrozen freezes or unfreezes, pushing the state to the issuer FIRST —
// if the issuer cannot be told, the card stays in its current state locally
// rather than diverging (a card the customer believes frozen must be frozen).
func (s *CardsService) SetCardFrozen(ctx context.Context, cardID, customerID, byUserID uuid.UUID,
	freeze bool) (*Card, error) {

	card, err := s.CardByID(ctx, cardID, customerID)
	if err != nil {
		return nil, err
	}
	if card.Status != "active" && card.Status != "frozen" {
		return nil, ErrCardNotActive
	}

	var providerCardID string
	if err := s.db.QueryRow(ctx,
		`SELECT provider_card_id FROM cards WHERE id = $1`, cardID).Scan(&providerCardID); err != nil {
		return nil, err
	}

	state, event := "active", "unfrozen"
	if freeze {
		state, event = "frozen", "frozen"
	}
	if err := s.issuerFor(ctx, customerID).SetCardState(ctx, providerCardID, state); err != nil {
		return nil, err
	}

	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE cards SET status = $2,
			       frozen_at = CASE WHEN $2 = 'frozen' THEN now() ELSE NULL END
			 WHERE id = $1`, cardID, state); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO card_events (card_id, event_type, actor_user_id, actor_type)
			VALUES ($1, $2, $3, 'user')`, cardID, event, byUserID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.CardByID(ctx, cardID, customerID)
}

// TerminateCard permanently closes a card: issuer first (a card the customer
// believes dead must be dead at the network), then our record.
func (s *CardsService) TerminateCard(ctx context.Context, cardID, customerID, byUserID uuid.UUID) error {
	card, err := s.CardByID(ctx, cardID, customerID)
	if err != nil {
		return err
	}
	if card.Status == "terminated" {
		return nil // already done; deleting twice is not an error
	}

	var providerCardID string
	if err := s.db.QueryRow(ctx,
		`SELECT provider_card_id FROM cards WHERE id = $1`, cardID).Scan(&providerCardID); err != nil {
		return err
	}
	if err := s.issuerFor(ctx, customerID).SetCardState(ctx, providerCardID, "terminated"); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE cards SET status = 'terminated', terminated_at = now(),
			       status_reason = 'customer_closed'
			 WHERE id = $1`, cardID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO card_events (card_id, event_type, actor_user_id, actor_type)
			VALUES ($1, 'terminated', $2, 'user')`, cardID, byUserID)
		return err
	})
}

// CloseByProviderCard marks a card closed because the issuer said so.
//
// The issuer can terminate a card without us asking — expiry, a scheme
// instruction, fraud on their side — and a card the issuer has stopped must
// not still look usable in the app.
func (s *CardsService) CloseByProviderCard(ctx context.Context, providerCardID, reason string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE cards
		   SET status = 'terminated', status_reason = $2, terminated_at = now()
		 WHERE provider_card_id = $1 AND status <> 'terminated'`,
		providerCardID, reason)
	// Zero rows means unknown to us or already closed. Both are fine to hear
	// twice: a webhook is delivered more than once.
	return err
}

// CardIDForProvider maps the issuer's card id onto ours.
func (s *CardsService) CardIDForProvider(ctx context.Context, providerCardID string) (uuid.UUID, error) {
	var out uuid.UUID
	err := s.db.QueryRow(ctx,
		`SELECT id FROM cards WHERE provider_card_id = $1`, providerCardID).Scan(&out)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrCardNotFound
		}
		return uuid.Nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Authorisation — the issuer-callback decision path.
// ---------------------------------------------------------------------------

type AuthRequest struct {
	ProviderAuthID  string
	CardID          uuid.UUID
	AmountMinor     int64
	Currency        string
	MerchantName    string
	MerchantMCC     string
	EntryMode       string // ecommerce | contactless | chip | atm
	IsInternational bool
}

type AuthDecision struct {
	AuthorisationID uuid.UUID
	Approved        bool
	DeclineReason   string
}

// Authorize decides one card authorisation. Approval places a 7-day ledger
// hold; declines record the precise reason. Idempotent on ProviderAuthID.
func (s *CardsService) Authorize(ctx context.Context, req AuthRequest) (*AuthDecision, error) {
	// Load the card with its wallet and controls FIRST. The card names the
	// rail, and the rail is half the key every authorisation is recorded and
	// replayed under, so nothing can be looked up before it is known.
	var (
		customerID, walletID, ledgerAcct                                          uuid.UUID
		cardStatus, currency, provider                                            string
		allowOnline, allowContactless, allowATM, allowInternational, applyEthical bool
		perTxnLimit, dailyLimit                                                   *int64
	)
	err := s.db.QueryRow(ctx, `
		SELECT c.customer_id, c.wallet_id, w.ledger_account_id, c.status, c.currency,
		       c.provider_name,
		       cc.allow_online, cc.allow_contactless, cc.allow_atm,
		       cc.allow_international, cc.apply_ethical_controls,
		       cc.per_transaction_limit_minor, cc.daily_limit_minor
		  FROM cards c
		  JOIN wallets w ON w.id = c.wallet_id
		  JOIN card_controls cc ON cc.card_id = c.id
		 WHERE c.id = $1`, req.CardID).
		Scan(&customerID, &walletID, &ledgerAcct, &cardStatus, &currency, &provider,
			&allowOnline, &allowContactless, &allowATM, &allowInternational,
			&applyEthical, &perTxnLimit, &dailyLimit)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCardNotFound
		}
		return nil, err
	}

	// Replay: return the recorded decision rather than deciding again. A
	// second delivery of the same authorisation must not place a second hold.
	var existingID uuid.UUID
	var existingDecision, existingReason string
	err = s.db.QueryRow(ctx, `
		SELECT id, decision, coalesce(decline_reason, '') FROM card_authorisations
		 WHERE provider_name = $1 AND provider_auth_id = $2`,
		provider, req.ProviderAuthID).
		Scan(&existingID, &existingDecision, &existingReason)
	if err == nil {
		return &AuthDecision{AuthorisationID: existingID,
			Approved: existingDecision == "approved", DeclineReason: existingReason}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	decline := func(reason string, controlID uuid.UUID) (*AuthDecision, error) {
		authID, err := s.recordAuth(ctx, id.New(), provider, req, customerID, walletID,
			"declined", reason, controlID, uuid.Nil)
		if err != nil {
			return nil, err
		}
		return &AuthDecision{AuthorisationID: authID, Approved: false, DeclineReason: reason}, nil
	}

	// 1. Card state.
	switch cardStatus {
	case "active":
	case "frozen":
		return decline("card_frozen", uuid.Nil)
	default:
		return decline("card_inactive", uuid.Nil)
	}
	if req.Currency != currency {
		return decline("issuer_declined", uuid.Nil)
	}

	// 2. Card controls.
	switch {
	case req.EntryMode == "ecommerce" && !allowOnline:
		return decline("control_blocked", uuid.Nil)
	case req.EntryMode == "contactless" && !allowContactless:
		return decline("control_blocked", uuid.Nil)
	case req.EntryMode == "atm" && !allowATM:
		return decline("control_blocked", uuid.Nil)
	case req.IsInternational && !allowInternational:
		return decline("country_blocked", uuid.Nil)
	case perTxnLimit != nil && req.AmountMinor > *perTxnLimit:
		return decline("limit_exceeded", uuid.Nil)
	}
	if dailyLimit != nil {
		var spentToday int64
		if err := s.db.QueryRow(ctx, `
			SELECT coalesce(sum(amount_minor), 0) FROM card_authorisations
			 WHERE card_id = $1 AND decision = 'approved'
			   AND authorised_at >= date_trunc('day', now())`, req.CardID).
			Scan(&spentToday); err != nil {
			return nil, err
		}
		if spentToday+req.AmountMinor > *dailyLimit {
			return decline("limit_exceeded", uuid.Nil)
		}
	}

	// 3. The customer's own ethical controls.
	if applyEthical && s.controls != nil {
		d, err := s.controls.Evaluate(ctx, customerID, req.MerchantMCC,
			req.MerchantName, req.AmountMinor, currency)
		if err != nil {
			return nil, err
		}
		if d.Outcome == "block" {
			return decline("ethical_control_blocked", d.ControlID)
		}
	}

	// 4. The customer's verification ceiling, which is not the card's.
	//
	// card_controls carries per-card limits somebody set for themselves; this
	// is the tier ceiling every rail answers to. Without it a customer at
	// their daily limit could keep spending on the card, and card spend would
	// consume none of the allowance the other rails share.
	//
	// It costs two queries inside the authorisation budget. That is the price
	// of the ceiling meaning anything on this rail, and both are indexed.
	if s.limits != nil {
		switch err := s.limits.CheckOutbound(ctx, customerID, req.AmountMinor, currency); {
		case err == nil:
		case errors.Is(err, ErrPerTransaction), errors.Is(err, ErrDailyOutbound):
			return decline("limit_exceeded", uuid.Nil)
		case errors.Is(err, ErrNotSet):
			// We do not know what this customer may spend. An unknown ceiling
			// is zero, never infinity — and it is not their fault, so the
			// reason says restriction rather than blaming their balance.
			log.Printf("cards: authorisation refused, no verification limits on record for customer %s", customerID)
			return decline("compliance_restriction", uuid.Nil)
		default:
			return nil, err
		}
	}

	// 5. Funds: place the authorisation hold. Insufficient available balance
	// is the ledger's call, not ours.
	authID := id.New()
	hold, err := s.ledger.PlaceHold(ctx, PlaceHoldInput{
		AccountID:   ledgerAcct,
		AmountMinor: req.AmountMinor,
		Currency:    currency,
		HoldType:    models.HoldCardAuthorisation,
		SourceType:  "card_authorisation",
		SourceID:    authID,
		TTL:         authHoldTTL,
	})
	if err != nil {
		if errors.Is(err, models.ErrInsufficientFunds) {
			return decline("insufficient_funds", uuid.Nil)
		}
		return nil, err
	}

	if _, err := s.recordAuth(ctx, authID, provider, req, customerID, walletID,
		"approved", "", uuid.Nil, hold.ID); err != nil {
		// Recording failed after reserving: release rather than strand funds.
		_ = s.ledger.ReleaseHold(ctx, hold.ID)
		return nil, err
	}
	return &AuthDecision{AuthorisationID: authID, Approved: true}, nil
}

func (s *CardsService) recordAuth(ctx context.Context, authID uuid.UUID, provider string,
	req AuthRequest, customerID, walletID uuid.UUID, decision, reason string,
	controlID, holdID uuid.UUID) (uuid.UUID, error) {

	// merchant_mcc carries a foreign key to merchant_categories: an MCC the
	// table does not know is recorded as none rather than failing the insert,
	// because a decline that cannot be written is a decline nobody can explain.
	var mcc *string
	if req.MerchantMCC != "" {
		var known bool
		_ = s.db.QueryRow(ctx,
			`SELECT true FROM merchant_categories WHERE mcc = $1`, req.MerchantMCC).Scan(&known)
		if known {
			mcc = &req.MerchantMCC
		}
	}
	var ctrl, hold *uuid.UUID
	if controlID != uuid.Nil {
		ctrl = &controlID
	}
	if holdID != uuid.Nil {
		hold = &holdID
	}
	status := "pending"
	if decision == "declined" {
		status = "declined"
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO card_authorisations
			(id, card_id, wallet_id, customer_id, provider_name, provider_auth_id,
			 amount_minor, currency, merchant_name, merchant_mcc, entry_mode,
			 is_international, decision, decline_reason, triggered_control_id,
			 hold_id, status, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, ''),
		        $12, $13, NULLIF($14, ''), $15, $16, $17, now() + interval '7 days')
		ON CONFLICT (provider_name, provider_auth_id) DO NOTHING`,
		authID, req.CardID, walletID, customerID,
		provider, req.ProviderAuthID,
		req.AmountMinor, req.Currency, req.MerchantName, mcc,
		req.EntryMode, req.IsInternational, decision, reason, ctrl, hold, status)
	return authID, err
}

// ---------------------------------------------------------------------------
// Settlement
// ---------------------------------------------------------------------------

// SettleCard clears an approved authorisation for the final amount
// (≤ authorised; partial settlement is the card-scheme norm). Captures the
// hold, posts the journal to the issuer clearing account, records the card
// transaction and the customer's feed item — one database transaction.
//
// provider is the rail the settlement advice came from; it is half the key an
// authorisation is stored under, and the caller (a webhook endpoint) knows
// which issuer is calling it.
func (s *CardsService) SettleCard(ctx context.Context, provider, providerAuthID string,
	settledMinor int64, currency string) error {

	var (
		authID, cardID, walletID, customerID, holdID uuid.UUID
		authAmount                                   int64
		authCurrency, status, merchant               string
		mcc                                          *string
	)
	err := s.db.QueryRow(ctx, `
		SELECT id, card_id, wallet_id, customer_id,
		       coalesce(hold_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       amount_minor, currency, status, coalesce(merchant_name, ''), merchant_mcc
		  FROM card_authorisations
		 WHERE provider_name = $1 AND provider_auth_id = $2`,
		provider, providerAuthID).
		Scan(&authID, &cardID, &walletID, &customerID, &holdID,
			&authAmount, &authCurrency, &status, &merchant, &mcc)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAuthNotFound
		}
		return err
	}
	if status == "settled" {
		return nil // duplicate settlement advice
	}
	if status != "pending" {
		return fmt.Errorf("cards: cannot settle authorisation in state %s", status)
	}
	if currency != authCurrency || settledMinor <= 0 || settledMinor > authAmount {
		return fmt.Errorf("cards: settlement amount invalid")
	}

	var ledgerAcct uuid.UUID
	if err := s.db.QueryRow(ctx,
		`SELECT ledger_account_id FROM wallets WHERE id = $1`, walletID).Scan(&ledgerAcct); err != nil {
		return err
	}

	now := s.clock.Now()

	return s.inTx(ctx, func(tx pgx.Tx) error {
		clearing, err := s.ledger.OpenAccountInTx(ctx, tx, OpenAccountInput{
			Type: models.AccountProviderClearing, Currency: authCurrency,
			OwnerType: "provider", ProviderName: provider,
			Name: "card issuer clearing " + authCurrency,
			Code: fmt.Sprintf("provider_clearing:%s:card:%s", authCurrency, provider),
		})
		if err != nil {
			return err
		}

		txn, err := s.ledger.CaptureHoldInTx(ctx, tx, holdID, PostTransactionInput{
			Reference:   "cardsettle:" + authID.String(),
			Kind:        models.KindCardSettlement,
			Description: "Card payment: " + merchant,
			Entries: []EntryInput{
				{AccountID: ledgerAcct, Direction: models.Debit, AmountMinor: settledMinor, Currency: authCurrency},
				{AccountID: clearing.ID, Direction: models.Credit, AmountMinor: settledMinor, Currency: authCurrency},
			},
		})
		if err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE card_authorisations
			   SET status = 'settled', settled_amount_minor = $2, settled_at = $3
			 WHERE id = $1`, authID, settledMinor, now); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO card_transactions
				(card_id, authorisation_id, wallet_id, customer_id, provider_name,
				 provider_transaction_id, transaction_type, amount_minor, currency,
				 merchant_name, merchant_mcc, ledger_transaction_id, settled_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'purchase', $7, $8, $9, $10, $11, $12)`,
			cardID, authID, walletID, customerID, provider,
			"st_"+providerAuthID, settledMinor, authCurrency,
			merchant, mcc, txn.ID, now); err != nil {
			return err
		}

		after := balanceAfterFor(txn, ledgerAcct)
		feedID, err := s.wallets.RecordFeedItem(ctx, tx, FeedInput{
			WalletID: walletID, CustomerID: customerID,
			LedgerTransactionID: txn.ID, Direction: "out",
			AmountMinor: settledMinor, Currency: authCurrency, BalanceAfterMinor: after,
			TransactionType: "card_payment", CounterpartyName: merchant,
			CounterpartyType: "merchant", Description: merchant,
			SourceType: "card_authorisation", SourceID: authID, OccurredAt: now,
		})
		if err != nil {
			return err
		}
		return s.accrueRoundUp(ctx, tx, customerID, feedID, settledMinor, authCurrency)
	})
}

// balanceAfterFor picks this account's closing balance out of a posting.
func balanceAfterFor(txn *models.Transaction, accountID uuid.UUID) int64 {
	for _, e := range txn.Entries {
		if e.AccountID == accountID {
			return e.BalanceAfterMinor
		}
	}
	return 0
}

// accrueRoundUp records the spare change on a card payment against the
// customer's active round-up accrual (to the next whole unit, times the
// multiplier). Accrual only — the sweep job moves real money later, in one
// posting, so statements stay readable.
func (s *CardsService) accrueRoundUp(ctx context.Context, tx pgx.Tx,
	customerID, feedItemID uuid.UUID, spentMinor int64, currency string) error {

	var accrualID uuid.UUID
	var multiplier int16
	err := tx.QueryRow(ctx, `
		SELECT id, round_up_multiplier FROM round_up_accruals
		 WHERE customer_id = $1 AND currency = $2 AND is_active
		 LIMIT 1`, customerID, currency).Scan(&accrualID, &multiplier)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // no round-ups enabled
		}
		return err
	}

	remainder := spentMinor % 100
	if remainder == 0 {
		return nil
	}
	roundUp := (100 - remainder) * int64(multiplier)

	if _, err := tx.Exec(ctx, `
		INSERT INTO round_up_items (accrual_id, wallet_transaction_id, spend_amount_minor, round_up_minor)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (wallet_transaction_id) DO NOTHING`,
		accrualID, feedItemID, spentMinor, roundUp); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE round_up_accruals SET accrued_minor = accrued_minor + $2, updated_at = now()
		 WHERE id = $1`, accrualID, roundUp)
	return err
}

// RefundCard returns a settled card payment to the customer.
//
// The issuer tells us after the fact — a merchant refunded, or a chargeback
// went the customer's way — so this is not a decision, it is a movement we are
// being informed of. The money goes back by reversing the settlement's own
// ledger transaction rather than by posting a fresh pair of entries, so the two
// are visibly one event and the reversal cannot drift from what it undoes.
//
// Idempotent, because a webhook will be delivered more than once. A refund
// already reversed is a refund already done.
func (s *CardsService) RefundCard(ctx context.Context, provider, providerAuthID string,
	amountMinor int64, currency string) error {

	var (
		authID, cardID, walletID, customerID, settlementTxn uuid.UUID
		status, authCurrency, merchant                      string
		settledMinor                                        int64
	)
	err := s.db.QueryRow(ctx, `
		SELECT a.id, a.card_id, a.wallet_id, a.customer_id, a.status, a.currency,
		       coalesce(a.merchant_name, ''), a.settled_amount_minor,
		       coalesce(t.ledger_transaction_id, '00000000-0000-0000-0000-000000000000'::uuid)
		  FROM card_authorisations a
		  LEFT JOIN card_transactions t
		         ON t.authorisation_id = a.id AND t.transaction_type = 'purchase'
		 WHERE a.provider_name = $1 AND a.provider_auth_id = $2`,
		provider, providerAuthID).
		Scan(&authID, &cardID, &walletID, &customerID, &status, &authCurrency,
			&merchant, &settledMinor, &settlementTxn)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAuthNotFound
		}
		return err
	}

	if status == "reversed" {
		return nil // already refunded; the webhook was delivered twice
	}
	if status != "settled" || settlementTxn == uuid.Nil {
		// Nothing was captured, so there is nothing to give back. Releasing a
		// hold that is still open is expiry's job, not a refund's.
		return fmt.Errorf("cards: cannot refund an authorisation in state %s", status)
	}
	if currency != authCurrency {
		return fmt.Errorf("cards: refund currency does not match the payment")
	}
	if amountMinor <= 0 || amountMinor > settledMinor {
		// More than was taken is not a refund, and this is the one place that
		// would quietly create money.
		return fmt.Errorf("cards: refund amount invalid")
	}

	reference := "cardrefund:" + authID.String()
	now := s.clock.Now()

	return s.inTx(ctx, func(tx pgx.Tx) error {
		// A reversal already posted under this reference means a previous
		// attempt got this far and then failed writing the records below.
		// Carry on with what it posted rather than refusing forever.
		txn, err := s.ledger.transactionByReferenceInTx(ctx, tx, reference)
		if errors.Is(err, ErrLedgerTransactionNotFound) {
			txn, err = s.ledger.ReverseInTx(ctx, tx, settlementTxn, reference,
				"card refund: "+merchant)
		}
		if err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE card_authorisations
			   SET status = 'reversed', reversed_at = $2
			 WHERE id = $1 AND status <> 'reversed'`, authID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO card_transactions
				(card_id, authorisation_id, wallet_id, customer_id, provider_name,
				 provider_transaction_id, transaction_type, amount_minor, currency,
				 merchant_name, ledger_transaction_id, settled_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'refund', $7, $8, $9, $10, $11)
			ON CONFLICT DO NOTHING`,
			cardID, authID, walletID, customerID, provider,
			"rf_"+providerAuthID, amountMinor, authCurrency, merchant,
			txn.ID, now); err != nil {
			return err
		}

		var after int64
		for _, e := range txn.Entries {
			if e.Direction == models.Credit {
				after = e.BalanceAfterMinor
			}
		}
		_, err = s.wallets.RecordFeedItem(ctx, tx, FeedInput{
			WalletID: walletID, CustomerID: customerID,
			LedgerTransactionID: txn.ID, Direction: "in",
			AmountMinor: amountMinor, Currency: authCurrency, BalanceAfterMinor: after,
			TransactionType: "card_refund", CounterpartyName: merchant,
			CounterpartyType: "merchant", Description: "Refund from " + merchant,
			SourceType: "card_authorisation", SourceID: authID, OccurredAt: now,
		})
		return err
	})
}

// ---------------------------------------------------------------------------
// History and delivery
// ---------------------------------------------------------------------------

// CardTransaction is one settled movement on a specific card.
type CardTransaction struct {
	ID           uuid.UUID `json:"id"`
	Type         string    `json:"type"`
	AmountMinor  int64     `json:"amount_minor"`
	Currency     string    `json:"currency"`
	MerchantName string    `json:"merchant_name"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

// CardTransactions lists a card's settled activity, newest first. Ownership is
// in the query, as everywhere.
func (s *CardsService) CardTransactions(ctx context.Context, cardID, customerID uuid.UUID,
	limit int) ([]CardTransaction, error) {

	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, transaction_type, amount_minor, currency, merchant_name,
		       'settled', created_at
		  FROM card_transactions
		 WHERE card_id = $1 AND customer_id = $2
		 ORDER BY created_at DESC LIMIT $3`, cardID, customerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CardTransaction{}
	for rows.Next() {
		var t CardTransaction
		if err := rows.Scan(&t.ID, &t.Type, &t.AmountMinor, &t.Currency,
			&t.MerchantName, &t.Status, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RequestCardDelivery couriers a physical card to an address the customer
// typed. One open delivery per card: the unique index refuses a second.
func (s *CardsService) RequestCardDelivery(ctx context.Context, customerID, cardID uuid.UUID,
	line1, city, state, phone string) (string, error) {

	var cardType string
	err := s.db.QueryRow(ctx,
		`SELECT card_type FROM cards WHERE id = $1 AND customer_id = $2`,
		cardID, customerID).Scan(&cardType)
	if err != nil {
		return "", ErrCardNotFound
	}
	if cardType != "physical" {
		return "", ErrNotPhysical
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO card_delivery_requests (card_id, customer_id, address_line1, city, state, phone)
		VALUES ($1, $2, $3, $4, $5, $6)`, cardID, customerID, line1, city, state, phone)
	if err != nil {
		// The partial unique index is what enforces "one open delivery"; a
		// second request hits it rather than being counted in Go, so the rule
		// holds under concurrency.
		if isUniqueViolation(err, "idx_card_delivery_open") {
			return "", ErrDeliveryOpen
		}
		return "", err
	}
	return "requested", nil
}

// CardDeliveryStatus returns the latest delivery state for a card ("" when none).
func (s *CardsService) CardDeliveryStatus(ctx context.Context, customerID, cardID uuid.UUID) string {
	var st string
	_ = s.db.QueryRow(ctx, `
		SELECT status FROM card_delivery_requests
		 WHERE card_id = $1 AND customer_id = $2 ORDER BY created_at DESC LIMIT 1`,
		cardID, customerID).Scan(&st)
	return st
}

// ---------------------------------------------------------------------------
// The cardholder
// ---------------------------------------------------------------------------

// cardholder assembles who the issuer is being asked to print a card for.
//
// The legal name, phone and email come from identity-svc, which owns them. The
// identity number, date of birth and address come from the request, because
// this service holds none of the three and issuing a card is not a reason to
// start.
//
// A missing piece is returned as an error rather than sent as an empty string.
// An issuer that rejects a half-formed cardholder does so after a round trip
// and in its own words; catching it here says which field, in ours.
func (s *CardsService) cardholder(ctx context.Context, customerID uuid.UUID,
	in IssueInput, delivery *Delivery, issuer providers.CardIssuer) (providers.Cardholder, error) {

	var ch providers.Cardholder

	// Whether a cardholder is needed at all is the ISSUER's question. An
	// issuer that prints cards without holding its own KYC — the mock, and
	// every environment with no real issuer configured — is asked for nothing,
	// and nothing is read for it: a national identity number should not be
	// gathered for a provider that never wanted one.
	if !providers.IssuerNeedsCardholderKYC(issuer) {
		return ch, nil
	}

	// The issuer's own id for this person, if we have already made one.
	var existing string
	err := s.db.QueryRow(ctx, `
		SELECT provider_customer_id FROM card_cardholders
		 WHERE customer_id = $1 AND provider_name = $2`,
		customerID, issuer.Info().Name).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ch, err
	}
	ch.ProviderCustomerID = existing

	// The identity number, straight through from the request.
	ch.IdentityType = strings.ToLower(strings.TrimSpace(in.IdentityType))
	ch.IdentityNumber = strings.TrimSpace(in.IdentityNumber)

	// Already known to the issuer: nothing more is needed, and nothing more
	// should be sent. Re-supplying an identity for a cardholder that exists is
	// personal data travelling for no reason.
	if ch.ProviderCustomerID != "" {
		return ch, nil
	}

	if ch.IdentityType != "nin" && ch.IdentityType != "bvn" {
		return ch, ErrIdentityRequired
	}
	if !idNumberPattern.MatchString(ch.IdentityNumber) {
		return ch, ErrIdentityRequired
	}

	if s.identity == nil {
		// A real issuer needs a real name for a real person, and the only
		// place that holds one is identity-svc. Without it the card is
		// refused rather than issued to a blank.
		return ch, ErrProfileIncomplete
	}
	kyc, err := s.identity.GetKYCProfile(ctx, customerID)
	if err != nil {
		return ch, err
	}
	ch.FirstName, ch.LastName = kyc.FirstName, kyc.LastName
	ch.Phone = kyc.PhoneNumber
	if user, uerr := s.identity.GetUser(ctx, customerID); uerr == nil {
		ch.Email = user.Email
		if ch.Phone == "" {
			ch.Phone = user.PhoneNumber
		}
	}
	if ch.FirstName == "" || ch.LastName == "" || ch.Phone == "" {
		return ch, ErrProfileIncomplete
	}

	ch.DateOfBirth = strings.TrimSpace(in.DateOfBirth)
	if !dobPattern.MatchString(ch.DateOfBirth) {
		return ch, ErrProfileIncomplete
	}

	ch.Address = billingAddressFor(in.BillingAddress, delivery)
	if ch.Address.Line1 == "" || ch.Address.City == "" {
		return ch, ErrAddressRequired
	}
	return ch, nil
}

// billingAddressFor prefers the address the customer gave for billing, and
// falls back to where the card is being couriered. A physical card already
// carries somewhere to deliver it, and that is the address the cardholder
// would expect to see on the account.
func billingAddressFor(billing *BillingAddress, delivery *Delivery) providers.CardholderAddress {
	if billing != nil && billing.Line1 != "" {
		country := billing.Country
		if country == "" {
			country = "Nigeria"
		}
		return providers.CardholderAddress{
			Line1: billing.Line1, City: billing.City, State: billing.State,
			PostalCode: billing.PostalCode, Country: country,
		}
	}
	if delivery != nil && delivery.Line1 != "" {
		return providers.CardholderAddress{
			Line1: delivery.Line1, City: delivery.City, State: delivery.State,
			Country: "Nigeria",
		}
	}
	return providers.CardholderAddress{}
}

// ---------------------------------------------------------------------------
// The issue fee
// ---------------------------------------------------------------------------

// chargeIssueFee takes what the card costs out of the customer's wallet.
//
// Insufficient balance refuses the card rather than issuing one and leaving a
// debt: this platform has no concept of owing us money, and inventing one for
// a card fee would be the wrong place to start. The ledger's own debit is what
// refuses it — the whole transaction rolls back, so no card is written either.
func (s *CardsService) chargeIssueFee(ctx context.Context, tx pgx.Tx,
	customerID, walletID, ledgerAcct, cardID uuid.UUID, cardType string,
	feeMinor int64, currency string) error {

	if feeMinor <= 0 {
		return nil
	}

	fees, err := s.ledger.OpenAccountInTx(ctx, tx, OpenAccountInput{
		Type: models.AccountFees, Currency: currency,
		OwnerType: "platform",
		Name:      "card fees " + currency,
		Code:      "fees:card:" + currency,
	})
	if err != nil {
		return err
	}

	what := "Virtual card"
	if cardType == "physical" {
		what = "Physical card"
	}

	txn, err := s.ledger.PostTransaction(ctx, tx, PostTransactionInput{
		Reference:   "cardfee:" + cardID.String(),
		Kind:        models.KindFee,
		Description: what + " fee",
		Entries: []EntryInput{
			{AccountID: ledgerAcct, Direction: models.Debit, AmountMinor: feeMinor, Currency: currency},
			{AccountID: fees.ID, Direction: models.Credit, AmountMinor: feeMinor, Currency: currency},
		},
	})
	if err != nil {
		return err
	}

	_, err = s.wallets.RecordFeedItem(ctx, tx, FeedInput{
		WalletID: walletID, CustomerID: customerID,
		LedgerTransactionID: txn.ID, Direction: "out",
		AmountMinor: feeMinor, Currency: currency,
		BalanceAfterMinor: balanceAfterFor(txn, ledgerAcct),
		TransactionType:   "fee", CounterpartyName: "Nablr",
		CounterpartyType: "platform", Description: what + " fee",
		SourceType: "card", SourceID: cardID, OccurredAt: s.clock.Now(),
	})
	return err
}

// ---------------------------------------------------------------------------
// Plumbing
// ---------------------------------------------------------------------------

// inTx runs fn inside one database transaction, rolling back on any error.
//
// Rollback uses a background context deliberately: a caller whose context was
// cancelled mid-flight still needs the transaction ended, and rolling back on
// a dead context leaves the connection holding locks.
func (s *CardsService) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
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

// isUniqueViolation reports whether err came from hitting the named unique
// constraint/index, so a caller can turn a race that the database already
// rejected into a domain error instead of a 500.
func isUniqueViolation(err error, constraint string) bool {
	return err != nil && strings.Contains(err.Error(), constraint)
}
