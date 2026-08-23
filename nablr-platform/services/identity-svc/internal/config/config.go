package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Environment          string
	DatabaseURL          string
	HTTPAddress          string
	GRPCAddress          string
	JWTSecret            string
	JWTIssuer            string
	APISecret            string
	InternalServiceToken string
	TransfersGRPCURL     string
	RabbitMQURL          string

	FrontendURL        string
	CORSAllowedOrigins []string

	SMSProvider   string
	SMSSenderID   string
	TermiiBaseURL string
	TermiiAPIKey  string
	TwilioBaseURL string
	TwilioSID     string
	TwilioToken   string
	TwilioFrom    string

	UseFaceDedup           bool
	FaceDedupBaseURL       string
	FaceDedupLicenseKey    string
	FaceDedupMockNINVerify bool

	BVNProviders []string
	BVNFailover  bool

	SwiftendBaseURL   string
	SwiftendServiceID string

	DojahBaseURL   string
	DojahAppID     string
	DojahSecretKey string

	NINAuthBaseURL string
	NINAuthAPIKey  string

	ObjectStorageProvider        string
	DocumentStoragePath          string
	ObjectStorageBucket          string
	ObjectStorageRegion          string
	ObjectStorageEndpoint        string
	ObjectStorageAccessKeyID     string
	ObjectStorageSecretAccessKey string
	ObjectStorageForcePathStyle  bool

	NotificationHTTPURL string

	EncryptionKey string

	MobileLatestVersion       string
	MobileMinSupportedVersion string
	MobileUpdateMessage       string

	MobileIOSLatestVersion       string
	MobileIOSMinSupportedVersion string
	MobileIOSStoreURL            string

	MobileAndroidLatestVersion       string
	MobileAndroidMinSupportedVersion string
	MobileAndroidStoreURL            string

	// AdminAPIKey gates the operator-only POST /admin/db/query route (raw SQL
	// against this service's own database). Optional and NOT validated as
	// required: an empty key means the route refuses every request rather
	// than the service failing to boot. Must match the same setting on
	// api-gateway, which proxies to this endpoint.
	AdminAPIKey string
}

// Load loads configuration from .env locally and Infisical in non-local environments.
func Load() (*Config, error) {
	_ = godotenv.Load()

	e := &env{cache: make(map[string]string)}

	environment := e.get("ENVIRONMENT", "development")
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

	if !isLocal {
		e.startRefresh(5*time.Minute, func() {
			cfg.loadFrom(e)

			if err := cfg.Validate(); err != nil {
				fmt.Printf("[WARN] configuration validation failed after Infisical refresh: %v\n", err)
			}
		})
	}

	return cfg, nil
}

func (c *Config) loadFrom(e *env) {
	c.Environment = e.get("ENVIRONMENT", "development")
	c.DatabaseURL = e.get("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/nablr_identity?sslmode=disable")
	c.HTTPAddress = e.get("IDENTITY_HTTP_ADDRESS", ":8001")
	c.GRPCAddress = e.get("IDENTITY_GRPC_ADDRESS", ":9001")
	c.JWTSecret = e.get("JWT_SECRET", "")
	c.JWTIssuer = e.get("JWT_ISSUER", "nablr")
	c.APISecret = e.get("API_SECRET", "")
	c.InternalServiceToken = e.get("INTERNAL_SERVICE_TOKEN", "")
	c.TransfersGRPCURL = e.get("TRANSFERS_GRPC_URL", "localhost:9002")
	c.RabbitMQURL = e.get("RABBITMQ_URL", "")

	c.FrontendURL = e.get("FRONTEND_URL", "http://localhost:3000")
	c.CORSAllowedOrigins = e.getCSV("CORS_ALLOWED_ORIGINS")

	c.SMSProvider = e.get("SMS_PROVIDER", "log")
	c.SMSSenderID = e.get("SMS_SENDER_ID", "Nabla")
	c.TermiiBaseURL = e.get("TERMII_BASE_URL", "https://v4.api.termii.com")
	c.TermiiAPIKey = e.get("TERMII_API_KEY", "")
	c.TwilioBaseURL = e.get("TWILIO_BASE_URL", "https://api.twilio.com")
	c.TwilioSID = e.get("TWILIO_SID", "")
	c.TwilioToken = e.get("TWILIO_TOKEN", "")
	c.TwilioFrom = e.get("TWILIO_FROM", "")

	c.UseFaceDedup = e.getBool("USE_FACEDEDUP", false)
	c.FaceDedupBaseURL = e.get("FACEDEDUP_BASE_URL", "https://facededup.ai")
	c.FaceDedupLicenseKey = e.get("FACEDEDUP_LICENSE_KEY", e.get("FACEDEDUP_API_KEY", ""))
	c.FaceDedupMockNINVerify = e.getBool("FACEDEDUP_MOCK_NIN_VERIFY", false)

	c.BVNProviders = e.getCSV("BVN_PROVIDERS")
	c.BVNFailover = e.getBool("BVN_FAILOVER", true)

	c.SwiftendBaseURL = e.get("SWIFTEND_BASE_URL", "")
	c.SwiftendServiceID = e.get("SWIFTEND_SERVICE_ID", "")

	c.DojahBaseURL = e.get("DOJAH_BASE_URL", "")
	c.DojahAppID = e.get("DOJAH_APP_ID", "")
	c.DojahSecretKey = e.get("DOJAH_SECRET_KEY", "")

	c.NINAuthBaseURL = e.get("NINAUTH_BASE_URL", "")
	c.NINAuthAPIKey = e.get("NINAUTH_API_KEY", "")

	c.ObjectStorageProvider = e.get("OBJECT_STORAGE_PROVIDER", "local")
	c.DocumentStoragePath = e.get("DOCUMENT_STORAGE_PATH", "./data/documents")
	c.ObjectStorageBucket = e.get("OBJECT_STORAGE_BUCKET", "")
	c.ObjectStorageRegion = e.get("OBJECT_STORAGE_REGION", "us-east-1")
	c.ObjectStorageEndpoint = e.get("OBJECT_STORAGE_ENDPOINT", "")
	c.ObjectStorageAccessKeyID = e.get("OBJECT_STORAGE_ACCESS_KEY_ID", "")
	c.ObjectStorageSecretAccessKey = e.get("OBJECT_STORAGE_SECRET_ACCESS_KEY", "")
	c.ObjectStorageForcePathStyle = e.getBool("OBJECT_STORAGE_FORCE_PATH_STYLE", false)

	c.NotificationHTTPURL = e.get("NOTIFICATION_HTTP_URL", "http://localhost:8004")

	c.EncryptionKey = e.get("ENCRYPTION_KEY", "")

	c.MobileLatestVersion = e.get("MOBILE_LATEST_VERSION", "1.0.0")
	c.MobileMinSupportedVersion = e.get("MOBILE_MIN_SUPPORTED_VERSION", "1.0.0")
	c.MobileUpdateMessage = e.get("MOBILE_UPDATE_MESSAGE", "A newer version is available.")

	c.MobileIOSLatestVersion = e.get("MOBILE_IOS_LATEST_VERSION", "1.0.0")
	c.MobileIOSMinSupportedVersion = e.get("MOBILE_IOS_MIN_SUPPORTED_VERSION", "1.0.0")
	c.MobileIOSStoreURL = e.get("MOBILE_IOS_STORE_URL", "")

	c.MobileAndroidLatestVersion = e.get("MOBILE_ANDROID_LATEST_VERSION", "1.0.0")
	c.MobileAndroidMinSupportedVersion = e.get("MOBILE_ANDROID_MIN_SUPPORTED_VERSION", "1.0.0")
	c.MobileAndroidStoreURL = e.get("MOBILE_ANDROID_STORE_URL", "")

	c.AdminAPIKey = e.get("ADMIN_API_KEY", "")
}

func (c *Config) Validate() error {
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}

	if strings.TrimSpace(c.HTTPAddress) == "" {
		return fmt.Errorf("IDENTITY_HTTP_ADDRESS is required")
	}

	if strings.TrimSpace(c.GRPCAddress) == "" {
		return fmt.Errorf("IDENTITY_GRPC_ADDRESS is required")
	}

	if strings.TrimSpace(c.JWTSecret) == "" {
		if c.IsProduction() {
			return fmt.Errorf("JWT_SECRET is required in production")
		}

		c.JWTSecret = "development-only-change-me"
	}

	if strings.TrimSpace(c.JWTIssuer) == "" {
		return fmt.Errorf("JWT_ISSUER is required")
	}

	if c.IsProduction() && strings.TrimSpace(c.APISecret) == "" {
		return fmt.Errorf("API_SECRET is required in production")
	}

	if c.IsProduction() && c.APISecret == "secret" {
		return fmt.Errorf("API_SECRET must be set to a non-default value in production")
	}

	if c.IsProduction() && strings.TrimSpace(c.InternalServiceToken) == "" {
		return fmt.Errorf("INTERNAL_SERVICE_TOKEN is required in production")
	}

	if c.IsProduction() && strings.TrimSpace(c.EncryptionKey) == "" {
		return fmt.Errorf("ENCRYPTION_KEY is required in production")
	}

	return nil
}

func (c *Config) IsProduction() bool {
	return strings.EqualFold(strings.TrimSpace(c.Environment), "production")
}
