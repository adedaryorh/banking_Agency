// Package metrics exposes Prometheus instrumentation for the notification
// service. It implements retry.Observer so every delivery and retry is counted
// without coupling the retry logic to a specific monitoring stack.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"nabla/notification-svc/internal/retry"
)

var _ retry.Observer = (*Metrics)(nil)

type Metrics struct {
	registry   *prometheus.Registry
	deliveries *prometheus.CounterVec
	retries    *prometheus.CounterVec
	duration   *prometheus.HistogramVec
}

// New builds a Metrics registry with a private Prometheus registry so it never
// collides with any framework default registry.
func New() *Metrics {
	registry := prometheus.NewRegistry()
	m := &Metrics{
		registry: registry,
		deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nabla_notification_deliveries_total",
			Help: "Total notification deliveries by channel, provider, and outcome.",
		}, []string{"channel", "provider", "outcome"}),
		retries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nabla_notification_retries_total",
			Help: "Total notification send retries by channel.",
		}, []string{"channel"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "nabla_notification_send_duration_seconds",
			Help:    "Notification send latency by channel and provider.",
			Buckets: prometheus.DefBuckets,
		}, []string{"channel", "provider"}),
	}
	registry.MustRegister(m.deliveries, m.retries, m.duration, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Handler returns the Prometheus scrape endpoint.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Delivery records the final outcome and total latency of one send.
func (m *Metrics) Delivery(channel, provider, outcome string, elapsed time.Duration) {
	m.deliveries.WithLabelValues(channel, provider, outcome).Inc()
	m.duration.WithLabelValues(channel, provider).Observe(elapsed.Seconds())
}

// Retry records one retried attempt.
func (m *Metrics) Retry(channel string) {
	m.retries.WithLabelValues(channel).Inc()
}