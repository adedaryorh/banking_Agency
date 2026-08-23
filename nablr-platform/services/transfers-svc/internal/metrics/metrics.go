package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	TransfersInitiated = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "transfers",
		Subsystem: "engine",
		Name:      "created_total",
		Help:      "Transfers created, by transfer type.",
	}, []string{"type"})

	TransferOutcomes = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "transfers",
		Subsystem: "engine",
		Name:      "outcomes_total",
		Help:      "Terminal transfer outcomes, by kind.",
	}, []string{"kind"})

	ComplianceFlags = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "transfers",
		Subsystem: "engine",
		Name:      "compliance_flags_total",
		Help:      "Transfers allowed but flagged for compliance monitoring, by reason.",
	}, []string{"reason"})

	// ProviderOperations counts each rail call by operation and result.
	ProviderOperations = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "transfers",
		Subsystem: "provider",
		Name:      "operations_total",
		Help:      "Provider rail calls, by provider, operation and result.",
	}, []string{"provider", "op", "result"})

	// ProviderLatency is the duration of provider rail calls, per op. Buckets
	// cover 10ms→30s: the rail's REPL latency, not our response latency.
	ProviderLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "transfers",
		Subsystem: "provider",
		Name:      "operation_seconds",
		Help:      "Provider rail call latency by provider and operation.",
		Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	}, []string{"provider", "op"})

	// CircuitState reports the payout circuit breaker: 0 closed, 1 open, 2
	// half-open. Alert when it spends sustained time > 0.
	CircuitState = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "transfers",
		Subsystem: "provider",
		Name:      "circuit_state",
		Help:      "Payout circuit breaker state: 0 closed, 1 open, 2 half-open.",
	}, []string{"provider"})

	// WebhooksReceived counts captured provider callbacks, split by validity.
	WebhooksReceived = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "transfers",
		Subsystem: "webhook",
		Name:      "received_total",
		Help:      "Provider webhook deliveries recorded, by signature validity.",
	}, []string{"valid"})

	// WebhooksReplayed counts admin replays of captured callbacks.
	WebhooksReplayed = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "transfers",
		Subsystem: "webhook",
		Name:      "replayed_total",
		Help:      "Captured provider callbacks replayed by operations.",
	})

	// DispatchFailures counts outbox events that could not be sent. High rate
	// with circuit open = rail down; high rate with circuit closed = bug.
	DispatchFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "transfers",
		Subsystem: "worker",
		Name:      "dispatch_failures_total",
		Help:      "Outbox payout dispatches that failed, by outcome.",
	}, []string{"outcome"})

	// AccrualsSwept counts round-up accruals moved per sweep cycle.
	AccrualsSwept = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "transfers",
		Subsystem: "worker",
		Name:      "round_up_accruals_swept_total",
		Help:      "Round-up accruals swept into savings per cycle.",
	})
)
