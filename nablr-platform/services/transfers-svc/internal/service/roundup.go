package service

import (
	"context"
	"fmt"
	"log"

	db "nabla/transfers-svc/db/sqlc"
	"nabla/transfers-svc/internal/metrics"
	"nabla/transfers-svc/internal/models"
	"nabla/transfers-svc/internal/platform/clock"
	platformdb "nabla/transfers-svc/internal/platform/db"
)

func (s *Service) SweepRoundUps(ctx context.Context, batch int32) (int, error) {
	if batch < 1 {
		batch = 50
	}
	accruals, err := s.q.RoundUpAccrualsDueForSweep(ctx, batch)
	if err != nil {
		return 0, err
	}
	ledger := NewLedgerService(platformdb.NewAdapter(s.pool), clock.RealClock{})
	swept := 0
	for _, a := range accruals {
		if err := s.sweepAccrual(ctx, ledger, a); err != nil {
			// Leave the accrual to try next cycle; the claim is rolled back
			// with the transaction, so nothing is lost.
			log.Printf("worker: round-up sweep for accrual %s deferred: %v", a.AccrualID, err)
			continue
		}
		swept++
	}
	metrics.AccrualsSwept.Add(float64(swept))
	return swept, nil
}

func (s *Service) sweepAccrual(ctx context.Context, ledger *LedgerService, a db.RoundUpAccrualsDueForSweepRow) error {
	if !a.SpendAccountID.Valid {
		return fmt.Errorf("customer has no ledger account for %s", a.Currency)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)

	// Claim: zero the accrual so a concurrent worker finds nothing to do. The
	// WHERE clause makes the race safe — only one winner per accrual.
	n, err := q.ClaimRoundUpAccrual(ctx, a.AccrualID)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("accrual %s already swept by another worker", a.AccrualID)
	}

	minor, err := q.RoundUpUnSweptMinor(ctx, a.AccrualID)
	if err != nil {
		return err
	}
	if minor <= 0 {
		return tx.Commit(ctx) // nothing to move; claim already recorded the sweep attempt
	}
	// Destination account, one per customer per currency, opened idempotently.
	savings, err := ledger.OpenAccountInTx(ctx, tx, OpenAccountInput{
		Type:       models.AccountCustomerAsset,
		Currency:   a.Currency,
		OwnerType:  "customer",
		CustomerID: a.CustomerID,
		Name:       "Round-up " + a.Currency,
		Code:       "round_up:" + a.Currency + ":" + a.CustomerID.String(),
	})
	if err != nil {
		return err
	}

	ref := "roundup:" + a.AccrualID.String()[:8]
	if _, err := ledger.PostTransaction(ctx, tx, PostTransactionInput{
		Reference:   ref,
		Kind:        models.KindSavingsIn,
		Description: "Round-up savings",
		Entries: []EntryInput{
			{AccountID: a.SpendAccountID.Bytes, Direction: models.Debit, AmountMinor: minor, Currency: a.Currency},
			{AccountID: savings.ID, Direction: models.Credit, AmountMinor: minor, Currency: a.Currency},
		},
	}); err != nil {
		return err
	}

	if err := q.MarkRoundUpItemsSwept(ctx, a.AccrualID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
