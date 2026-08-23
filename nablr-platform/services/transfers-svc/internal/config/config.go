package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	DatabaseURL         string
	HTTPAddress         string
	GRPCAddress         string
	JWTSecret           string
	JWTIssuer           string
	APISecret           string
	PayoutWebhookSecret string
	WebhookSecret       string
	EncryptionKey       string
	BlindIndexKey       string

	MockCollections bool
	RunWorker       bool

	NovacAPIURL            string
	NovacAPIKey            string
	NovacAPISecret         string
	NovacVABankCode        string
	NovacWebhookAllowedIPs string
	NovacWebhookTrustProxy bool
	// NovacSandbox marks a non-production key, inferred from the URL when unset.
	// It is customer-visible: a funding account issued by a test issuer is a
	// real-looking number that swallows real money.
	NovacSandbox bool

	IdentityGRPCURL      string
	NotificationGRPCURL  string
	InternalServiceToken string
	RabbitMQURL          string

	// Cards. With no SUDO_API_KEY the service runs on the mock issuer, which
	// asks for no cardholder KYC and therefore causes no identity number to
	// be gathered. See internal/providers/sudo.
	SudoAPIURL          string
	SudoAPIKey          string
	SudoFundingSourceID string
	SudoDebitAccountID  string
	SudoBrand           string
	SudoSandbox         bool
	// SudoWebhookSecret proves a callback came from Sudo. Empty refuses every
	// callback: an endpoint that approves card spending must not be open
	// because a setting was missed.
	SudoWebhookSecret string
	// SudoAllowedCustomers is who may hold a REAL card, as a comma-separated
	// list of customer ids or emails, or "*"/"all" for everyone. Empty means
	// nobody — a rollout that fails open is not gated at all.
	SudoAllowedCustomers string

	// AdminAPIKey gates the operator-only /admin/* routes (currently:
	// crediting a customer's wallet outside any payment rail). Optional and
	// NOT validated as required: an empty key means those routes refuse
	// every request rather than the service failing to boot. Must match the
	// same setting on api-gateway, which proxies to this key.
	AdminAPIKey string
}

// Load loads configuration from .env locally and Infisical in non-local
// environments.
func Load() (*Config, error) {
	// Load .env for local development.
	_ = godotenv.Load()

	e := &env{
		cache: make(map[string]string),
	}

	environment := e.get("APP_ENV", e.get("ENVIRONMENT", "development"))
	isLocal := strings.EqualFold(environment, "local")
	if !isLocal {
		if err := e.loadFromInfisical(); err != nil {
			return nil, fmt.Errorf("initial Infisical load failed: %w", err)
		}
	}

	cfg := &Config{}
	cfg.loadFrom(e)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Refresh Infisical configuration periodically in non-local environments.
	if !isLocal {
		e.startRefresh(5*time.Minute, func() {
			cfg.loadFrom(e)

			if err := cfg.Validate(); err != nil {
				fmt.Printf(
					"[WARN] configuration validation failed after Infisical refresh: %v\n",
					err,
				)
			}
		})
	}

	return cfg, nil
}

// loadFrom populates the typed Config from the current environment/cache.
func (c *Config) loadFrom(e *env) {
	c.DatabaseURL = e.get("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/nabla_transfers?sslmode=disable")
	c.HTTPAddress = e.get("TRANSFERS_HTTP_ADDRESS", ":8002")

	c.GRPCAddress = e.get("TRANSFERS_GRPC_ADDRESS", ":9002")

	c.JWTSecret = e.get("JWT_SECRET", "")
	c.JWTIssuer = e.get("JWT_ISSUER", "nablr")

	c.APISecret = e.get("API_SECRET", "")
	c.PayoutWebhookSecret = e.get("PAYOUT_WEBHOOK_SECRET", "")
	c.WebhookSecret = e.get("WEBHOOK_SECRET", "")
	c.EncryptionKey = e.get("ENCRYPTION_KEY", "")
	c.BlindIndexKey = e.get("BLIND_INDEX_KEY", "")

	c.NovacAPIURL = e.get("NOVAC_API_URL", "https://api.novacpayment.com")
	c.NovacAPIKey = e.get("NOVAC_API_KEY", "")
	c.NovacAPISecret = e.get("NOVAC_API_SECRET", "")
	c.NovacVABankCode = e.get("NOVAC_VA_BANK_CODE", "")
	c.NovacSandbox = e.getBool("NOVAC_SANDBOX", false)
	c.NovacWebhookAllowedIPs = e.get("NOVAC_WEBHOOK_ALLOWED_IPS", "18.233.137.110")
	c.NovacWebhookTrustProxy = e.getBool("NOVAC_WEBHOOK_TRUST_PROXY_HEADERS", false)

	c.RunWorker = e.getBool("RUN_WORKER", true)
	c.MockCollections = e.getBool("MOCK_COLLECTIONS", false)

	c.IdentityGRPCURL = e.get("IDENTITY_GRPC_URL", "localhost:9001")
	c.NotificationGRPCURL = e.get("NOTIFICATION_GRPC_URL", "localhost:9004")
	c.InternalServiceToken = e.get("INTERNAL_SERVICE_TOKEN", "")
	c.RabbitMQURL = e.get("RABBITMQ_URL", "")

	c.SudoAPIURL = e.get("SUDO_API_URL", "")
	c.SudoAPIKey = e.get("SUDO_API_KEY", "")
	c.SudoFundingSourceID = e.get("SUDO_FUNDING_SOURCE_ID", "")
	c.SudoDebitAccountID = e.get("SUDO_DEBIT_ACCOUNT_ID", "")
	c.SudoBrand = e.get("SUDO_BRAND", "Verve")
	c.SudoSandbox = e.getBool("SUDO_SANDBOX", true)
	c.SudoWebhookSecret = e.get("SUDO_WEBHOOK_SECRET", "")
	c.SudoAllowedCustomers = e.get("SUDO_ALLOWED_CUSTOMERS", "")

	c.AdminAPIKey = e.get("ADMIN_API_KEY", "")
}

// Validate fails fast when required configuration is missing.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.JWTSecret) == "" {
		return fmt.Errorf("JWT_SECRET is required")
	}

	if strings.TrimSpace(c.DatabaseURL) == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}

	if strings.TrimSpace(c.HTTPAddress) == "" {
		return fmt.Errorf("TRANSFERS_HTTP_ADDRESS is required")
	}

	if strings.TrimSpace(c.GRPCAddress) == "" {
		return fmt.Errorf("TRANSFERS_GRPC_ADDRESS is required")
	}

	if strings.TrimSpace(c.APISecret) == "" {
		return fmt.Errorf("API_SECRET is required")
	}

	if strings.TrimSpace(c.InternalServiceToken) == "" {
		return fmt.Errorf("INTERNAL_SERVICE_TOKEN is required")
	}

	return nil
}
