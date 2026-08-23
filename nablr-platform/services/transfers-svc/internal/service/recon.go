package service

import (
	"context"
	"time"

	db "nabla/transfers-svc/db/sqlc"
	"nabla/transfers-svc/internal/providers"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type ReconciliationRun struct {
	ID                 uuid.UUID          `json:"id"`
	ProviderName       string             `json:"provider_name"`
	PeriodStart        time.Time          `json:"period_start"`
	PeriodEnd          time.Time          `json:"period_end"`
	Currency           string             `json:"currency"`
	Status             string             `json:"status"`
	ProviderItemCount  int                `json:"provider_item_count"`
	LedgerItemCount    int                `json:"ledger_item_count"`
	MatchedCount       int                `json:"matched_count"`
	BreakCount         int                `json:"break_count"`
	ProviderTotalMinor int64              `json:"provider_total_minor"`
	LedgerTotalMinor   int64              `json:"ledger_total_minor"`
	VarianceMinor      int64              `json:"variance_minor"`
	CreatedAt          time.Time          `json:"created_at"`
	CompletedAt        pgtype.Timestamptz `json:"completed_at"`
}

func (s *Service) RunReconciliation(ctx context.Context, providerName, currency string, from, to time.Time, triggeredBy uuid.UUID) (*ReconciliationRun, error) {
	run, err := s.q.CreateReconciliationRun(ctx, db.CreateReconciliationRunParams{
		ProviderName:       providerName,
		ReconciliationType: "payout",
		PeriodStart:        from,
		PeriodEnd:          to,
		Currency:           currency,
		TriggeredBy:        pgtype.UUID{Bytes: triggeredBy, Valid: triggeredBy != uuid.Nil},
	})
	if err != nil {
		return nil, err
	}

	page := &providers.StatementPage{}
	cursor := ""
	for {
		p, err := s.payout.FetchStatement(ctx, from, to, cursor)
		if err != nil {
			_ = s.q.CompleteReconciliationRun(ctx, db.CompleteReconciliationRunParams{ID: run.ID, Status: "failed"})
			return nil, err
		}
		page.Items = append(page.Items, p.Items...)
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}

	ours, err := s.q.CompletedTransfersForReconciliation(ctx, db.CompletedTransfersForReconciliationParams{
		ProviderName: pgtype.Text{String: providerName, Valid: true},
		CreatedAt:    from,
		CreatedAt_2:  to,
	})
	if err != nil {
		_ = s.q.CompleteReconciliationRun(ctx, db.CompleteReconciliationRunParams{ID: run.ID, Status: "failed"})
		return nil, err
	}

	ourAmount := map[string]int64{}
	for _, t := range ours {
		ourAmount[t.ProviderReference] = t.AmountMinor
	}

	var providerTotal, ledgerTotal int64
	matched := 0
	breaks := 0
	for _, it := range page.Items {
		ledgerTotal += it.AmountMinor
		providerTotal += it.AmountMinor
		status := "missing_in_ledger"
		if amt, ok := ourAmount[it.ProviderRef]; ok {
			status = "matched"
			if amt != it.AmountMinor {
				status = "amount_mismatch"
			}
		}
		ledgerAmt, hasLedger := ourAmount[it.ProviderRef]
		arg := db.InsertReconciliationItemParams{
			RunID:               run.ID,
			MatchStatus:         status,
			ProviderReference:   pgtype.Text{String: it.ProviderRef, Valid: it.ProviderRef != ""},
			OurReference:        pgtype.Text{String: it.OurRef, Valid: it.OurRef != ""},
			ProviderAmountMinor: pgtype.Int8{Int64: it.AmountMinor, Valid: true},
			ProviderStatus:      pgtype.Text{String: it.Status, Valid: it.Status != ""},
			ProviderTimestamp:   pgtype.Timestamptz{Time: it.OccurredAt, Valid: !it.OccurredAt.IsZero()},
			Currency:            pgtype.Text{String: it.Currency, Valid: it.Currency != ""},
		}
		if hasLedger {
			arg.LedgerAmountMinor = pgtype.Int8{Int64: ledgerAmt, Valid: true}
		}
		if status == "amount_mismatch" {
			arg.VarianceMinor = ledgerAmt - it.AmountMinor
		}
		if status == "matched" {
			matched++
		} else {
			breaks++
		}
		if err := s.q.InsertReconciliationItem(ctx, arg); err != nil {
			_ = s.q.CompleteReconciliationRun(ctx, db.CompleteReconciliationRunParams{ID: run.ID, Status: "failed"})
			return nil, err
		}
	}

	outcome := "completed"
	if breaks > 0 {
		outcome = "completed_with_breaks"
	}
	_ = s.q.CompleteReconciliationRun(ctx, db.CompleteReconciliationRunParams{
		ID:                 run.ID,
		Status:             outcome,
		ProviderItemCount:  int32(len(page.Items)),
		LedgerItemCount:    int32(len(ours)),
		MatchedCount:       int32(matched),
		BreakCount:         int32(breaks),
		ProviderTotalMinor: providerTotal,
		LedgerTotalMinor:   ledgerTotal,
		VarianceMinor:      providerTotal - ledgerTotal,
	})
	return &ReconciliationRun{
		ID: run.ID, ProviderName: providerName, PeriodStart: from, PeriodEnd: to,
		Currency: currency, Status: outcome,
		ProviderItemCount: len(page.Items), LedgerItemCount: len(ours),
		MatchedCount: matched, BreakCount: breaks,
		ProviderTotalMinor: providerTotal, LedgerTotalMinor: ledgerTotal,
		VarianceMinor: providerTotal - ledgerTotal, CreatedAt: run.CreatedAt,
	}, nil
}

// ProviderName is the routing name our payouts are filed under, which is also
// the key transfers.provider_name and provider_webhooks.provider_name carry.
func (s *Service) ProviderName(ctx context.Context) string {
	if s.payout == nil {
		return ""
	}
	return s.payout.Info().Name
}

// ProviderHealthStatus is one row of the ops health surface.
type ProviderHealthStatus struct {
	Name    string `json:"name"`
	Sandbox bool   `json:"sandbox"`
	Healthy bool   `json:"healthy"`
	Circuit string `json:"circuit,omitempty"`
	LastErr string `json:"last_error,omitempty"`
}

// ProviderHealth pings the payout rail and reports its circuit state. Failures
// are reported per rail, never fatal: the endpoint stays 200 so a scrape sees a
// body, and Prometheus alerting keys on the circuit gauge + health field.
func (s *Service) ProviderHealth(ctx context.Context) []ProviderHealthStatus {
	if s.payout == nil {
		return nil
	}
	info := s.payout.Info()
	st := ProviderHealthStatus{Name: info.Name, Sandbox: info.Sandbox}
	if s.breaker != nil {
		st.Circuit = s.breaker.State()
	}
	if err := s.payout.HealthCheck(ctx); err != nil {
		st.LastErr = err.Error()
	} else {
		st.Healthy = true
	}
	out := []ProviderHealthStatus{st}

	// The identity control plane has no ping of its own, but its circuit state
	// is the honest health read: closed means the last call worked, open means
	// we are serving degraded/cached reads (or failing closed on PINs), and a
	// long open window is what an alert on this row is for.
	if ic, ok := s.identity.(IdentityHealthCarrier); ok {
		state := ic.CircuitState()
		out = append(out, ProviderHealthStatus{
			Name:    "identity-svc",
			Circuit: state,
			Healthy: state == "closed",
		})
	}
	return out
}

// PayoutCircuitName exposes the breaker state for ops tooling.
func (s *Service) PayoutCircuitName(ctx context.Context) string {
	if s.breaker == nil {
		return "closed"
	}
	return s.breaker.State()
}

// ListReconciliationRuns returns audited windows, newest first.
func (s *Service) ListReconciliationRuns(ctx context.Context, limit int32) ([]ReconciliationRun, error) {
	if limit < 1 {
		limit = 25
	}
	rows, err := s.q.ListReconciliationRuns(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ReconciliationRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, ReconciliationRun{
			ID: r.ID, ProviderName: r.ProviderName, PeriodStart: r.PeriodStart, PeriodEnd: r.PeriodEnd,
			Currency: r.Currency, Status: r.Status,
			ProviderItemCount: int(r.ProviderItemCount), LedgerItemCount: int(r.LedgerItemCount),
			MatchedCount: int(r.MatchedCount), BreakCount: int(r.BreakCount),
			ProviderTotalMinor: r.ProviderTotalMinor, LedgerTotalMinor: r.LedgerTotalMinor,
			VarianceMinor: r.VarianceMinor, CreatedAt: r.CreatedAt, CompletedAt: r.CompletedAt,
		})
	}
	return out, nil
}
