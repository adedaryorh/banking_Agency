// Package retry decorates the notification providers with a bounded, jittered
// retry that only re-sends when the previous attempt is confirmed to have been
// rejected before delivery. Timeouts are deliberately not retried: the provider
// may already have accepted the message, and a duplicate is worse than a miss.
package retry

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"time"

	"nabla/notification-svc/internal/providers"
)

// Config controls the retry behaviour applied to the providers that main wires.
type Config struct {
	// Enabled toggles retries. When false only the first attempt is made.
	Enabled bool
	// MaxAttempts is the total number of send attempts, including the first.
	MaxAttempts int
	// BackoffBase is the initial backoff; each attempt doubles it up to BackoffCap.
	BackoffBase time.Duration
	// BackoffCap bounds the backoff window regardless of how many attempts remain.
	BackoffCap time.Duration
}

// DefaultConfig is the starting point; main only overrides MaxAttempts.
func DefaultConfig() Config {
	return Config{Enabled: true, MaxAttempts: 3, BackoffBase: 200 * time.Millisecond, BackoffCap: 2 * time.Second}
}

// attempts returns how many times a send is executed. A disabled or degenerate
// config results in a single attempt with no retries.
func (c Config) attempts() int {
	if !c.Enabled || c.MaxAttempts < 2 {
		return 1
	}
	return c.MaxAttempts
}

// Outcome labels reported to the Observer.
const (
	OutcomeSuccess        = "success"
	OutcomeRetryExhausted = "retry_exhausted"
	OutcomeIndeterminate  = "indeterminate"
	OutcomePermanent      = "permanent"
)

// Observer is notified about every send and every retry so callers can record
// metrics without coupling the retry logic to any specific monitoring stack.
type Observer interface {
	// Delivery is called once per send with the final outcome and total latency.
	Delivery(channel, provider, outcome string, elapsed time.Duration)
	// Retry is called immediately before each retried attempt.
	Retry(channel string)
}

type nopObserver struct{}

func (nopObserver) Delivery(string, string, string, time.Duration) {}
func (nopObserver) Retry(string)                                   {}

func asObserver(obs Observer) Observer {
	if obs == nil {
		return nopObserver{}
	}
	return obs
}

const (
	channelSMS   = "sms"
	channelEmail = "email"
	channelPush  = "push"
)

// WrapSMS decorates sms with the retry policy.
func WrapSMS(inner providers.SMSProvider, cfg Config, obs Observer) providers.SMSProvider {
	return &smsRetry{inner: inner, cfg: cfg, obs: asObserver(obs)}
}

// WrapEmail decorates email with the retry policy. Wrapping the failover chain
// as a whole means every provider in the chain gets its chance per attempt.
func WrapEmail(inner providers.EmailProvider, cfg Config, obs Observer) providers.EmailProvider {
	return &emailRetry{inner: inner, cfg: cfg, obs: asObserver(obs)}
}

// WrapPush decorates push with the retry policy.
func WrapPush(inner providers.PushProvider, cfg Config, obs Observer) providers.PushProvider {
	return &pushRetry{inner: inner, cfg: cfg, obs: asObserver(obs)}
}

type smsRetry struct {
	inner providers.SMSProvider
	cfg   Config
	obs   Observer
}

func (r *smsRetry) Name() string { return r.inner.Name() }

func (r *smsRetry) SendSMS(ctx context.Context, message providers.SMSMessage) (*providers.SMSResult, error) {
	var result *providers.SMSResult
	err := run(ctx, r.cfg, r.obs, channelSMS, r.inner.Name(), func(ctx context.Context) error {
		got, err := r.inner.SendSMS(ctx, message)
		if err == nil {
			result = got
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type emailRetry struct {
	inner providers.EmailProvider
	cfg   Config
	obs   Observer
}

func (r *emailRetry) Name() string { return r.inner.Name() }

func (r *emailRetry) SendEmail(ctx context.Context, message providers.EmailMessage) (*providers.EmailResult, error) {
	var result *providers.EmailResult
	err := run(ctx, r.cfg, r.obs, channelEmail, r.inner.Name(), func(ctx context.Context) error {
		got, err := r.inner.SendEmail(ctx, message)
		if err == nil {
			result = got
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type pushRetry struct {
	inner providers.PushProvider
	cfg   Config
	obs   Observer
}

func (r *pushRetry) Name() string { return r.inner.Name() }

func (r *pushRetry) SendPush(ctx context.Context, message providers.PushMessage) (*providers.PushResult, error) {
	var result *providers.PushResult
	err := run(ctx, r.cfg, r.obs, channelPush, r.inner.Name(), func(ctx context.Context) error {
		got, err := r.inner.SendPush(ctx, message)
		if err == nil {
			result = got
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// run executes the send up to the configured number of attempts, backing off
// with full jitter between retries and recording the outcome on the observer.
func run(ctx context.Context, cfg Config, obs Observer, channel, provider string, op func(ctx context.Context) error) error {
	attempts := cfg.attempts()
	start := time.Now()
	var last error

	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			obs.Delivery(channel, provider, OutcomeIndeterminate, time.Since(start))
			return err
		}
		last = op(ctx)
		if last == nil {
			obs.Delivery(channel, provider, OutcomeSuccess, time.Since(start))
			return nil
		}
		if attempt == attempts || !isRetryable(last) {
			break
		}
		obs.Retry(channel)
		timer := time.NewTimer(backoff(cfg, attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			obs.Delivery(channel, provider, OutcomeIndeterminate, time.Since(start))
			return ctx.Err()
		case <-timer.C:
		}
	}

	obs.Delivery(channel, provider, outcome(last), time.Since(start))
	return last
}

func outcome(err error) string {
	switch {
	case err == nil:
		return OutcomeSuccess
	case errors.Is(err, providers.ErrIndeterminate):
		return OutcomeIndeterminate
	case isRetryable(err):
		return OutcomeRetryExhausted
	default:
		return OutcomePermanent
	}
}

// isRetryable reports whether the error is a confirmed, safe-to-resend failure.
// Only a definitive "we did not deliver" result allows an automatic retry:
//   - ErrUnavailable (429 / 5xx / connection refused): the provider did not
//     accept the message. Safe to try again.
//   - ErrIndeterminate (timeout / unknown outcome): the provider may already
//     have accepted it. Never auto-resend.
//   - ErrRejected / ErrNotFound (4xx / invalid destination): permanent. Never
//     retry.
//   - Unclassified errors default to non-retryable so we never blind-resend.
func isRetryable(err error) bool {
	if errors.Is(err, providers.ErrIndeterminate) {
		return false
	}
	if errors.Is(err, providers.ErrRejected) || errors.Is(err, providers.ErrNotFound) {
		return false
	}
	if errors.Is(err, providers.ErrUnavailable) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		// A connection-level failure is retryable; a deadline is partly unknown.
		return !netErr.Timeout()
	}
	return false
}

// backoff returns a retry delay with full jitter within a window that grows
// exponentially with each attempt, capped at BackoffCap.
func backoff(cfg Config, attempt int) time.Duration {
	window := cfg.BackoffBase
	for i := 1; i < attempt; i++ {
		window *= 2
		if window <= 0 || window >= cfg.BackoffCap {
			window = cfg.BackoffCap
			break
		}
	}
	if window <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(window)))
}