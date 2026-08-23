package healthmonitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"log/slog"

	"nabla/api-gateway/internal/alerting"
)

type recordingNotifier struct {
	mu     sync.Mutex
	alerts []string
	logs   []string
}

func (r *recordingNotifier) Alert(_ context.Context, _ string, message string, _ map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alerts = append(r.alerts, message)
}

func (r *recordingNotifier) Log(_ context.Context, _ slog.Level, message string, _ map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, message)
}

func (*recordingNotifier) Writer() alerting.LogWriter { return nopWriter{} }

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// Simulates a backend that goes down then recovers, and asserts exactly one
// Alert fires on the down edge and one Log on recovery — no per-probe spam.
func TestMonitor_EdgeTriggeredTransitions(t *testing.T) {
	var mu sync.Mutex
	down := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ok := !down
		mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifier := &recordingNotifier{}
	m := New(notifier, []Target{{Name: "test-svc", BaseURL: srv.URL}})
	m.interval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()

	waitFor(t, func() bool {
		notifier.mu.Lock()
		defer notifier.mu.Unlock()
		return len(notifier.alerts) >= 1
	})

	mu.Lock()
	down = false
	mu.Unlock()
	waitFor(t, func() bool {
		notifier.mu.Lock()
		defer notifier.mu.Unlock()
		return len(notifier.logs) >= 1
	})

	// Give any stray probes time to over-fire, then assert no duplicates.
	time.Sleep(5 * time.Millisecond)
	cancel()
	<-done

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if got := len(notifier.alerts); got != 1 {
		t.Fatalf("alerts=%d, want exactly 1 (one per down edge)", got)
	}
	if got := len(notifier.logs); got != 1 {
		t.Fatalf("recoveries=%d, want exactly 1 (one per up edge)", got)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}