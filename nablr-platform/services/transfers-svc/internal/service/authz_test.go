package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"nabla/transfers-svc/internal/models"
)

// errIdentityBoom is an unclassified identity error — neither "unavailable" nor
// "user not found" — used to prove authzError passes such an error through
// unchanged rather than swallowing it into a friendlier code.
var errIdentityBoom = errors.New("identity: internal boom")

// withIdentity returns a permissive fake mutated by f, so each case names only
// the one knob it turns.
func withIdentity(f func(*fakeIdentity)) *fakeIdentity {
	id := permissiveIdentity()
	f(id)
	return id
}

func TestAuthorize(t *testing.T) {
	ctx := context.Background()
	u := uuid.New()

	cases := []struct {
		name string
		// identity nil means no client is wired in at all — the production
		// deployment that was given no identity URL, which must fail closed.
		identity *fakeIdentity
		in       TransferInput
		wantErr  error // nil => expect success
		wantTier int32 // checked only on success
		wantPEP  bool  // checked only on success
	}{
		{
			name:     "nil identity client fails closed",
			identity: nil,
			in:       TransferInput{PIN: testPIN},
			wantErr:  models.ErrAuthorizationUnavailable,
		},
		{
			name:     "blank PIN is refused before any identity round trip",
			identity: permissiveIdentity(),
			in:       TransferInput{PIN: "   "}, // trims to empty
			wantErr:  models.ErrPINRequired,
		},
		{
			name:     "wrong PIN is rejected",
			identity: withIdentity(func(f *fakeIdentity) { f.pinOK = false }),
			in:       TransferInput{PIN: testPIN},
			wantErr:  models.ErrPINInvalid,
		},
		{
			name:     "identity unreachable during VerifyPIN fails closed",
			identity: withIdentity(func(f *fakeIdentity) { f.pinErr = ErrIdentityUnavailable }),
			in:       TransferInput{PIN: testPIN},
			wantErr:  models.ErrAuthorizationUnavailable,
		},
		{
			name:     "unclassified identity error passes through unchanged",
			identity: withIdentity(func(f *fakeIdentity) { f.pinErr = errIdentityBoom }),
			in:       TransferInput{PIN: testPIN},
			wantErr:  errIdentityBoom,
		},
		{
			name:     "suspended account cannot transact",
			identity: withIdentity(func(f *fakeIdentity) { f.status = "suspended" }),
			in:       TransferInput{PIN: testPIN},
			wantErr:  models.ErrAccountNotActive,
		},
		{
			name:     "settled user-not-found maps to account-not-active",
			identity: withIdentity(func(f *fakeIdentity) { f.userErr = ErrIdentityUserNotFound }),
			in:       TransferInput{PIN: testPIN},
			wantErr:  models.ErrAccountNotActive,
		},
		{
			name:     "identity unreachable during GetUser fails closed",
			identity: withIdentity(func(f *fakeIdentity) { f.userErr = ErrIdentityUnavailable }),
			in:       TransferInput{PIN: testPIN},
			wantErr:  models.ErrAuthorizationUnavailable,
		},
		{
			name:     "sanctioned payer is a hard block regardless of tier",
			identity: withIdentity(func(f *fakeIdentity) { f.sanctioned = true }),
			in:       TransferInput{PIN: testPIN},
			wantErr:  models.ErrSanctioned,
		},
		{
			name:     "identity unreachable during GetKYCProfile fails closed",
			identity: withIdentity(func(f *fakeIdentity) { f.kycErr = ErrIdentityUnavailable }),
			in:       TransferInput{PIN: testPIN},
			wantErr:  models.ErrAuthorizationUnavailable,
		},
		{
			name:     "off-ladder tier 0 cannot transact",
			identity: withIdentity(func(f *fakeIdentity) { f.tier = 0 }),
			in:       TransferInput{PIN: testPIN},
			wantErr:  models.ErrTierLimitExceeded,
		},
		{
			name:     "tier 1 is authorized",
			identity: withIdentity(func(f *fakeIdentity) { f.tier = 1 }),
			in:       TransferInput{PIN: testPIN},
			wantTier: 1,
		},
		{
			name:     "tier 3 happy path",
			identity: permissiveIdentity(),
			in:       TransferInput{PIN: testPIN},
			wantTier: 3,
		},
		{
			name:     "PEP is allowed and flagged for monitoring",
			identity: withIdentity(func(f *fakeIdentity) { f.pep = true }),
			in:       TransferInput{PIN: testPIN},
			wantTier: 3,
			wantPEP:  true,
		},
		{
			name:     "preauthorized transfer skips the PIN step",
			identity: withIdentity(func(f *fakeIdentity) { f.pinOK = false }), // PIN would fail if checked
			in:       TransferInput{PreAuthorized: true},                      // and none is supplied
			wantTier: 3,
		},
		{
			name:     "preauthorized still enforces account status",
			identity: withIdentity(func(f *fakeIdentity) { f.status = "suspended" }),
			in:       TransferInput{PreAuthorized: true},
			wantErr:  models.ErrAccountNotActive,
		},
		{
			name:     "preauthorized still enforces sanctions",
			identity: withIdentity(func(f *fakeIdentity) { f.sanctioned = true }),
			in:       TransferInput{PreAuthorized: true},
			wantErr:  models.ErrSanctioned,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{}
			if tc.identity != nil {
				s.WithClients(tc.identity, nil)
			}
			got, err := s.authorize(ctx, u, tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("authorize error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("authorize error = %v, want success", err)
			}
			if got.tier != tc.wantTier {
				t.Errorf("authorized tier = %d, want %d", got.tier, tc.wantTier)
			}
			if got.pep != tc.wantPEP {
				t.Errorf("authorized pep = %v, want %v", got.pep, tc.wantPEP)
			}
		})
	}
}

// TestAuthorizePIN covers the dedicated transaction-PIN step that stands in
// front of a send and mints the single-use token POST /transfers consumes.
// Every REFUSAL returns before the row is written, so these run without Postgres
// (the happy path — a token actually minted — needs the database and is proved
// end-to-end over HTTP). What is pinned here is the whole reason the PIN was
// split onto its own endpoint: a failed PIN is answered DISTINCTLY — wrong (so
// re-enter) vs not-set (so go set one) vs locked (so wait or reset) — and a PIN
// that cannot be verified FAILS CLOSED into a 503 rather than minting a token.
func TestAuthorizePIN(t *testing.T) {
	ctx := context.Background()
	u := uuid.New()
	const amount = 25000

	cases := []struct {
		name     string
		identity *fakeIdentity // nil => no client wired in at all
		in       AuthorizePINInput
		wantErr  error
	}{
		{
			name:     "nil identity client fails closed",
			identity: nil,
			in:       AuthorizePINInput{PIN: testPIN, AmountMinor: amount, Currency: "NGN"},
			wantErr:  models.ErrAuthorizationUnavailable,
		},
		{
			name:     "blank PIN is refused before any identity round trip",
			identity: permissiveIdentity(),
			in:       AuthorizePINInput{PIN: "   ", AmountMinor: amount, Currency: "NGN"},
			wantErr:  models.ErrPINRequired,
		},
		{
			name:     "non-positive amount is refused",
			identity: permissiveIdentity(),
			in:       AuthorizePINInput{PIN: testPIN, AmountMinor: 0, Currency: "NGN"},
			wantErr:  models.ErrZeroAmount,
		},
		{
			name:     "wrong PIN is incorrect, not not-set",
			identity: withIdentity(func(f *fakeIdentity) { f.pinOK = false }),
			in:       AuthorizePINInput{PIN: testPIN, AmountMinor: amount, Currency: "NGN"},
			wantErr:  models.ErrPINInvalid,
		},
		{
			name:     "not-set PIN passes through distinctly (identity answering, not an outage)",
			identity: withIdentity(func(f *fakeIdentity) { f.pinErr = models.ErrPINNotSet }),
			in:       AuthorizePINInput{PIN: testPIN, AmountMinor: amount, Currency: "NGN"},
			wantErr:  models.ErrPINNotSet,
		},
		{
			name:     "locked PIN passes through distinctly",
			identity: withIdentity(func(f *fakeIdentity) { f.pinErr = models.ErrPINLocked }),
			in:       AuthorizePINInput{PIN: testPIN, AmountMinor: amount, Currency: "NGN"},
			wantErr:  models.ErrPINLocked,
		},
		{
			name:     "identity unreachable during VerifyPIN fails closed",
			identity: withIdentity(func(f *fakeIdentity) { f.pinErr = ErrIdentityUnavailable }),
			in:       AuthorizePINInput{PIN: testPIN, AmountMinor: amount, Currency: "NGN"},
			wantErr:  models.ErrAuthorizationUnavailable,
		},
		{
			name:     "unclassified identity error passes through unchanged",
			identity: withIdentity(func(f *fakeIdentity) { f.pinErr = errIdentityBoom }),
			in:       AuthorizePINInput{PIN: testPIN, AmountMinor: amount, Currency: "NGN"},
			wantErr:  errIdentityBoom,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{}
			if tc.identity != nil {
				s.WithClients(tc.identity, nil)
			}
			// A refusal must never reach the row-writing call: this Service has no
			// pool, so if a case fell through to CreatePINAuthorization it would not
			// return tc.wantErr. That is the assertion — the guard order holds.
			_, err := s.AuthorizePIN(ctx, u, tc.in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("AuthorizePIN error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestCBNTierLadder pins the confirmed CBN Tier 1/2/3 ceilings (in kobo) and the
// two structural facts the engine depends on: Tier 3 carries noBalanceCap (no cap
// row is ever written for it), and Tier 0 has no rung at all (an unverified
// customer has no allowance, so authorize refuses them before any row exists).
func TestCBNTierLadder(t *testing.T) {
	want := map[int32]tierCeilings{
		1: {perTransactionMinor: 50_000_00, dailyOutboundMinor: 50_000_00, balanceCapMinor: 300_000_00},
		2: {perTransactionMinor: 200_000_00, dailyOutboundMinor: 500_000_00, balanceCapMinor: 5_000_000_00},
		3: {perTransactionMinor: 5_000_000_00, dailyOutboundMinor: 10_000_000_00, balanceCapMinor: noBalanceCap},
	}
	for tier, w := range want {
		got, ok := cbnTierLadder[tier]
		if !ok {
			t.Fatalf("tier %d missing from ladder", tier)
		}
		if got != w {
			t.Errorf("tier %d ceilings = %+v, want %+v", tier, got, w)
		}
	}
	if _, ok := cbnTierLadder[0]; ok {
		t.Error("tier 0 (unverified) must have no rung on the ladder")
	}
	if cbnTierLadder[3].balanceCapMinor != noBalanceCap {
		t.Error("tier 3 must carry noBalanceCap so no balance_cap row is materialised")
	}
}
