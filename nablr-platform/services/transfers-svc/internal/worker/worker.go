package worker

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"

	"nabla/transfers-svc/internal/service"
)

const reconcileInterval = time.Minute

const statementReconInterval = 30 * time.Minute

const (
	undispatchedAge = 10 * time.Minute
	processingAge   = 5 * time.Minute
	underReviewAge  = 15 * time.Minute
)

type Worker struct {
	service  *service.Service
	funding  *service.FundingService
	interval time.Duration
}

func New(s *service.Service, interval time.Duration, funding ...*service.FundingService) *Worker {
	if interval <= 0 {
		interval = time.Second
	}
	var fundingService *service.FundingService
	if len(funding) > 0 {
		fundingService = funding[0]
	}
	return &Worker{service: s, funding: fundingService, interval: interval}
}

func (w *Worker) Run(ctx context.Context) {
	dispatch := time.NewTicker(w.interval)
	defer dispatch.Stop()
	reconcile := time.NewTicker(reconcileInterval)
	defer reconcile.Stop()
	statement := time.NewTicker(statementReconInterval)
	defer statement.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-dispatch.C:
			w.dispatch(ctx)
		case <-reconcile.C:
			w.reconcile(ctx)
		case <-statement.C:
			w.statementRecon(ctx)
		}
	}
}

// dispatch is the hot loop: get instructions to the rail.
func (w *Worker) dispatch(ctx context.Context) {
	if _, err := w.service.RunDueSchedules(ctx, 50); err != nil {
		logSweep("run due schedules", err)
	}
	if err := w.service.DispatchOutbox(ctx, 50); err != nil {
		logSweep("dispatch outbox", err)
	}
}

func (w *Worker) reconcile(ctx context.Context) {
	if _, err := w.service.ExpireStaleHolds(ctx, 50); err != nil {
		logSweep("expire stale holds", err)
	}
	if _, err := w.service.ReleaseUndispatched(ctx, undispatchedAge, 50); err != nil {
		logSweep("release undispatched", err)
	}
	if _, _, err := w.service.ReconcileProcessing(ctx, processingAge, 50); err != nil {
		logSweep("reconcile processing", err)
	}
	if _, err := w.service.ReconcileUnderReview(ctx, underReviewAge, 50); err != nil {
		logSweep("reconcile under review", err)
	}
	if w.funding != nil {
		if _, _, err := w.funding.ReconcilePendingCollections(ctx, 50); err != nil {
			logSweep("reconcile collections", err)
		}
	}

	if _, err := w.service.SweepRoundUps(ctx, 50); err != nil {
		logSweep("sweep round-ups", err)
	}
}

func (w *Worker) statementRecon(ctx context.Context) {
	now := time.Now()
	if _, err := w.service.RunReconciliation(ctx, w.service.ProviderName(ctx), "", now.Add(-24*time.Hour), now, uuid.Nil); err != nil {
		logSweep("statement reconciliation", err)
	}
}

func logSweep(name string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return // shutdown, not a fault
	}
	log.Printf("worker: %s failed: %v", name, err)
}
