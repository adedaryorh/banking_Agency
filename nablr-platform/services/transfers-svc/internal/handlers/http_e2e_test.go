package handlers_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nabla/transfers-svc/internal/handlers"
	"nabla/transfers-svc/internal/providers"
	"nabla/transfers-svc/internal/routes"
	"nabla/transfers-svc/internal/service"
)

const (
	testJWTSecret  = "http-e2e-jwt-secret"
	testJWTIssuer  = "identity-svc-test"
	testHookSecret = "http-e2e-webhook-secret"
	ngnHTTP        = "NGN"
	httpTestPIN    = "1234"
	schemaHTTPPath = "../../db/schema.sql"
)

const truncateHTTP = `TRUNCATE
  provider_webhooks, provider_requests, outbox_events,
  scheduled_payment_runs, scheduled_payments,
  transfer_pin_authorizations,
  transfer_events, transfer_quotes, transfers,
  holds, ledger_entries, ledger_transactions,
  beneficiaries, customer_limits, wallets
  RESTART IDENTITY CASCADE`

// ---------------------------------------------------------------------------
// Token + request + webhook-signature helpers
// ---------------------------------------------------------------------------

// mintToken signs an HS256 JWT the real JWTMiddleware accepts: sub is the user
// id, iss matches the configured issuer, and exp is present (the middleware
// requires expiration).
func mintToken(t *testing.T, user uuid.UUID) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  user.String(),
		"iss":  testJWTIssuer,
		"role": "buyer",
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	signed, err := tok.SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func jsonBytes(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return b
}

func doReq(r http.Handler, method, path, token string, headers map[string]string, body []byte) *httptest.ResponseRecorder {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// signWebhook produces the headers the real verifier requires: HMAC-SHA256 over
// "<timestamp>.<body>" with the shared secret, and the timestamp itself.
func signWebhook(body []byte, ts time.Time) map[string]string {
	tsStr := ts.UTC().Format(time.RFC3339)
	mac := hmac.New(sha256.New, []byte(testHookSecret))
	mac.Write([]byte(tsStr))
	mac.Write([]byte("."))
	mac.Write(body)
	return map[string]string{
		"X-Webhook-Timestamp": tsStr,
		"X-Webhook-Signature": hex.EncodeToString(mac.Sum(nil)),
	}
}

func decode(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
}

type moneyView struct {
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Formatted string `json:"formatted"`
}

type transferView struct {
	ID         string    `json:"id"`
	Reference  string    `json:"reference"`
	Status     string    `json:"status"`
	SendAmount moneyView `json:"send_amount"`
}

// for requests that never reach the database — the tests using it are written to
// stop at auth, at a 501, at a pre-DB 400, or at the webhook signature gate.
func newTransportRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	svc := service.NewWithProvider(nil, providers.NewMockProvider())
	th := handlers.NewTransferHandler(svc)
	wh := handlers.NewWebhookHandler(svc, testHookSecret)
	return routes.New(testJWTSecret, testJWTIssuer, th, wh)
}

func TestHTTPTransport_HealthIsUnauthenticated(t *testing.T) {
	r := newTransportRouter()
	w := doReq(r, http.MethodGet, "/health", "", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /health = %d, want 200 (health must not require a token)", w.Code)
	}
}

// The ops surface must be reachable without a customer token and carry no
// customer data: /metrics is scraped by the collector, /health/providers is a
// read-only rail status blob.
func TestHTTPTransport_OpsSurfaceIsUnauthenticated(t *testing.T) {
	r := newTransportRouter()

	if w := doReq(r, http.MethodGet, "/metrics", "", nil, nil); w.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", w.Code)
	}
	w := doReq(r, http.MethodGet, "/health/providers", "", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /health/providers = %d, want 200", w.Code)
	}
	var body []map[string]any
	decode(t, w, &body)
	if len(body) == 0 || body[0]["name"] == nil {
		t.Fatalf("GET /health/providers body=%v, want a provider-name keyed list", body)
	}
}

func TestHTTPTransport_AuthGate(t *testing.T) {
	r := newTransportRouter()

	if w := doReq(r, http.MethodGet, "/api/v1/transfers", "", nil, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: GET /api/v1/transfers = %d, want 401", w.Code)
	}
	// The "who are you paying?" surface is mounted on the same protected group
	// and must refuse anonymous callers the same way.
	if w := doReq(r, http.MethodGet, "/api/v1/pay/resolve?q=bode", "", nil, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: GET /api/v1/pay/resolve?q=bode = %d, want 401", w.Code)
	}
	// A malformed token must not authenticate.
	if w := doReq(r, http.MethodGet, "/api/v1/transfers", "not-a-jwt", nil, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("bad token: GET /api/v1/transfers = %d, want 401", w.Code)
	}
	// A token signed with the wrong secret must not authenticate.
	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": uuid.New().String(), "iss": testJWTIssuer, "exp": time.Now().Add(time.Hour).Unix(),
	})
	wrongKey, _ := forged.SignedString([]byte("the-wrong-secret"))
	if w := doReq(r, http.MethodGet, "/api/v1/transfers", wrongKey, nil, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong-secret token: GET /api/v1/transfers = %d, want 401", w.Code)
	}
}

// The formerly-deferred routes are implemented now. Two things are worth
// pinning here rather than in a unit test:
//
//   - validation-first endpoints answer 400 (account_number/code/beneficiary_id
//     missing) before anything touches a database, so they hold over a nil pool;
//   - the database-backed ones answer 500 over the nil pool of this router, NOT
//     501 and NOT 404 — they are wired to the real engine, and the absence of a
//     pool is a test artifact, not a missing route.
//
// (PATCH /beneficiaries/:id is deliberately NOT here: it is implemented as
// presentation-only — nickname/favourite/save — which is not the
// redirection-fraud vector that editable account details are.)
func TestHTTPTransport_DeferredRoutesAreImplemented(t *testing.T) {
	r := newTransportRouter()
	token := mintToken(t, uuid.New())
	id := uuid.New().String()

	validationOnly := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/banks/suggest"},
		{http.MethodGet, "/api/v1/banks/timing"},
		{http.MethodGet, "/api/v1/transfers/suggestions"},
		{http.MethodPost, "/api/v1/scheduled-payments"},
		{http.MethodPatch, "/api/v1/scheduled-payments/" + id},
	}
	for _, rt := range validationOnly {
		w := doReq(r, rt.method, rt.path, token, nil, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400", rt.method, rt.path, w.Code)
		}
	}

	dbBacked := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/banks"},
		{http.MethodGet, "/api/v1/scheduled-payments"},
		{http.MethodGet, "/api/v1/scheduled-payments/" + id},
		{http.MethodPost, "/api/v1/scheduled-payments/" + id + "/cancel"},
		{http.MethodPost, "/api/v1/scheduled-payments/" + id + "/pause"},
		{http.MethodPost, "/api/v1/scheduled-payments/" + id + "/resume"},
		{http.MethodGet, "/api/v1/scheduled-payments/" + id + "/runs"},
	}
	for _, rt := range dbBacked {
		w := doReq(r, rt.method, rt.path, token, nil, nil)
		if w.Code == http.StatusNotImplemented || w.Code == http.StatusNotFound {
			t.Errorf("%s %s = %d, want a wired (500-over-nil-pool) answer, not a stub", rt.method, rt.path, w.Code)
		}
	}
}

// These 400s are decided before any wallet, token or ledger is touched, so they
// hold even over a nil pool — and they guard the ways a client can ask us to move
// money without saying how much or to whom. The quote_id that used to be
// mandatory here is gone: the send flow now takes the amount directly (Figma:
// resolve → amount → confirm → PIN → transfer).
func TestHTTPTransport_TransferValidation(t *testing.T) {
	r := newTransportRouter()
	token := mintToken(t, uuid.New())
	payee := uuid.New().String()

	// Idempotency-Key is mandatory: without it a retry would be a second payment.
	// (Checked before the amount, so a complete body still 400s on the missing key.)
	w := doReq(r, http.MethodPost, "/api/v1/transfers", token, nil,
		jsonBytes(t, map[string]any{"amount": "10.00", "beneficiary_id": payee}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing Idempotency-Key = %d, want 400", w.Code)
	}

	// Amount is mandatory: the quote used to carry it, now the request must.
	w = doReq(r, http.MethodPost, "/api/v1/transfers", token,
		map[string]string{"Idempotency-Key": "k-no-amount"},
		jsonBytes(t, map[string]any{"beneficiary_id": payee}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing amount = %d, want 400", w.Code)
	}

	// A recipient is mandatory: neither a saved beneficiary_id nor inline details.
	w = doReq(r, http.MethodPost, "/api/v1/transfers", token,
		map[string]string{"Idempotency-Key": "k-no-recipient"},
		jsonBytes(t, map[string]any{"amount": "10.00"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing recipient = %d, want 400", w.Code)
	}
}

// The authorize-pin step has the same pre-DB validation as the send it precedes
// (amount + a named recipient), and it FAILS CLOSED: this router wires no identity
// client, so a well-formed request that would verify a PIN gets a 503 rather than
// a token. All three answers are decided without a database.
func TestHTTPTransport_AuthorizePINValidation(t *testing.T) {
	r := newTransportRouter()
	token := mintToken(t, uuid.New())
	payee := uuid.New().String()

	// Amount is required.
	w := doReq(r, http.MethodPost, "/api/v1/transfers/authorize-pin", token, nil,
		jsonBytes(t, map[string]any{"pin": httpTestPIN, "beneficiary_id": payee}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("authorize-pin missing amount = %d, want 400", w.Code)
	}

	// A recipient is required.
	w = doReq(r, http.MethodPost, "/api/v1/transfers/authorize-pin", token, nil,
		jsonBytes(t, map[string]any{"pin": httpTestPIN, "amount": "10.00"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("authorize-pin missing recipient = %d, want 400", w.Code)
	}

	// A customer_id that is not a UUID is rejected up front — before any identity
	// round trip — so a malformed payee never mints a token or fails closed as 503.
	w = doReq(r, http.MethodPost, "/api/v1/transfers/authorize-pin", token, nil,
		jsonBytes(t, map[string]any{"pin": httpTestPIN, "amount": "10.00", "customer_id": "not-a-uuid"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("authorize-pin bad customer_id = %d, want 400", w.Code)
	}

	// Well-formed, but no identity client is wired: fail closed with 503, no token.
	w = doReq(r, http.MethodPost, "/api/v1/transfers/authorize-pin", token, nil,
		jsonBytes(t, map[string]any{"pin": httpTestPIN, "amount": "10.00", "beneficiary_id": payee}))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("authorize-pin with no identity wired = %d, want 503 (fail closed)", w.Code)
	}
}

// The webhook signature gate, exercised without a database: an unsigned or
// stale-timestamped delivery is rejected before any state change, and a
// correctly-signed but in-flight event is acknowledged without acting. A
// classifiable settle/fail is deliberately NOT sent here — that path reaches the
// engine and belongs to the DB-backed suite.
func TestHTTPTransport_WebhookSignatureGate(t *testing.T) {
	r := newTransportRouter()
	body := jsonBytes(t, map[string]any{"event": "payout.settled", "provider_reference": "ref-1"})

	// No signature headers at all.
	if w := doReq(r, http.MethodPost, "/webhooks/provider/payout", "", nil, body); w.Code != http.StatusUnauthorized {
		t.Errorf("unsigned webhook = %d, want 401", w.Code)
	}

	// Correctly signed, but the timestamp is far outside the replay window.
	stale := signWebhook(body, time.Now().Add(-30*time.Minute))
	if w := doReq(r, http.MethodPost, "/webhooks/provider/payout", "", stale, body); w.Code != http.StatusUnauthorized {
		t.Errorf("stale-timestamp webhook = %d, want 401 (replay must be refused)", w.Code)
	}

	// Signature over different bytes than the body (tamper): must fail.
	tampered := signWebhook([]byte(`{"event":"payout.settled"}`), time.Now())
	if w := doReq(r, http.MethodPost, "/webhooks/provider/payout", "", tampered, body); w.Code != http.StatusUnauthorized {
		t.Errorf("tampered-body webhook = %d, want 401", w.Code)
	}

	// Valid signature, fresh timestamp, but an in-flight event we do not act on:
	// acknowledged (200) without ever reaching the engine.
	inflight := jsonBytes(t, map[string]any{"event": "payout.processing", "provider_reference": "ref-1"})
	hdrs := signWebhook(inflight, time.Now())
	if w := doReq(r, http.MethodPost, "/webhooks/provider/payout", "", hdrs, inflight); w.Code != http.StatusOK {
		t.Errorf("signed in-flight webhook = %d, want 200 (acknowledged, not acted on)", w.Code)
	}
}

// ---------------------------------------------------------------------------
// DB-backed HTTP harness
// ---------------------------------------------------------------------------

// httpFakeIdentity is the permissive identity-svc stand-in for the DB-backed HTTP
// flows: an active Tier 3 customer whose PIN always verifies. These tests prove
// the transport -> engine -> database wiring, so the control plane must not gate
// them; its refusals (bad PIN, suspended, sanctions, fail-closed) are covered in
// the service package's authz_test.go.
type httpFakeIdentity struct{}

func (httpFakeIdentity) VerifyPIN(context.Context, uuid.UUID, string) (bool, error) {
	return true, nil
}

func (httpFakeIdentity) GetUser(_ context.Context, u uuid.UUID) (service.UserProfile, error) {
	return service.UserProfile{UserID: u.String(), Status: "active"}, nil
}

func (httpFakeIdentity) GetKYCProfile(_ context.Context, u uuid.UUID) (service.KYCProfile, error) {
	return service.KYCProfile{UserID: u.String(), Tier: 3}, nil
}

func (httpFakeIdentity) GetUserByUsername(_ context.Context, _ string) (service.UserProfile, error) {
	return service.UserProfile{}, service.ErrIdentityUserNotFound
}

func (httpFakeIdentity) GetUserByAccountNumber(_ context.Context, _ string) (service.UserProfile, error) {
	return service.UserProfile{}, service.ErrIdentityUserNotFound
}

type httpHarness struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
	svc  *service.Service
	mock *providers.MockProvider
	r    *gin.Engine
}

func newHTTPHarness(t *testing.T, settleDelay time.Duration) *httpHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dsn := os.Getenv("TRANSFERS_TEST_DSN")
	if dsn == "" {
		t.Skip("set TRANSFERS_TEST_DSN to a disposable database to run HTTP end-to-end tests")
	}
	ctx := context.Background()
	loadSchemaHTTP(t, ctx, dsn)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect pool: %v", err)
	}
	if _, err := pool.Exec(ctx, truncateHTTP); err != nil {
		pool.Close()
		t.Fatalf("truncate: %v", err)
	}
	mock := providers.NewMockProviderWithSink(nil, settleDelay)
	svc := service.NewWithProvider(pool, mock).WithClients(httpFakeIdentity{}, nil)
	th := handlers.NewTransferHandler(svc)
	wh := handlers.NewWebhookHandler(svc, testHookSecret)
	r := routes.New(testJWTSecret, testJWTIssuer, th, wh)

	t.Cleanup(pool.Close)
	return &httpHarness{t: t, ctx: ctx, pool: pool, svc: svc, mock: mock, r: r}
}

// loadSchemaHTTP applies db/schema.sql on a simple-protocol connection: a
// multi-statement DDL file cannot go through the extended protocol the pool
// uses. CREATE ... IF NOT EXISTS makes it idempotent across tests.
func loadSchemaHTTP(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	ddl, err := os.ReadFile(schemaHTTPPath)
	if err != nil {
		t.Fatalf("read schema %s: %v", schemaHTTPPath, err)
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect for schema load: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, string(ddl)); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
}

// --- fixtures: seed the state a transfer needs to be legal, using only the
// exported engine API plus raw SQL for what has no service method. ---

func (h *httpHarness) fundWallet(user uuid.UUID, minor int64) uuid.UUID {
	h.t.Helper()
	w, err := h.svc.CreateWallet(h.ctx, user, ngnHTTP)
	if err != nil {
		h.t.Fatalf("create wallet: %v", err)
	}
	if _, err := h.pool.Exec(h.ctx, `UPDATE wallets SET available_minor=$2 WHERE id=$1`, w.ID, minor); err != nil {
		h.t.Fatalf("fund wallet: %v", err)
	}
	return w.ID
}

func (h *httpHarness) generousLimits(user uuid.UUID) {
	h.t.Helper()
	// Written as source='override' rows so they survive the tier materialisation
	// the transfer path runs, matching the versioned customer_limits shape.
	limits := []struct {
		limitType string
		amount    int64
	}{
		{"per_transaction", 1_000_000_00},
		{"daily_outbound", 10_000_000_00},
		{"balance_cap", 100_000_000_00},
	}
	for _, l := range limits {
		if _, err := h.pool.Exec(h.ctx,
			`INSERT INTO customer_limits (user_id, limit_type, currency, amount_minor, source, reason)
			 VALUES ($1, $2, $3, $4, 'override', 'http e2e fixture')
			 ON CONFLICT (user_id, limit_type, currency) WHERE effective_to IS NULL
			 DO UPDATE SET amount_minor = EXCLUDED.amount_minor`,
			user, l.limitType, ngnHTTP, l.amount); err != nil {
			h.t.Fatalf("set limit %s: %v", l.limitType, err)
		}
	}
}

func (h *httpHarness) agePayee(id uuid.UUID) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.ctx, `UPDATE beneficiaries SET cooling_period_ends_at=NULL WHERE id=$1`, id); err != nil {
		h.t.Fatalf("age payee: %v", err)
	}
}

func (h *httpHarness) bankPayee(owner uuid.UUID, account, bankCode string) uuid.UUID {
	h.t.Helper()
	b, err := h.svc.AddBeneficiary(h.ctx, owner, service.BeneficiaryInput{
		Type: "bank", BankCode: bankCode, AccountNumber: account, AccountName: "Test Payee", Currency: ngnHTTP,
	})
	if err != nil {
		h.t.Fatalf("add bank payee: %v", err)
	}
	h.agePayee(b.ID)
	return b.ID
}

func (h *httpHarness) internalPayee(owner, recipient uuid.UUID) uuid.UUID {
	h.t.Helper()
	b, err := h.svc.AddBeneficiary(h.ctx, owner, service.BeneficiaryInput{
		Type: "internal", RecipientUserID: &recipient, AccountName: "Internal Payee", Currency: ngnHTTP,
	})
	if err != nil {
		h.t.Fatalf("add internal payee: %v", err)
	}
	h.agePayee(b.ID)
	return b.ID
}

func (h *httpHarness) walletBalance(walletID uuid.UUID) (available, reserved int64) {
	h.t.Helper()
	if err := h.pool.QueryRow(h.ctx,
		`SELECT available_minor, reserved_minor FROM wallets WHERE id=$1`, walletID).
		Scan(&available, &reserved); err != nil {
		h.t.Fatalf("read balance: %v", err)
	}
	return available, reserved
}

func (h *httpHarness) providerRef(transferID uuid.UUID) string {
	h.t.Helper()
	var ref string
	if err := h.pool.QueryRow(h.ctx,
		`SELECT coalesce(provider_reference,'') FROM transfers WHERE id=$1`, transferID).Scan(&ref); err != nil {
		h.t.Fatalf("read provider reference: %v", err)
	}
	return ref
}

func (h *httpHarness) ledgerSums() (debits, credits int64) {
	h.t.Helper()
	if err := h.pool.QueryRow(h.ctx, `
		SELECT
		  coalesce(sum(amount_minor) FILTER (WHERE entry_type='debit'), 0),
		  coalesce(sum(amount_minor) FILTER (WHERE entry_type='credit'), 0)
		FROM ledger_entries`).Scan(&debits, &credits); err != nil {
		h.t.Fatalf("ledger sums: %v", err)
	}
	return debits, credits
}

func (h *httpHarness) countRows(table string) int64 {
	h.t.Helper()
	var n int64
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		h.t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// authorizePIN runs the dedicated PIN step (POST /transfers/authorize-pin) for a
// saved payee and returns the minted single-use token id. The PIN is carried in
// the body exactly as a real client sends it; the fake identity verifies it. The
// amount and recipient are the SAME ones the following send presents, so the
// token binds to that payment.
func (h *httpHarness) authorizePIN(token string, payee uuid.UUID, amount string) string {
	h.t.Helper()
	w := doReq(h.r, http.MethodPost, "/api/v1/transfers/authorize-pin", token, nil,
		jsonBytes(h.t, map[string]any{"beneficiary_id": payee.String(), "amount": amount, "currency": ngnHTTP, "pin": httpTestPIN}))
	if w.Code != http.StatusCreated {
		h.t.Fatalf("POST /authorize-pin = %d, want 201: %s", w.Code, w.Body.String())
	}
	var resp struct {
		PINToken string    `json:"pin_token"`
		Amount   moneyView `json:"amount"`
	}
	decode(h.t, w, &resp)
	if resp.PINToken == "" {
		h.t.Fatalf("authorize-pin returned no token: %s", w.Body.String())
	}
	return resp.PINToken
}

// sendWithToken runs POST /transfers for a saved payee, presenting a PIN
// authorization token (not an inline PIN) and an idempotency key. Amount and
// beneficiary are sent directly — there is no quote — and the token is what
// proves the PIN.
func (h *httpHarness) sendWithToken(token string, payee uuid.UUID, amount, pinToken, idemKey string) *httptest.ResponseRecorder {
	h.t.Helper()
	return doReq(h.r, http.MethodPost, "/api/v1/transfers", token,
		map[string]string{"Idempotency-Key": idemKey},
		jsonBytes(h.t, map[string]any{"beneficiary_id": payee.String(), "amount": amount, "currency": ngnHTTP, "pin_token": pinToken}))
}

// send is the whole Figma flow for a saved payee in one call: authorize the PIN
// on its own step to mint a token, then transfer against that token. It is what
// most DB-backed tests use when the token lifecycle itself is not the subject.
func (h *httpHarness) send(token string, payee uuid.UUID, amount, idemKey string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.sendWithToken(token, payee, amount, h.authorizePIN(token, payee, amount), idemKey)
}

// sendInlinePIN runs POST /transfers with the back-compatible inline `pin` and no
// token — the direct-PIN path the service still accepts for a client that has not
// moved to the authorize-pin step.
func (h *httpHarness) sendInlinePIN(token string, payee uuid.UUID, amount, idemKey string) *httptest.ResponseRecorder {
	h.t.Helper()
	return doReq(h.r, http.MethodPost, "/api/v1/transfers", token,
		map[string]string{"Idempotency-Key": idemKey},
		jsonBytes(h.t, map[string]any{"beneficiary_id": payee.String(), "amount": amount, "currency": ngnHTTP, "pin": httpTestPIN}))
}

// authorizePINByCustomerID mints a token for a payee addressed by customer_id
// (the id GET /pay/resolve returns) — the alternative to a saved beneficiary_id.
// The customer_id is folded to the same internal descriptor the send uses, so the
// token binds to the payment exactly as the beneficiary_id path does.
func (h *httpHarness) authorizePINByCustomerID(token, customerID, amount string) string {
	h.t.Helper()
	w := doReq(h.r, http.MethodPost, "/api/v1/transfers/authorize-pin", token, nil,
		jsonBytes(h.t, map[string]any{"customer_id": customerID, "amount": amount, "currency": ngnHTTP, "pin": httpTestPIN}))
	if w.Code != http.StatusCreated {
		h.t.Fatalf("POST /authorize-pin (customer_id) = %d, want 201: %s", w.Code, w.Body.String())
	}
	var resp struct {
		PINToken string `json:"pin_token"`
	}
	decode(h.t, w, &resp)
	if resp.PINToken == "" {
		h.t.Fatalf("authorize-pin (customer_id) returned no token: %s", w.Body.String())
	}
	return resp.PINToken
}

// sendByCustomerID is the whole resolve -> pay flow for a Nablr user who is NOT a
// saved beneficiary: authorize the PIN by customer_id, then transfer by the same
// customer_id. The service materialises — or, on a repeat, reuses — an ephemeral
// pay-once payee.
func (h *httpHarness) sendByCustomerID(token, customerID, amount, idemKey string) *httptest.ResponseRecorder {
	h.t.Helper()
	pinToken := h.authorizePINByCustomerID(token, customerID, amount)
	return doReq(h.r, http.MethodPost, "/api/v1/transfers", token,
		map[string]string{"Idempotency-Key": idemKey},
		jsonBytes(h.t, map[string]any{"customer_id": customerID, "amount": amount, "currency": ngnHTTP, "pin_token": pinToken}))
}

// expirePINToken backdates a minted token's expiry so the next consume attempt
// finds no live row — without waiting out the real TTL.
func (h *httpHarness) expirePINToken(pinToken string) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.ctx,
		`UPDATE transfer_pin_authorizations SET expires_at = now() - interval '1 minute' WHERE id=$1`, pinToken); err != nil {
		h.t.Fatalf("expire pin token: %v", err)
	}
}

// ---------------------------------------------------------------------------
// DB-backed end-to-end flows
// ---------------------------------------------------------------------------

// An internal transfer settles synchronously: authorize the PIN, then send, and
// the money has already moved when the 201 is written. The response shape, the
// persisted status read back over HTTP, and the wallet balance must all agree.
func TestHTTPE2E_InternalTransfer_AuthorizePINThenSendCompletes(t *testing.T) {
	h := newHTTPHarness(t, time.Hour)
	sender, recipient := uuid.New(), uuid.New()
	srcWallet := h.fundWallet(sender, 1_000_000_00)
	h.fundWallet(recipient, 0)
	h.generousLimits(sender)
	h.generousLimits(recipient)
	payee := h.internalPayee(sender, recipient)
	token := mintToken(t, sender)

	// Authorize the PIN for 250.00 -> 25000 minor, then send against the token.
	// An internal transfer is already complete, so the contract is 201.
	w := h.send(token, payee, "250.00", "int-key-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /transfers = %d, want 201: %s", w.Code, w.Body.String())
	}
	var tr transferView
	decode(t, w, &tr)
	if tr.Status != "completed" {
		t.Errorf("status = %q, want completed", tr.Status)
	}
	if tr.SendAmount.Amount != 25000 {
		t.Errorf("transfer send amount = %d, want 25000", tr.SendAmount.Amount)
	}
	if tr.SendAmount.Formatted != "NGN 250.00" {
		t.Errorf("transfer formatted = %q, want \"NGN 250.00\"", tr.SendAmount.Formatted)
	}

	// Read it back over HTTP: same status.
	w = doReq(h.r, http.MethodGet, "/api/v1/transfers/"+tr.ID, token, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /transfers/:id = %d, want 200", w.Code)
	}
	var got transferView
	decode(t, w, &got)
	if got.Status != "completed" {
		t.Errorf("read-back status = %q, want completed", got.Status)
	}

	// Status endpoint agrees.
	w = doReq(h.r, http.MethodGet, "/api/v1/transfers/"+tr.ID+"/status", token, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /transfers/:id/status = %d, want 200", w.Code)
	}
	var status struct {
		Status string `json:"status"`
	}
	decode(t, w, &status)
	if status.Status != "completed" {
		t.Errorf("status endpoint = %q, want completed", status.Status)
	}

	// The money moved exactly once, and the ledger balances.
	avail, reserved := h.walletBalance(srcWallet)
	if avail != 1_000_000_00-25000 || reserved != 0 {
		t.Errorf("sender wallet available=%d reserved=%d, want %d/0", avail, reserved, 1_000_000_00-25000)
	}
	if debits, credits := h.ledgerSums(); debits != credits || debits != 25000 {
		t.Errorf("ledger debits=%d credits=%d, want 25000/25000", debits, credits)
	}
}

// Regression: a Nablr user paid by customer_id (not a saved beneficiary) must be
// payable AGAIN. The first send mints an ephemeral pay-once payee; the uniqueness
// index allows only one non-deleted row per recipient, so the second send has to
// REUSE that row rather than collide on the index and fail with 409 "beneficiary
// already exists" — the bug this covers. Both sends complete, and exactly one
// beneficiary row backs them.
func TestHTTPE2E_RepeatCustomerIDSendReusesPayee(t *testing.T) {
	h := newHTTPHarness(t, time.Hour)
	sender, recipient := uuid.New(), uuid.New()
	srcWallet := h.fundWallet(sender, 1_000_000_00)
	h.fundWallet(recipient, 0)
	h.generousLimits(sender)
	h.generousLimits(recipient)
	token := mintToken(t, sender)
	customerID := recipient.String() // the id GET /pay/resolve would return

	before := h.countRows("beneficiaries")

	// First payment to a brand-new payee: mints the ephemeral row.
	first := h.sendByCustomerID(token, customerID, "100.00", "cust-key-1")
	if first.Code != http.StatusCreated {
		t.Fatalf("first customer_id send = %d, want 201: %s", first.Code, first.Body.String())
	}
	// Second payment to the SAME user: must reuse the row, not 409.
	second := h.sendByCustomerID(token, customerID, "150.00", "cust-key-2")
	if second.Code != http.StatusCreated {
		t.Fatalf("second customer_id send = %d, want 201 (reuse the payee, not 409): %s",
			second.Code, second.Body.String())
	}

	// One recipient, one beneficiary row — the repeat send did not mint a second.
	if got := h.countRows("beneficiaries") - before; got != 1 {
		t.Errorf("beneficiaries created across two sends = %d, want 1 (payee reused)", got)
	}
	// Both sends debited the sender: 100.00 + 150.00 = 250.00.
	if avail, reserved := h.walletBalance(srcWallet); avail != 1_000_000_00-25000 || reserved != 0 {
		t.Errorf("sender available=%d reserved=%d, want %d/0 (both sends settled)",
			avail, reserved, 1_000_000_00-25000)
	}
}

// A double-tapped Send over HTTP — the identical request, same token and same
// Idempotency-Key — is one payment. Both requests return the same transfer and
// the money moves once. The second request never reaches the token consume: the
// idempotency replay at the top of Transfer returns the original first, which is
// exactly why a re-tap of a single-use token does not 409.
func TestHTTPE2E_IdempotentReplayReturnsSameTransfer(t *testing.T) {
	h := newHTTPHarness(t, time.Hour)
	sender, recipient := uuid.New(), uuid.New()
	srcWallet := h.fundWallet(sender, 1_000_000_00)
	h.fundWallet(recipient, 0)
	h.generousLimits(sender)
	h.generousLimits(recipient)
	payee := h.internalPayee(sender, recipient)
	token := mintToken(t, sender)

	pinToken := h.authorizePIN(token, payee, "100.00")

	first := h.sendWithToken(token, payee, "100.00", pinToken, "replay-key")
	if first.Code != http.StatusCreated {
		t.Fatalf("first send = %d, want 201: %s", first.Code, first.Body.String())
	}
	var a transferView
	decode(t, first, &a)

	// Replay the identical request with the same key and the same token.
	second := h.sendWithToken(token, payee, "100.00", pinToken, "replay-key")
	if second.Code != http.StatusCreated {
		t.Fatalf("replay send = %d, want 201: %s", second.Code, second.Body.String())
	}
	var b transferView
	decode(t, second, &b)

	if a.ID != b.ID {
		t.Errorf("replay returned transfer %s, first returned %s: a replay must resolve to one payment", b.ID, a.ID)
	}
	avail, _ := h.walletBalance(srcWallet)
	if avail != 1_000_000_00-10000 {
		t.Errorf("sender available=%d, want %d: a replay must not move money twice", avail, 1_000_000_00-10000)
	}
	if n := h.countRows("transfers"); n != 1 {
		t.Errorf("transfers=%d, want 1", n)
	}
}

// A bank payout is accepted (202), dispatched by the worker, then settled by a
// signed provider webhook — the full asynchronous path over HTTP. Only an
// explicit settlement event completes it, and the hold is captured, not
// released back to the customer.
func TestHTTPE2E_BankTransfer_AcceptedThenSettledByWebhook(t *testing.T) {
	h := newHTTPHarness(t, time.Hour)
	sender := uuid.New()
	srcWallet := h.fundWallet(sender, 1_000_000_00)
	h.generousLimits(sender)
	payee := h.bankPayee(sender, "0123456790", "058")
	token := mintToken(t, sender)

	// Authorize the PIN for 500.00 -> 50000 (a plain amount the mock accepts and
	// settles slowly), then send. A bank payout is still in flight, so it is 202.
	w := h.send(token, payee, "500.00", "bank-key-1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST /transfers = %d, want 202: %s", w.Code, w.Body.String())
	}
	var tr transferView
	decode(t, w, &tr)
	if tr.Status != "pending" {
		t.Errorf("status = %q, want pending", tr.Status)
	}
	transferID := uuid.MustParse(tr.ID)

	// The money is reserved, not yet gone.
	if _, reserved := h.walletBalance(srcWallet); reserved != 50000 {
		t.Fatalf("reserved=%d, want 50000 after accept", reserved)
	}

	// The worker dispatches to the rail (no HTTP route drives this).
	if err := h.svc.DispatchOutbox(h.ctx, 10); err != nil {
		t.Fatalf("DispatchOutbox: %v", err)
	}
	ref := h.providerRef(transferID)
	if ref == "" {
		t.Fatal("no provider reference recorded after dispatch")
	}

	// Over HTTP the transfer now reads processing.
	w = doReq(h.r, http.MethodGet, "/api/v1/transfers/"+tr.ID, token, nil, nil)
	var afterDispatch transferView
	decode(t, w, &afterDispatch)
	if afterDispatch.Status != "processing" {
		t.Errorf("post-dispatch status = %q, want processing", afterDispatch.Status)
	}

	// A signed settlement webhook completes it.
	payload := jsonBytes(t, map[string]any{
		"event_id":           "evt-settle-1",
		"event":              "payout.settled",
		"provider_reference": ref,
		"transfer_id":        tr.ID,
	})
	w = doReq(h.r, http.MethodPost, "/webhooks/provider/payout", "", signWebhook(payload, time.Now()), payload)
	if w.Code != http.StatusOK {
		t.Fatalf("settle webhook = %d, want 200: %s", w.Code, w.Body.String())
	}

	// Completed, hold captured (reserved back to 0), money debited exactly once.
	w = doReq(h.r, http.MethodGet, "/api/v1/transfers/"+tr.ID, token, nil, nil)
	var settled transferView
	decode(t, w, &settled)
	if settled.Status != "completed" {
		t.Errorf("post-settle status = %q, want completed", settled.Status)
	}
	avail, reserved := h.walletBalance(srcWallet)
	if reserved != 0 {
		t.Errorf("reserved=%d, want 0: the hold must be captured on settlement", reserved)
	}
	if avail != 1_000_000_00-50000 {
		t.Errorf("available=%d, want %d: the payout debits the wallet once", avail, 1_000_000_00-50000)
	}

	// A duplicate settlement delivery is reported as such and moves nothing more.
	w = doReq(h.r, http.MethodPost, "/webhooks/provider/payout", "", signWebhook(payload, time.Now()), payload)
	if w.Code != http.StatusOK {
		t.Fatalf("duplicate settle webhook = %d, want 200: %s", w.Code, w.Body.String())
	}
	var dupResp struct {
		Duplicate bool `json:"duplicate"`
	}
	decode(t, w, &dupResp)
	if !dupResp.Duplicate {
		t.Errorf("second delivery not flagged duplicate: %s", w.Body.String())
	}
	if avail2, _ := h.walletBalance(srcWallet); avail2 != avail {
		t.Errorf("duplicate webhook moved money: available %d -> %d", avail, avail2)
	}
}

// Insufficient funds is refused at the engine with 422, and the refusal touches
// nothing: no debit, no hold, no transfer row. Authorizing the PIN succeeds (that
// step checks a PIN, not a balance); the send is where the money is actually
// reserved, and where the shortfall is caught.
func TestHTTPE2E_InsufficientFundsIsRefused(t *testing.T) {
	h := newHTTPHarness(t, time.Hour)
	sender := uuid.New()
	wallet := h.fundWallet(sender, 5000)
	h.generousLimits(sender)
	payee := h.bankPayee(sender, "0123456791", "058")
	token := mintToken(t, sender)

	w := h.send(token, payee, "100.00", "nsf-key") // 10000 minor, more than the 5000 balance
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST /transfers = %d, want 422: %s", w.Code, w.Body.String())
	}

	avail, reserved := h.walletBalance(wallet)
	if avail != 5000 || reserved != 0 {
		t.Errorf("wallet available=%d reserved=%d, want 5000/0: a refused transfer touches nothing", avail, reserved)
	}
	if n := h.countRows("transfers"); n != 0 {
		t.Errorf("transfers=%d, want 0: an underfunded transfer must not be created", n)
	}
	if n := h.countRows("holds"); n != 0 {
		t.Errorf("holds=%d, want 0", n)
	}
}

// errorView decodes the { "error", "code" } envelope handleError writes, so a
// test can assert the machine-readable code and not merely the HTTP status.
type errorView struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// assertPINAuthInvalid pins the single answer every bad-token path collapses to:
// 409 with code pin_authorization_invalid. The point of one indistinguishable
// code — missing, expired, spent, or bound to a different payment — is that the
// only remedy for any of them is to send the customer back through authorize-pin.
func assertPINAuthInvalid(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusConflict {
		t.Fatalf("POST /transfers = %d, want 409: %s", w.Code, w.Body.String())
	}
	var e errorView
	decode(t, w, &e)
	if e.Code != "pin_authorization_invalid" {
		t.Errorf("error code = %q, want pin_authorization_invalid: %s", e.Code, w.Body.String())
	}
}

// The back-compatible inline-PIN path still authorizes a send with no token and
// no authorize-pin step: a client that has not moved to the dedicated PIN screen
// keeps working, and that path mints no token row.
func TestHTTPE2E_InlinePINStillWorks(t *testing.T) {
	h := newHTTPHarness(t, time.Hour)
	sender, recipient := uuid.New(), uuid.New()
	srcWallet := h.fundWallet(sender, 1_000_000_00)
	h.fundWallet(recipient, 0)
	h.generousLimits(sender)
	h.generousLimits(recipient)
	payee := h.internalPayee(sender, recipient)
	token := mintToken(t, sender)

	w := h.sendInlinePIN(token, payee, "80.00", "inline-pin-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /transfers (inline pin) = %d, want 201: %s", w.Code, w.Body.String())
	}
	var tr transferView
	decode(t, w, &tr)
	if tr.Status != "completed" {
		t.Errorf("status = %q, want completed", tr.Status)
	}
	if avail, _ := h.walletBalance(srcWallet); avail != 1_000_000_00-8000 {
		t.Errorf("available=%d, want %d", avail, 1_000_000_00-8000)
	}
	if n := h.countRows("transfer_pin_authorizations"); n != 0 {
		t.Errorf("pin authorizations=%d, want 0: the inline-PIN path mints no token", n)
	}
}

// A minted token is single-use. The first send consumes it and completes; a
// second send of the SAME token under a DIFFERENT idempotency key is a fresh
// payment (not an idempotency replay), so it reaches the consume, finds the token
// spent, and is refused — the money moves exactly once.
func TestHTTPE2E_PINTokenIsSingleUse(t *testing.T) {
	h := newHTTPHarness(t, time.Hour)
	sender, recipient := uuid.New(), uuid.New()
	srcWallet := h.fundWallet(sender, 1_000_000_00)
	h.fundWallet(recipient, 0)
	h.generousLimits(sender)
	h.generousLimits(recipient)
	payee := h.internalPayee(sender, recipient)
	token := mintToken(t, sender)

	pinToken := h.authorizePIN(token, payee, "100.00")

	if w := h.sendWithToken(token, payee, "100.00", pinToken, "single-1"); w.Code != http.StatusCreated {
		t.Fatalf("first send = %d, want 201: %s", w.Code, w.Body.String())
	}
	assertPINAuthInvalid(t, h.sendWithToken(token, payee, "100.00", pinToken, "single-2"))

	if avail, _ := h.walletBalance(srcWallet); avail != 1_000_000_00-10000 {
		t.Errorf("available=%d, want %d: a single-use token must move money once", avail, 1_000_000_00-10000)
	}
	if n := h.countRows("transfers"); n != 1 {
		t.Errorf("transfers=%d, want 1", n)
	}
}

// A token is bound to the exact amount it was minted for: one issued for 100.00
// cannot pay 200.00. The consume matches no live row for that amount, so the send
// is refused before any money is reserved.
func TestHTTPE2E_PINTokenBoundToAmount(t *testing.T) {
	h := newHTTPHarness(t, time.Hour)
	sender, recipient := uuid.New(), uuid.New()
	srcWallet := h.fundWallet(sender, 1_000_000_00)
	h.fundWallet(recipient, 0)
	h.generousLimits(sender)
	h.generousLimits(recipient)
	payee := h.internalPayee(sender, recipient)
	token := mintToken(t, sender)

	pinToken := h.authorizePIN(token, payee, "100.00")
	assertPINAuthInvalid(t, h.sendWithToken(token, payee, "200.00", pinToken, "amt-1"))

	if avail, reserved := h.walletBalance(srcWallet); avail != 1_000_000_00 || reserved != 0 {
		t.Errorf("wallet available=%d reserved=%d, want %d/0: a rejected token moves nothing", avail, reserved, 1_000_000_00)
	}
	if n := h.countRows("transfers"); n != 0 {
		t.Errorf("transfers=%d, want 0", n)
	}
}

// A token is bound to the exact recipient it was minted for: one issued to pay
// payee A cannot be spent paying payee B, even for the same amount. This is the
// binding that stops a token being transplanted onto a different payment.
func TestHTTPE2E_PINTokenBoundToRecipient(t *testing.T) {
	h := newHTTPHarness(t, time.Hour)
	sender, recipientA, recipientB := uuid.New(), uuid.New(), uuid.New()
	srcWallet := h.fundWallet(sender, 1_000_000_00)
	h.fundWallet(recipientA, 0)
	h.fundWallet(recipientB, 0)
	h.generousLimits(sender)
	h.generousLimits(recipientA)
	h.generousLimits(recipientB)
	payeeA := h.internalPayee(sender, recipientA)
	payeeB := h.internalPayee(sender, recipientB)
	token := mintToken(t, sender)

	pinToken := h.authorizePIN(token, payeeA, "100.00")
	assertPINAuthInvalid(t, h.sendWithToken(token, payeeB, "100.00", pinToken, "rcpt-1"))

	if avail, _ := h.walletBalance(srcWallet); avail != 1_000_000_00 {
		t.Errorf("available=%d, want %d: a token spent on the wrong payee moves nothing", avail, 1_000_000_00)
	}
	if n := h.countRows("transfers"); n != 0 {
		t.Errorf("transfers=%d, want 0", n)
	}
}

// A token past its TTL is dead. Backdating its expiry (rather than waiting out the
// real five minutes) makes the consume find no live row, and the send is refused.
func TestHTTPE2E_PINTokenExpires(t *testing.T) {
	h := newHTTPHarness(t, time.Hour)
	sender, recipient := uuid.New(), uuid.New()
	srcWallet := h.fundWallet(sender, 1_000_000_00)
	h.fundWallet(recipient, 0)
	h.generousLimits(sender)
	h.generousLimits(recipient)
	payee := h.internalPayee(sender, recipient)
	token := mintToken(t, sender)

	pinToken := h.authorizePIN(token, payee, "100.00")
	h.expirePINToken(pinToken)

	assertPINAuthInvalid(t, h.sendWithToken(token, payee, "100.00", pinToken, "expired-1"))

	if avail, _ := h.walletBalance(srcWallet); avail != 1_000_000_00 {
		t.Errorf("available=%d, want %d: an expired token moves nothing", avail, 1_000_000_00)
	}
	if n := h.countRows("transfers"); n != 0 {
		t.Errorf("transfers=%d, want 0", n)
	}
}
