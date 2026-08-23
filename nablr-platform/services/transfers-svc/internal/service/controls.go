package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"nabla/transfers-svc/internal/platform/clock"
	"nabla/transfers-svc/internal/platform/id"
)

var (
	ErrControlNotFound  = errors.New("controls: not found")
	ErrGuardianControl  = errors.New("controls: this control was set by a guardian and cannot be changed here")
	ErrCategoryInvalid  = errors.New("controls: invalid category")
	ErrOverrideNeedsWhy = errors.New("controls: an override needs a reason")
)

var controlCategories = map[string]struct{}{
	"gambling": {}, "alcohol": {}, "adult_content": {}, "speculative_trading": {},
	"interest_lending": {}, "tobacco": {}, "nightlife": {}, "custom_mcc": {},
}

// ControlsService owns the customer's own spending policy.
type ControlsService struct {
	db    DB
	clock clock.Clock
}

func NewControlsService(db DB, clk clock.Clock) *ControlsService {
	return &ControlsService{db: db, clock: clk}
}

type Control struct {
	ID            uuid.UUID `json:"id"`
	Category      string    `json:"category"`
	Action        string    `json:"action"` // block | warn
	Scope         string    `json:"scope"`
	ScopeID       uuid.UUID `json:"scope_id"`
	CustomMCCs    []string  `json:"custom_mccs"`
	IsGuardianSet bool      `json:"is_guardian_set"`
	IsActive      bool      `json:"is_active"`
	CreatedAt     time.Time `json:"created_at"`
}

// Enable turns a control on for the customer (idempotent per scope+category).
func (s *ControlsService) Enable(ctx context.Context, customerID, byUserID uuid.UUID,
	category, action string, guardianSet bool, customMCCs []string) (*Control, error) {

	if _, ok := controlCategories[category]; !ok {
		return nil, ErrCategoryInvalid
	}
	if action != "block" && action != "warn" {
		return nil, errors.New("controls: action must be block or warn")
	}
	if category == "custom_mcc" && len(customMCCs) == 0 {
		return nil, errors.New("controls: custom_mcc needs at least one MCC")
	}
	if customMCCs == nil {
		customMCCs = []string{}
	}

	controlID := id.New()
	_, err := s.db.Exec(ctx, `
		INSERT INTO spending_controls
			(id, customer_id, scope, category, action, custom_mccs,
			 set_by_user_id, is_guardian_set)
		VALUES ($1, $2, 'customer', $3, $4, $5, $6, $7)
		ON CONFLICT (customer_id, scope, COALESCE(scope_id, '00000000-0000-0000-0000-000000000000'::uuid), category)
		WHERE is_active
		DO UPDATE SET action = EXCLUDED.action, custom_mccs = EXCLUDED.custom_mccs`,
		controlID, customerID, category, action, customMCCs, byUserID, guardianSet)
	if err != nil {
		return nil, err
	}
	return s.byCategory(ctx, customerID, category)
}

// Disable turns a control off. A guardian-set control on a dependent cannot be
// removed by the dependent themselves.
func (s *ControlsService) Disable(ctx context.Context, controlID, customerID, byUserID uuid.UUID) error {
	var guardianSet bool
	var setBy uuid.UUID
	err := s.db.QueryRow(ctx, `
		SELECT is_guardian_set, set_by_user_id FROM spending_controls
		 WHERE id = $1 AND customer_id = $2 AND is_active`, controlID, customerID).
		Scan(&guardianSet, &setBy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrControlNotFound
		}
		return err
	}
	if guardianSet && setBy != byUserID {
		return ErrGuardianControl
	}
	_, err = s.db.Exec(ctx, `
		UPDATE spending_controls SET is_active = false WHERE id = $1`, controlID)
	return err
}

func (s *ControlsService) List(ctx context.Context, customerID uuid.UUID) ([]Control, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, category, action, scope,
		       coalesce(scope_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       custom_mccs, is_guardian_set, is_active, created_at
		  FROM spending_controls
		 WHERE customer_id = $1 AND is_active
		 ORDER BY category`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Control{}
	for rows.Next() {
		var c Control
		if err := rows.Scan(&c.ID, &c.Category, &c.Action, &c.Scope, &c.ScopeID,
			&c.CustomMCCs, &c.IsGuardianSet, &c.IsActive, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *ControlsService) byCategory(ctx context.Context, customerID uuid.UUID, category string) (*Control, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id, category, action, scope,
		       coalesce(scope_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       custom_mccs, is_guardian_set, is_active, created_at
		  FROM spending_controls
		 WHERE customer_id = $1 AND category = $2 AND is_active AND scope = 'customer'`,
		customerID, category)
	var c Control
	if err := row.Scan(&c.ID, &c.Category, &c.Action, &c.Scope, &c.ScopeID,
		&c.CustomMCCs, &c.IsGuardianSet, &c.IsActive, &c.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrControlNotFound
		}
		return nil, err
	}
	return &c, nil
}

// ---------------------------------------------------------------------------
// Evaluation — called from the card authorisation path.
// ---------------------------------------------------------------------------

type ControlDecision struct {
	Outcome       string // allow | warn | block | allowed_by_override
	ControlID     uuid.UUID
	Category      string
	MCC           string
	MCCConfidence string
	OverrideID    uuid.UUID
}

// Evaluate decides whether the customer's own controls permit a payment to a
// merchant with the given MCC, and records blocked/warned outcomes so the
// customer can see exactly why and dispute a miscategorised merchant.
func (s *ControlsService) Evaluate(ctx context.Context, customerID uuid.UUID,
	mcc, merchantName string, amountMinor int64, currency string) (*ControlDecision, error) {

	// Map the MCC to an ethical category with its confidence.
	var category, confidence string
	err := s.db.QueryRow(ctx, `
		SELECT coalesce(ethical_category, ''), confidence
		  FROM merchant_categories WHERE mcc = $1`, mcc).Scan(&category, &confidence)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// Load the customer's active controls once.
	controlList, err := s.List(ctx, customerID)
	if err != nil {
		return nil, err
	}

	var matched *Control
	for i := range controlList {
		c := &controlList[i]
		if c.Category == category && category != "" {
			matched = c
			break
		}
		if c.Category == "custom_mcc" {
			for _, m := range c.CustomMCCs {
				if m == mcc {
					matched = c
					break
				}
			}
		}
		if matched != nil {
			break
		}
	}

	if matched == nil {
		return &ControlDecision{Outcome: "allow", MCC: mcc, MCCConfidence: confidence}, nil
	}

	now := s.clock.Now()

	// An unexpired override for this control lets the payment through, once
	// per merchant or blanket for the window.
	var overrideID uuid.UUID
	err = s.db.QueryRow(ctx, `
		SELECT id FROM spending_control_overrides
		 WHERE control_id = $1 AND cancelled_at IS NULL
		   AND starts_at <= $2 AND expires_at > $2
		   AND (merchant_name IS NULL OR lower(merchant_name) = lower($3))
		 ORDER BY expires_at LIMIT 1`, matched.ID, now, merchantName).Scan(&overrideID)
	hasOverride := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	outcome := matched.Action // block | warn
	if hasOverride {
		outcome = "allowed_by_override"
	}

	decision := &ControlDecision{
		Outcome: outcome, ControlID: matched.ID, Category: matched.Category,
		MCC: mcc, MCCConfidence: confidence, OverrideID: overrideID,
	}

	// Record every non-allow outcome; blocked/warned history is the customer's
	// own audit trail and the entry point for disputes.
	if outcome != "allow" {
		eventOutcome := map[string]string{
			"block": "blocked", "warn": "warned", "allowed_by_override": "allowed_by_override",
		}[outcome]
		var overridePtr *uuid.UUID
		if overrideID != uuid.Nil {
			overridePtr = &overrideID
		}
		var mccPtr *string
		if mcc != "" {
			// Only record MCCs the table knows: an unknown code recorded here
			// says the merchant was categorised when it was not.
			var exists bool
			_ = s.db.QueryRow(ctx,
				`SELECT true FROM merchant_categories WHERE mcc = $1`, mcc).Scan(&exists)
			if exists {
				mccPtr = &mcc
			}
		}
		if _, err := s.db.Exec(ctx, `
			INSERT INTO spending_control_events
				(customer_id, control_id, outcome, category, merchant_name,
				 merchant_mcc, mcc_confidence, amount_minor, currency, override_id)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, NULLIF($7, ''), $8, $9, $10)`,
			customerID, matched.ID, eventOutcome, matched.Category, merchantName,
			mccPtr, confidence, amountMinor, currency, overridePtr); err != nil {
			return nil, err
		}
	}

	return decision, nil
}

// RequestOverride creates a time-boxed exception. The route requires step-up
// authentication; the reason is the customer writing to themselves.
func (s *ControlsService) RequestOverride(ctx context.Context, controlID, customerID, byUserID uuid.UUID,
	merchantName, reason string, window time.Duration) (uuid.UUID, error) {

	if reason == "" {
		return uuid.Nil, ErrOverrideNeedsWhy
	}
	if window <= 0 || window > 24*time.Hour {
		window = time.Hour
	}

	var guardianSet bool
	err := s.db.QueryRow(ctx, `
		SELECT is_guardian_set FROM spending_controls
		 WHERE id = $1 AND customer_id = $2 AND is_active`, controlID, customerID).
		Scan(&guardianSet)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrControlNotFound
		}
		return uuid.Nil, err
	}
	if guardianSet {
		// Guardian-set controls need the guardian's approval — routed through
		// the family approval workflow, not created here.
		return uuid.Nil, ErrGuardianControl
	}

	overrideID := id.New()
	now := s.clock.Now()
	_, err = s.db.Exec(ctx, `
		INSERT INTO spending_control_overrides
			(id, control_id, customer_id, requested_by_user_id, reason,
			 merchant_name, starts_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8)`,
		overrideID, controlID, customerID, byUserID, reason, merchantName,
		now, now.Add(window))
	if err != nil {
		return uuid.Nil, err
	}
	return overrideID, nil
}
