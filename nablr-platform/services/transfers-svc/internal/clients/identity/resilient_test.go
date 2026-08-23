package identity

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"nabla/transfers-svc/internal/service"
)

// flaky is a downstream identity client whose failure modes are a switch, not
// a network: set the err fields and every corresponding call starts failing.
type flaky struct {
	mu        sync.Mutex
	userCalls int
	kycCalls  int
	pinCalls  int

	userErr error
	kycErr  error
	pinErr  error
}

func (f *flaky) VerifyPIN(context.Context, uuid.UUID, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pinCalls++
	return true, f.pinErr
}

func (f *flaky) GetUser(_ context.Context, u uuid.UUID) (service.UserProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userCalls++
	if f.userErr != nil {
		return service.UserProfile{}, f.userErr
	}
	return service.UserProfile{UserID: u.String(), Status: "active", NablrUsername: "ada", AccountNumber: "8031234567"}, nil
}

func (f *flaky) GetKYCProfile(_ context.Context, u uuid.UUID) (service.KYCProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kycCalls++
	if f.kycErr != nil {
		return service.KYCProfile{}, f.kycErr
	}
	return service.KYCProfile{UserID: u.String(), Tier: 3, FirstName: "Ada", LastName: "Eze", Sanctioned: false, PEP: false}, nil
}

func (f *flaky) GetUserByUsername(context.Context, string) (service.UserProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userCalls++
	if f.userErr != nil {
		return service.UserProfile{}, f.userErr
	}
	return service.UserProfile{UserID: uuid.NewString(), NablrUsername: "ada", AccountNumber: "8031234567", Status: "active"}, nil
}

func (f *flaky) GetUserByAccountNumber(context.Context, string) (service.UserProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userCalls++
	if f.userErr != nil {
		return service.UserProfile{}, f.userErr
	}
	return service.UserProfile{UserID: uuid.NewString(), NablrUsername: "ada", AccountNumber: "8031234567", Status: "active"}, nil
}

func (f *flaky) calls(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch name {
	case "user":
		return f.userCalls
	case "kyc":
		return f.kycCalls
	case "pin":
		return f.pinCalls
	}
	return 0
}

const (
	testUser = "11111111-1111-1111-1111-111111111111"
	emptyTTL = 5 * time.Minute
)

func newResilient(t *testing.T, down *flaky) (*Resilient, *flaky, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Now()}
	r := NewResilient(down)
	r.now = clock.get
	return r, down, clock
}

// fakeClock lets a test advance "now" across the TTL and the breaker cooldown.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) get() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestResilientServesCacheOnOutage(t *testing.T) {
	r, down, _ := newResilient(t, &flaky{})
	id := uuid.MustParse(testUser)

	if _, err := r.GetKYCProfile(context.Background(), id); err != nil {
		t.Fatalf("prime: %v", err)
	}

	down.kycErr = service.ErrIdentityUnavailable
	got, err := r.GetKYCProfile(context.Background(), id)
	if err != nil {
		t.Fatalf("expected cached answer during outage, got error: %v", err)
	}
	if got.Tier != 3 || got.FirstName != "Ada" {
		t.Errorf("degraded answer lost its value: %+v", got)
	}
	if down.calls("kyc") != 2 {
		t.Errorf("after a cache hit the downstream should only have seen the prime + outage call, saw %d", down.calls("kyc"))
	}
}

func TestResilientFailsClosedWhenNoCache(t *testing.T) {
	r, down, _ := newResilient(t, &flaky{})
	id := uuid.MustParse(testUser)

	down.kycErr = service.ErrIdentityUnavailable
	if _, err := r.GetKYCProfile(context.Background(), id); !errors.Is(err, service.ErrIdentityUnavailable) {
		t.Fatalf("no cache + outage must fail closed, got %v", err)
	}
}

func TestResilientTTLExpiryFailsClosed(t *testing.T) {
	r, down, clock := newResilient(t, &flaky{})
	id := uuid.MustParse(testUser)

	if _, err := r.GetKYCProfile(context.Background(), id); err != nil {
		t.Fatalf("prime: %v", err)
	}

	// Krone past the bounded staleness window: the door slams shut again.
	clock.advance(emptyTTL + time.Second)
	down.kycErr = service.ErrIdentityUnavailable
	if _, err := r.GetKYCProfile(context.Background(), id); !errors.Is(err, service.ErrIdentityUnavailable) {
		t.Fatalf("stale data must not be served past TTL, got %v", err)
	}
}

func TestResilientVerifyPINNeverCached(t *testing.T) {
	r, down, _ := newResilient(t, &flaky{})
	id := uuid.MustParse(testUser)

	// Prime a perfectly good KYC cache entry...
	if _, err := r.GetKYCProfile(context.Background(), id); err != nil {
		t.Fatalf("prime: %v", err)
	}
	// ...then take identity down and drive PIN verification hard.
	down.pinErr = service.ErrIdentityUnavailable
	for i := 0; i < 10; i++ {
		if ok, err := r.VerifyPIN(context.Background(), id, "1234"); err == nil || ok {
			t.Fatalf("VerifyPIN returned (%v,%v): a PIN that cannot be checked is a refusal", ok, err)
		}
	}
}

func TestResilientBreakerTripsAndFailsFast(t *testing.T) {
	r, down, _ := newResilient(t, &flaky{})
	id := uuid.MustParse(testUser)

	down.userErr = service.ErrIdentityUnavailable
	for i := 0; i < 5; i++ {
		r.GetUser(context.Background(), id) // trip the circuit (5 failures)
	}

	before := down.calls("user")
	for i := 0; i < 20; i++ {
		if _, err := r.GetUser(context.Background(), id); !errors.Is(err, service.ErrIdentityUnavailable) {
			t.Fatalf("open circuit must fail fast, got %v", err)
		}
	}
	if after := down.calls("user"); after != before {
		t.Errorf("an open circuit must not touch the downstream: calls %d -> %d", before, after)
	}
}

func TestResilientProbeRecovers(t *testing.T) {
	r, down, clock := newResilient(t, &flaky{})
	id := uuid.MustParse(testUser)

	down.userErr = service.ErrIdentityUnavailable
	for i := 0; i < 5; i++ {
		r.GetUser(context.Background(), id) // trip it
	}

	// The probe window opens after the cooldown; a good answer closes the
	// circuit and the outage is over.
	clock.advance(31 * time.Second)
	down.userErr = nil
	if _, err := r.GetUser(context.Background(), id); err != nil {
		t.Fatalf("the single probe must reach the downstream, got %v", err)
	}
	if got := r.CircuitState(); got != "closed" {
		t.Fatalf("circuit should reclose after a good probe, state=%s", got)
	}
	if down.calls("user") != 6 {
		t.Errorf("expected exactly one probe call, got %d", down.calls("user"))
	}
}

func TestResilientResolveNotFoundIsNotAFailure(t *testing.T) {
	r, down, _ := newResilient(t, &flaky{})
	down.userErr = service.ErrIdentityUserNotFound

	if _, err := r.GetUserByUsername(context.Background(), "nobody"); !errors.Is(err, service.ErrIdentityUserNotFound) {
		t.Fatalf("settled negative must come back as not found, got %v", err)
	}
	if got := r.CircuitState(); got != "closed" {
		t.Fatalf("a not-found is an answer, not an outage: state=%s", got)
	}

	// The next resolve still goes live — the circuit did not open.
	before := down.calls("user")
	down.userErr = nil
	if _, err := r.GetUserByAccountNumber(context.Background(), "8031234567"); err != nil {
		t.Fatalf("fresh resolve after a settled negative must reach identity, got %v", err)
	}
	if after := down.calls("user"); after != before+1 {
		t.Errorf("expected one live call, saw %d -> %d", before, after)
	}
}

func TestResilientResolveServesCachedDuringOutage(t *testing.T) {
	r, down, _ := newResilient(t, &flaky{})

	if _, err := r.GetUserByUsername(context.Background(), "@ada"); err != nil {
		t.Fatalf("prime: %v", err)
	}

	down.userErr = service.ErrIdentityUnavailable
	_, err := r.GetUserByUsername(context.Background(), "@ada")
	if err != nil {
		t.Fatalf("resolve should serve the cached match during an outage, got %v", err)
	}
}