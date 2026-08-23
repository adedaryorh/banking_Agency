package retry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"nabla/notification-svc/internal/providers"
)

type recordingObserver struct {
	deliveries []delivery
	retries    []string
}

type delivery struct {
	channel, provider, outcome string
	elapsed                    time.Duration
}

func (r *recordingObserver) Delivery(channel, provider, outcome string, elapsed time.Duration) {
	r.deliveries = append(r.deliveries, delivery{channel, provider, outcome, elapsed})
}
func (r *recordingObserver) Retry(channel string) { r.retries = append(r.retries, channel) }

type fakeSMS struct {
	name    string
	calls   int
	failFor int
	failErr error
}

func (f *fakeSMS) Name() string { return f.name }
func (f *fakeSMS) SendSMS(_ context.Context, _ providers.SMSMessage) (*providers.SMSResult, error) {
	f.calls++
	if f.calls <= f.failFor {
		return nil, f.failErr
	}
	return &providers.SMSResult{Reference: "ref", Provider: f.name}, nil
}

func wrapSMSFake(name string, failErr error, failFor int, attempts int) (*fakeSMS, *recordingObserver, providers.SMSProvider) {
	inner := &fakeSMS{name: name, failFor: failFor, failErr: failErr}
	obs := &recordingObserver{}
	return inner, obs, WrapSMS(inner, Config{Enabled: true, MaxAttempts: attempts, BackoffBase: time.Millisecond, BackoffCap: 4 * time.Millisecond}, obs)
}

// likeTransportError wraps like the real providers: the sentinel stays in the
// chain so errors.Is can classify it, mirroring client.Do's "%w:" style.
func likeTransportError(sentinel error) error {
	return fmt.Errorf("send: %w", sentinel)
}

func TestRetryRecoversFromUnavailable(t *testing.T) {
	inner, obs, wrapped := wrapSMSFake("termii", likeTransportError(providers.ErrUnavailable), 2, 4)
	_, err := wrapped.SendSMS(context.Background(), providers.SMSMessage{To: "+234", Body: "code"})
	if err != nil {
		t.Fatalf("send = %v", err)
	}
	if inner.calls != 3 {
		t.Fatalf("calls = %d, want 3", inner.calls)
	}
	if len(obs.retries) != 2 {
		t.Fatalf("retries = %d, want 2", len(obs.retries))
	}
	if len(obs.deliveries) != 1 || obs.deliveries[0].outcome != OutcomeSuccess {
		t.Fatalf("deliveries = %+v", obs.deliveries)
	}
}

func TestRetryExhaustionReturnsOriginalError(t *testing.T) {
	inner, obs, wrapped := wrapSMSFake("twilio", likeTransportError(providers.ErrUnavailable), 10, 3)
	_, err := wrapped.SendSMS(context.Background(), providers.SMSMessage{To: "+234", Body: "code"})
	if err == nil || !strings.Contains(err.Error(), providers.ErrUnavailable.Error()) {
		t.Fatalf("err = %v, want unavailable", err)
	}
	if inner.calls != 3 {
		t.Fatalf("calls = %d, want 3", inner.calls)
	}
	if len(obs.retries) != 2 {
		t.Fatalf("retries = %d, want 2", len(obs.retries))
	}
	last := obs.deliveries[len(obs.deliveries)-1]
	if last.outcome != OutcomeRetryExhausted {
		t.Fatalf("outcome = %q, want %q", last.outcome, OutcomeRetryExhausted)
	}
}

func TestNoRetryOnIndeterminate(t *testing.T) {
	inner, obs, wrapped := wrapSMSFake("termii", likeTransportError(providers.ErrIndeterminate), 10, 4)
	_, err := wrapped.SendSMS(context.Background(), providers.SMSMessage{To: "+234", Body: "code"})
	if err == nil {
		t.Fatal("expected error")
	}
	if inner.calls != 1 {
		t.Fatalf("calls = %d, want 1 (timeouts must not be auto-resent)", inner.calls)
	}
	if len(obs.retries) != 0 {
		t.Fatalf("retries = %d, want 0", len(obs.retries))
	}
	if last := obs.deliveries[len(obs.deliveries)-1]; last.outcome != OutcomeIndeterminate {
		t.Fatalf("outcome = %q, want indeterminate", last.outcome)
	}
}

func TestNoRetryOnRejected(t *testing.T) {
	inner, obs, wrapped := wrapSMSFake("termii", likeTransportError(providers.ErrRejected), 10, 4)
	_, err := wrapped.SendSMS(context.Background(), providers.SMSMessage{To: "+234", Body: "code"})
	if err == nil {
		t.Fatal("expected error")
	}
	if inner.calls != 1 {
		t.Fatalf("calls = %d, want 1 (4xx must never be retried)", inner.calls)
	}
	if len(obs.retries) != 0 {
		t.Fatalf("retries = %d, want 0", len(obs.retries))
	}
}

func TestUnclassifiedErrorsAreNotRetried(t *testing.T) {
	inner, obs, wrapped := wrapSMSFake("termii", errors.New("some opaque failure"), 10, 4)
	_, err := wrapped.SendSMS(context.Background(), providers.SMSMessage{To: "+234", Body: "code"})
	if err == nil {
		t.Fatal("expected error")
	}
	if inner.calls != 1 {
		t.Fatalf("calls = %d, want 1 (opaque errors must not blind-resend)", inner.calls)
	}
	if last := obs.deliveries[len(obs.deliveries)-1]; last.outcome != OutcomePermanent {
		t.Fatalf("outcome = %q, want permanent", last.outcome)
	}
}

func TestDisabledPerformsSingleAttempt(t *testing.T) {
	inner := &fakeSMS{name: "termii", failFor: 10, failErr: likeTransportError(providers.ErrUnavailable)}
	wrapped := WrapSMS(inner, Config{Enabled: false, MaxAttempts: 3, BackoffBase: time.Millisecond, BackoffCap: time.Millisecond}, nil)
	_, err := wrapped.SendSMS(context.Background(), providers.SMSMessage{To: "+234", Body: "code"})
	if err == nil {
		t.Fatal("expected error")
	}
	if inner.calls != 1 {
		t.Fatalf("calls = %d, want 1 (retry disabled)", inner.calls)
	}
}

func TestRespectsContextCancellation(t *testing.T) {
	inner := &fakeSMS{name: "termii", failFor: 10, failErr: likeTransportError(providers.ErrUnavailable)}
	obs := &recordingObserver{}
	wrapped := WrapSMS(inner, Config{Enabled: true, MaxAttempts: 5, BackoffBase: time.Second, BackoffCap: time.Second}, obs)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := wrapped.SendSMS(ctx, providers.SMSMessage{To: "+234", Body: "code"})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(obs.deliveries) != 1 || obs.deliveries[0].outcome != OutcomeIndeterminate {
		t.Fatalf("deliveries = %+v", obs.deliveries)
	}
}

func TestBackoffWithinCap(t *testing.T) {
	cfg := Config{Enabled: true, MaxAttempts: 3, BackoffBase: 10 * time.Millisecond, BackoffCap: 40 * time.Millisecond}
	for attempt := 1; attempt <= 4; attempt++ {
		if d := backoff(cfg, attempt); d < 0 || d > cfg.BackoffCap {
			t.Fatalf("attempt %d: backoff = %v outside [0, cap]", attempt, d)
		}
	}
}