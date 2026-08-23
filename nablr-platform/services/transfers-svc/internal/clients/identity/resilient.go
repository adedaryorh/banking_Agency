package identity

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"nabla/transfers-svc/internal/models"
	"nabla/transfers-svc/internal/providers"
	"nabla/transfers-svc/internal/service"
)

const cacheTTL = 5 * time.Minute

type Resilient struct {
	next    service.IdentityClient
	breaker *providers.Breaker
	now     func() time.Time

	users  ttlCache[service.UserProfile]
	kyc    ttlCache[service.KYCProfile]
	byUser ttlCache[service.UserProfile]
	byNum  ttlCache[service.UserProfile]
}

func NewResilient(next service.IdentityClient) *Resilient {
	if next == nil {
		return nil
	}
	return &Resilient{
		next:    next,
		breaker: providers.NewBreaker(5, 30*time.Second),
		now:     time.Now,
	}
}

var _ service.IdentityClient = (*Resilient)(nil)

// CircuitState backs the /health/providers ops surface.
func (r *Resilient) CircuitState() string {
	if r == nil || r.breaker == nil {
		return "closed"
	}
	return r.breaker.State()
}

func (r *Resilient) VerifyPIN(ctx context.Context, userID uuid.UUID, pin string) (bool, error) {
	now := r.now()
	if !r.breaker.Allow(now) {
		return false, service.ErrIdentityUnavailable
	}
	ok, err := r.next.VerifyPIN(ctx, userID, pin)
	if err != nil {
		// A settled PIN verdict (not set / locked) is identity ANSWERING, not an
		// outage: record success so it does not trip the breaker, and pass the
		// reason through unchanged. Only a transport failure counts against the
		// circuit — otherwise a run of customers with no PIN set would open it and
		// start serving fail-closed 503s to everyone.
		if errors.Is(err, models.ErrPINNotSet) || errors.Is(err, models.ErrPINLocked) {
			r.breaker.Success()
			return false, err
		}
		r.breaker.Failure(now)
		return false, err
	}
	r.breaker.Success()
	return ok, nil
}

func (r *Resilient) GetUser(ctx context.Context, userID uuid.UUID) (service.UserProfile, error) {
	key := userID.String()
	now := r.now()
	if !r.breaker.Allow(now) {
		return r.users.serve(key, now)
	}
	p, err := r.next.GetUser(ctx, userID)
	if err == nil {
		r.breaker.Success()
		r.users.put(key, p, now)
		return p, nil
	}
	// A settled "no such user" is identity ANSWERING, not an outage. Do not trip
	// the breaker and do not convert it into authorization unavailable; callers
	// that resolve recipients need this to become beneficiary-not-found.
	if errors.Is(err, service.ErrIdentityUserNotFound) {
		r.breaker.Success()
		return p, err
	}
	r.breaker.Failure(now)
	return r.users.serveOrError(key, r.now(), err)
}

// GetKYCProfile reads and caches the compliance profile. This is the value the
// money path leans on (tier, sanctions), so the TTL boundary — not an
// always-serve policy — decides staleness, and past TTL the fail-closed 503 is
// the same one the raw client returns immediately.
func (r *Resilient) GetKYCProfile(ctx context.Context, userID uuid.UUID) (service.KYCProfile, error) {
	key := userID.String()
	now := r.now()
	if !r.breaker.Allow(now) {
		return r.kyc.serve(key, now)
	}
	p, err := r.next.GetKYCProfile(ctx, userID)
	if err == nil {
		r.breaker.Success()
		r.kyc.put(key, p, now)
		return p, nil
	}
	// A settled "no profile" is a correct answer, not a circuit failure; it must
	// not slam the breaker shut (which would then serve fail-closed 503s). Mirror
	// resolve(): record success, answer fresh, cache nothing.
	if errors.Is(err, service.ErrIdentityUserNotFound) {
		r.breaker.Success()
		return p, err
	}
	r.breaker.Failure(now)
	return r.kyc.serveOrError(key, r.now(), err)
}

func (r *Resilient) GetUserByUsername(ctx context.Context, username string) (service.UserProfile, error) {
	return r.resolve(ctx, &r.byUser, username, r.next.GetUserByUsername)
}

func (r *Resilient) GetUserByAccountNumber(ctx context.Context, accountNumber string) (service.UserProfile, error) {
	return r.resolve(ctx, &r.byNum, accountNumber, r.next.GetUserByAccountNumber)
}

func (r *Resilient) resolve(ctx context.Context, cache *ttlCache[service.UserProfile], key string, live func(context.Context, string) (service.UserProfile, error)) (service.UserProfile, error) {
	now := r.now()
	if !r.breaker.Allow(now) {
		return cache.serveResolve(key, now)
	}
	p, err := live(ctx, key)
	switch {
	case err == nil:
		r.breaker.Success()
		cache.put(key, p, now)
		return p, nil
	case errors.Is(err, service.ErrIdentityUserNotFound):
		r.breaker.Success()
		return service.UserProfile{}, service.ErrIdentityUserNotFound
	default:
		r.breaker.Failure(now)
		return cache.serveResolveOrError(key, r.now(), err)
	}
}

// ttlCache is a minimal keyed TTL cache. Expired entries are dropped on read so
// the map cannot grow without bound.
type ttlCache[T any] struct {
	mu   sync.Mutex
	vals map[string]cacheEntry[T]
}

type cacheEntry[T any] struct {
	value T
	at    time.Time
}

func (c *ttlCache[T]) get(key string, now time.Time) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.vals[key]
	if !ok {
		var zero T
		return zero, false
	}
	if now.Sub(e.at) >= cacheTTL {
		delete(c.vals, key)
		var zero T
		return zero, false
	}
	return e.value, true
}

func (c *ttlCache[T]) put(key string, value T, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.vals == nil {
		c.vals = make(map[string]cacheEntry[T])
	}
	c.vals[key] = cacheEntry[T]{value: value, at: at}
}

// serve returns the cached value inside its TTL, else the caller's
// fail-closed sentinel.
func (c *ttlCache[T]) serve(key string, now time.Time) (T, error) {
	if v, ok := c.get(key, now); ok {
		return v, nil
	}
	var zero T
	return zero, service.ErrIdentityUnavailable
}

func (c *ttlCache[T]) serveOrError(key string, now time.Time, cause error) (T, error) {
	if v, ok := c.get(key, now); ok {
		return v, nil
	}
	var zero T
	return zero, errors.Join(service.ErrIdentityUnavailable, cause)
}

// serveResolve is serve for the resolve path, whose degraded answer is "not
// found" rather than "unavailable": the caller paints an empty Nablr half.
func (c *ttlCache[T]) serveResolve(key string, now time.Time) (T, error) {
	if v, ok := c.get(key, now); ok {
		return v, nil
	}
	var zero T
	return zero, service.ErrIdentityUserNotFound
}

// serveResolveOrError mirrors serveResolve, but preserves the concrete cause for
// callers that choose to fail closed instead of degrading to "not found".
func (c *ttlCache[T]) serveResolveOrError(key string, now time.Time, cause error) (T, error) {
	if v, ok := c.get(key, now); ok {
		return v, nil
	}
	var zero T
	return zero, errors.Join(service.ErrIdentityUnavailable, cause)
}
