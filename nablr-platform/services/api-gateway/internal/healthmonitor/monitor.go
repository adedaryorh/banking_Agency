package healthmonitor

import (
	"context"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"nabla/api-gateway/internal/alerting"
)

// Target is one backend the gateway depends on.
type Target struct {
	Name    string
	BaseURL string
}

// Monitor tracks the up/down edge for each target.
type Monitor struct {
	alerts   alerting.Notifier
	client   *http.Client
	targets  []Target
	mu       sync.Mutex
	healthy  map[string]bool
	failures map[string]int
	interval time.Duration
}

// New creates a Monitor. Healthy-target keys are filled lazily on first check.
func New(alerts alerting.Notifier, targets []Target) *Monitor {
	return &Monitor{
		alerts:   alerts,
		client:   &http.Client{Timeout: 3 * time.Second},
		targets:  targets,
		healthy:  make(map[string]bool),
		failures: make(map[string]int),
		interval: 30 * time.Second,
	}
}

// Run checks each target immediately, then on the configured interval until ctx
// is cancelled. Transitions are edge-triggered: one Alert on down, one Log on
// recovery.
func (m *Monitor) Run(ctx context.Context) {
	m.checkAll(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkAll(ctx)
		}
	}
}

func (m *Monitor) checkAll(ctx context.Context) {
	for _, target := range m.targets {
		m.check(ctx, target)
	}
}

func (m *Monitor) check(ctx context.Context, target Target) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.BaseURL+"/health", nil)
	if err != nil {
		log.Printf("healthmonitor: bad target %s: %v", target.Name, err)
		return
	}
	resp, err := m.client.Do(req)
	up := err == nil && resp != nil && resp.StatusCode == http.StatusOK
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	m.mu.Lock()
	wasUp, known := m.healthy[target.Name]
	if up {
		m.failures[target.Name] = 0
		m.healthy[target.Name] = true
	} else if !known {
		m.healthy[target.Name] = false
	} else if wasUp {
		m.failures[target.Name]++
		if m.failures[target.Name] >= 3 {
			m.healthy[target.Name] = false
		}
	}
	confirmedDown := known && wasUp && !up && m.failures[target.Name] >= 3
	m.mu.Unlock()

	switch {
	case confirmedDown:
		m.alerts.Alert(ctx, "service down", target.Name+" is not responding on "+target.BaseURL+"/health",
			map[string]string{"service": target.Name, "base_url": target.BaseURL})
	case known && !wasUp && up:
		m.alerts.Log(ctx, 0, "service recovered", map[string]string{"service": target.Name, "base_url": target.BaseURL})
	case !known && !up:
		m.alerts.Alert(ctx, "service down", target.Name+" not responding on first probe ("+target.BaseURL+"/health)",
			map[string]string{"service": target.Name, "base_url": target.BaseURL})
	}
}
