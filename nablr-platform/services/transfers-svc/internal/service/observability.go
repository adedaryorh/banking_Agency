package service

import (
	"errors"

	"nabla/transfers-svc/internal/metrics"
	"nabla/transfers-svc/internal/providers"
)

func outcomeOf(err error) string {
	if err == nil {
		return "ok"
	}
	var pe *providers.Error
	if errors.As(err, &pe) {
		return string(pe.Code)
	}
	return "error"
}

func boolLabel(b bool) string {
	if b {
		return "valid"
	}
	return "invalid"
}

// observeCircuit mirrors the payout breaker state to Prometheus: 0 closed, 1
// open, 2 half-open. Alert on sustained non-zero.
func observeCircuit(provider string, b *providers.Breaker) {
	var v float64
	switch b.State() {
	case "open":
		v = 1
	case "half_open":
		v = 2
	}
	metrics.CircuitState.WithLabelValues(provider).Set(v)
}
