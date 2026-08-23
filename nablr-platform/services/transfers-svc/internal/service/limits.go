package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"nabla/transfers-svc/internal/platform/outbox"
)

var (
	// ErrPerTransaction: this single payment is larger than the tier allows
	ErrPerTransaction = errors.New("limits: over the per-transaction ceiling")
	// ErrDailyOutbound: this payment would take today's total past the tier's daily ceiling
	ErrDailyOutbound = errors.New("limits: over today's outbound ceiling")
	// ErrNotSet: no ceiling is on record for this customer and currency
	ErrNotSet = errors.New("limits: no verification limits on record")
)

type LimitsService struct {
	db DB
}

func NewLimitsService(db DB) *LimitsService {
	return &LimitsService{db: db}
}

// CheckOutbound refuses a payment that breaks either ceiling
func (s *LimitsService) CheckOutbound(ctx context.Context, customerID uuid.UUID, amountMinor int64, currency string) error {
	perTxn, daily, err := s.ceilings(ctx, customerID, currency)
	if err != nil {
		return err
	}

	if amountMinor > perTxn {
		return ErrPerTransaction
	}

	spent, err := s.OutboundToday(ctx, customerID, currency)
	if err != nil {
		return err
	}
	if spent+amountMinor > daily {
		return ErrDailyOutbound
	}
	return nil
}

// ceilings reads the two limits in force
func (s *LimitsService) ceilings(ctx context.Context, customerID uuid.UUID, currency string) (perTxn, daily int64, err error) {
	rows, qerr := s.db.Query(ctx, `
		SELECT limit_type, amount_minor
		  FROM customer_limits
		 WHERE user_id = $1 AND currency = $2
		   AND effective_to IS NULL AND amount_minor IS NOT NULL
		   AND limit_type IN ('per_transaction', 'daily_outbound')`,
		customerID, currency)
	if qerr != nil {
		return 0, 0, qerr
	}
	defer rows.Close()

	found := make(map[string]int64, 2)
	for rows.Next() {
		var typ string
		var amt int64
		if err := rows.Scan(&typ, &amt); err != nil {
			return 0, 0, err
		}
		found[typ] = amt
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	perTxn, okTxn := found["per_transaction"]
	daily, okDaily := found["daily_outbound"]
	if !okTxn || !okDaily {
		return 0, 0, ErrNotSet
	}
	return perTxn, daily, nil
}

// OutboundToday is everything this customer has committed to spending today,
// in minor units of the given currency. See the package comment for why it
// reads three tables.
func (s *LimitsService) OutboundToday(ctx context.Context, customerID uuid.UUID, currency string) (int64, error) {
	var total int64
	err := s.db.QueryRow(ctx, outboundQuery,
		customerID, currency).Scan(&total)
	return total, err
}

// outboundQuery is named rather than inlined so a test can read it. It IS the
// daily ceiling: the sources and exclusions in it are each load-bearing, and a
// silent edit to any of them changes what a customer is allowed to spend.
const outboundQuery = `
	SELECT
		-- Transfers, counted from initiation. An external transfer holds the
		-- money now and settles later; waiting for settlement would leave
		-- every payment in flight invisible to the ceiling.
		coalesce((
			SELECT sum(send_amount_minor) FROM transfers
			 WHERE sender_user_id = $1 AND send_currency = $2
			   AND status NOT IN ('failed', 'cancelled')
			   AND created_at >= date_trunc('day', now())
		), 0)
		+
		-- Card payments approved but not yet settled. A card authorisation
		-- holds the money now and captures later, so between the two it
		-- appears nowhere else — the same blind spot an external transfer
		-- has. Only pending ones: once settled it is in the feed below, and
		-- once reversed or expired the money came back.
		coalesce((
			SELECT sum(a.amount_minor)
			  FROM card_authorisations a
			 WHERE a.customer_id = $1 AND a.currency = $2
			   AND a.decision = 'approved' AND a.status = 'pending'
			   AND a.authorised_at >= date_trunc('day', now())
		), 0)
		+
		-- Every other rail: bills, giving, family, and settled card spend.
		-- transfer_out is excluded so a transfer is not counted twice, and
		-- savings_in because money moved into your own pot has not been spent.
		coalesce((
			SELECT sum(amount_minor) FROM wallet_transactions
			 WHERE customer_id = $1 AND currency = $2
			   AND direction = 'out'
			   AND transaction_type NOT IN ('transfer_out', 'savings_in')
			   AND occurred_at >= date_trunc('day', now())
		), 0)`

func (s *LimitsService) FlagIfOverBalanceCap(ctx context.Context, tx pgx.Tx,
	customerID uuid.UUID, currency string, balanceAfterMinor int64) error {

	var cap int64
	err := tx.QueryRow(ctx, `
		SELECT amount_minor FROM customer_limits
		 WHERE user_id = $1 AND currency = $2 AND limit_type = 'balance_cap'
		   AND effective_to IS NULL AND amount_minor IS NOT NULL`,
		customerID, currency).Scan(&cap)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // no ceiling on this tier
		}
		return err
	}
	if balanceAfterMinor <= cap {
		return nil
	}

	// One open case per customer per currency, not one per deposit. Somebody
	// over their ceiling who is paid three times that day has one problem, and
	// a queue with three identical cases in it is a queue nobody reads.
	reference := "BALCAP-" + customerID.String() + "-" + currency
	if _, err := tx.Exec(ctx, capFlagSQL,
		reference, customerID,
		"Balance above the tier ceiling",
		fmt.Sprintf("Balance is %d minor units of %s against a ceiling of %d. "+
			"The money was credited: it had already left the sender's bank. "+
			"The customer has been asked to verify further.",
			balanceAfterMinor, currency, cap)); err != nil {
		return err
	}

	// And tell them, because a limit somebody is not told about is one they
	// cannot do anything about.
	return outbox.Enqueue(ctx, tx, "customer", customerID,
		"notification.balance_cap_reached", map[string]string{
			"customer_id": customerID.String(),
		}, "")
}

// capFlagSQL is named so a test can read it: what this statement does and does
// NOT do — it records, it does not act on the account — is the whole of the
// credit-and-flag decision.
const capFlagSQL = `
	INSERT INTO compliance_cases
		(case_reference, customer_id, case_type, priority, status, title, summary, source)
	VALUES ($1, $2, 'transaction_monitoring_alert', 'medium', 'open', $3, $4, 'automated_rule')
	ON CONFLICT (case_reference) DO NOTHING`
