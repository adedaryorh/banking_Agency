# Transfer Service

Complete transfer implementation for the Nablr platform, supporting internal transfers, bank payouts, scheduled payments, and smart pay features.

## 🎯 What This Service Does

- **Transfers**: Send money to bank accounts or internal wallets
- **Beneficiaries**: Manage saved payees with encryption and verification
- **Smart Pay**: Bank suggestions, payment patterns, timing statistics
- **Scheduled Payments**: One-off and recurring payment schedules
- **Security**: Account number encryption, cooling period, name verification
- **Limits**: Per-transaction and daily outbound limits enforcement

## 📚 Documentation

- **[COMPLETE.md](./COMPLETE.md)** - ✅ Complete implementation summary
- **[TRANSFER_IMPLEMENTATION.md](./TRANSFER_IMPLEMENTATION.md)** - Comprehensive guide
- **[QUICKSTART.md](./QUICKSTART.md)** - Getting started guide
- **[FILES_CREATED.md](./FILES_CREATED.md)** - File inventory

## 🚀 Quick Start

```bash
# 1. Setup environment
cp .env.example .env
# Edit .env with your configuration

# 2. Setup database
createdb transfers_db
psql transfers_db < db/migrations/001_transfers_schema.up.sql

# 3. Install dependencies
go mod tidy

# 4. Run service
go run cmd/main.go
```

See [QUICKSTART.md](./QUICKSTART.md) for detailed instructions.

## 🏗️ Architecture

```
services/transfers-svc/
├── cmd/                    # Application entrypoints
├── db/                     # Database migrations
├── internal/
│   ├── handlers/          # HTTP handlers
│   ├── models/            # Domain models
│   ├── platform/          # Utilities (crypto, clock, id)
│   ├── providers/         # Payment provider integration
│   ├── routes/            # HTTP routes
│   └── service/           # Business logic
└── docs/                  # Documentation
```

## ✨ Key Features

### Security 🔐
- AES-256-GCM encryption for account numbers
- Blind index for duplicate detection
- 24-hour cooling period for new payees
- HMAC webhook signature verification
- Idempotency keys for exactly-once semantics

### Smart Pay 🧠
- NUBAN bank suggestion (check digit validation)
- Payment history analysis
- Suggested amounts and cadence detection
- High-value warning system
- Bank settlement timing statistics

### Scheduled Payments 📅
- One-off and recurring schedules
- Multiple frequencies (daily, weekly, monthly, etc.)
- Automatic retry with failure handling
- Complete execution history

### State Machine 🔄
9 states with validated transitions:
- created → pending → processing → completed
- Supports: under_review, failed, cancelled, reversed, refunded

## 📡 API Endpoints

### Beneficiaries
- `POST /api/v1/beneficiaries` - Create
- `GET /api/v1/beneficiaries` - List
- `PATCH /api/v1/beneficiaries/{id}` - Update
- `DELETE /api/v1/beneficiaries/{id}` - Delete
- `POST /api/v1/beneficiaries/resolve` - Name enquiry

### Transfers
- `POST /api/v1/transfers/quotes` - Get quote
- `POST /api/v1/transfers` - Create transfer
- `GET /api/v1/transfers/{id}` - Get transfer
- `GET /api/v1/transfers` - List transfers
- `POST /api/v1/transfers/{id}/cancel` - Cancel

### Smart Features
- `GET /api/v1/banks?account_number=` - Suggest banks
- `GET /api/v1/transfers/suggestions?beneficiary_id=` - Payment suggestions
- `GET /api/v1/banks/timing?bank_code=` - Bank timing

## 🗄️ Database

### Tables
- `beneficiaries` - Saved payees with encryption
- `transfers` - Transfer records with state tracking
- `transfer_events` - Complete audit trail
- `scheduled_payments` - Payment schedules
- `scheduled_payment_runs` - Execution history
- `customer_limits` - Transaction and daily limits
- `banks` - Bank catalog
- `idempotency_keys` - Request deduplication

## 🔌 Provider Integration

Supports any provider implementing the `PayoutProvider` interface:

```go
type PayoutProvider interface {
    Info() ProviderInfo
    HealthCheck(ctx) error
    SendPayout(ctx, PayoutRequest) (*PayoutResult, error)
    PayoutStatus(ctx, reference string) (*PayoutStatusResult, error)
    ValidateAccount(ctx, countryCode, bankCode, accountNumber string) (*AccountValidation, error)
}
```

Includes mock provider for testing.

## 🧪 Testing

```go
// Setup mock provider
mock := providers.NewMockProvider()

// Test payout flow
result, _ := mock.SendPayout(ctx, request)
mock.SimulateSettlement(result.ProviderRef)

// Verify outcome
status, _ := mock.PayoutStatus(ctx, result.ProviderRef)
```

## 📊 Monitoring

Key metrics to track:
- Transfer success rate by status
- Time to settlement (P50, P90, P99)
- Cooling period rejections
- High-value warnings triggered
- Scheduled payment success rate
- Provider error rates

## 🔗 Integration

### Required Services
- **Wallet Service** - Balance validation
- **Ledger Service** - Holds and postings
- **Customer Service** - Internal transfer recipient lookup
- **Notification Service** - Transfer event notifications

### Optional Services
- FX Service - Cross-currency rates
- Fraud Detection - Transaction scoring
- Analytics - Event streaming

## ⚙️ Configuration

Key environment variables:

```bash
# Encryption
ENCRYPTION_KEY_V1=<32-byte-hex>
ENCRYPTION_KEY_V2=<32-byte-hex>
CURRENT_KEY_VERSION=2
BLIND_INDEX_PEPPER=<32-byte-hex>

# Security
WEBHOOK_SECRET=<secret>
COOLING_PERIOD_HOURS=24

# Provider
PROVIDER_NAME=novac
PROVIDER_API_KEY=<key>
PROVIDER_API_SECRET=<secret>
```

See `.env.example` for full configuration.

## 📈 Status

✅ **COMPLETE** - 100% feature parity with usenablr-1.0

- ✅ Domain models
- ✅ Service layer
- ✅ HTTP handlers
- ✅ Provider integration
- ✅ Database schema
- ✅ Security features
- ✅ Smart pay features
- ✅ Scheduled payments
- ✅ Documentation

Ready for integration and deployment!

## 🤝 Contributing

1. Follow existing code patterns
2. Add tests for new features
3. Update documentation
4. Maintain security standards
5. Follow state machine rules

## 📝 License

Copyright © 2024 Nablr Platform

## 📞 Support

For questions or issues:
1. Check documentation in this directory
2. Review code comments
3. Test with mock provider
4. Verify database schema

---

**Built with ❤️ for the Nablr Platform**
