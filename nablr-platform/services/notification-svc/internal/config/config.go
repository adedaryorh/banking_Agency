package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Environment string
	HTTPAddress string
	GRPCAddress string

	DatabaseURL          string
	InternalServiceToken string
	RabbitMQURL          string

	EmailProviders   string
	EmailFromAddress string
	EmailFromName    string

	MailgunBaseURL string
	MailgunDomain  string
	MailgunAPIKey  string

	SendGridBaseURL string
	SendGridAPIKey  string

	SMTPHost     string
	SMTPPort     string
	SMTPUsername string
	SMTPPassword string

	SMSProvider        string
	SMSBaseURL         string
	SMSSenderID        string
	TermiiAPIKey       string
	DigitalPulseAPIKey string
	DigitalPulseRoute  string
	TwilioSID          string
	TwilioToken        string
	TwilioFrom         string

	PushProvider            string
	FirebaseConfigJSON      string
	FirebaseCredentials     string
	FirebaseCredentialsPath string
}

// Load loads configuration from .env locally and Infisical in non-local environments.
func Load() (*Config, error) {
	_ = godotenv.Load()

	e := &env{cache: make(map[string]string)}

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

// loadFrom populates the typed Config from the current environment/cache.
func (c *Config) loadFrom(e *env) {
	c.Environment = e.get("APP_ENV", e.get("ENVIRONMENT", "development"))
	c.HTTPAddress = e.get("NOTIFICATION_HTTP_ADDRESS", ":8004")
	c.GRPCAddress = e.get("NOTIFICATION_GRPC_ADDRESS", ":9004")

	c.DatabaseURL = e.get("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/nabla_notification?sslmode=disable")
	c.InternalServiceToken = e.mustGet("INTERNAL_SERVICE_TOKEN")
	c.RabbitMQURL = e.get("RABBITMQ_URL", "")

	smtpPassword := e.get("SMTP_PASSWORD", e.get("RESEND_SMTP_PASSWORD", ""))

	c.EmailProviders = e.get("EMAIL_PROVIDERS", defaultEmailProviders(c.Environment, smtpPassword))
	c.EmailFromAddress = e.get("EMAIL_FROM_ADDRESS", "")
	c.EmailFromName = e.get("EMAIL_FROM_NAME", "Nabla")

	c.MailgunBaseURL = e.get("MAILGUN_BASE_URL", "https://api.mailgun.net")
	c.MailgunDomain = e.get("MAILGUN_DOMAIN", "")
	c.MailgunAPIKey = e.get("MAILGUN_API_KEY", "")

	c.SendGridBaseURL = e.get("SENDGRID_BASE_URL", "https://api.sendgrid.com")
	c.SendGridAPIKey = e.get("SENDGRID_API_KEY", "")

	c.SMTPHost = e.get("SMTP_HOST", e.get("RESEND_SMTP_HOST", ""))
	c.SMTPPort = e.get("SMTP_PORT", e.get("RESEND_SMTP_PORT", "587"))
	c.SMTPUsername = e.get("SMTP_USERNAME", e.get("RESEND_SMTP_USERNAME", ""))
	c.SMTPPassword = smtpPassword

	c.SMSProvider = e.get("SMS_PROVIDER", "log")
	c.SMSBaseURL = e.get("TERMII_BASE_URL", "https://v4.api.termii.com")

	if strings.EqualFold(c.SMSProvider, "twilio") {
		c.SMSBaseURL = e.get("TWILIO_BASE_URL", "https://api.twilio.com")
	} else if strings.EqualFold(c.SMSProvider, "digitalpulse") {
		c.SMSBaseURL = e.get("DIGITALPULSE_BASE_URL", "https://mps-00.digitalpulseapi.net")
	}

	c.SMSSenderID = e.get("SMS_SENDER_ID", "")
	c.TermiiAPIKey = e.get("TERMII_API_KEY", "")

	c.DigitalPulseAPIKey = e.get("DIGITALPULSE_API_KEY", "")
	c.DigitalPulseRoute = e.get("DIGITALPULSE_ROUTE", "anq")

	c.TwilioSID = e.get("TWILIO_ACCOUNT_SID", "")
	c.TwilioToken = e.get("TWILIO_AUTH_TOKEN", "")
	c.TwilioFrom = e.get("TWILIO_PHONE_NUMBER", "")

	c.PushProvider = e.get("PUSH_PROVIDER", "log")
	c.FirebaseConfigJSON = e.get("FIREBASE_CONFIG_JSON", e.get("FIREBASE_CREDENTIALS_JSON", ""))
	c.FirebaseCredentials = e.get("FIREBASE_CREDENTIALS_BASE64", "")
	c.FirebaseCredentialsPath = e.get("FIREBASE_CREDENTIALS_PATH", "")
}

// Validate fails fast when required configuration is missing.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Environment) == "" {
		return fmt.Errorf("ENVIRONMENT is required")
	}

	if strings.TrimSpace(c.HTTPAddress) == "" {
		return fmt.Errorf("NOTIFICATION_HTTP_ADDRESS is required")
	}

	if strings.TrimSpace(c.GRPCAddress) == "" {
		return fmt.Errorf("NOTIFICATION_GRPC_ADDRESS is required")
	}

	if strings.TrimSpace(c.DatabaseURL) == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}

	if strings.TrimSpace(c.InternalServiceToken) == "" {
		return fmt.Errorf("INTERNAL_SERVICE_TOKEN is required")
	}

	return nil
}

func defaultEmailProviders(environment, smtpPassword string) string {
	switch strings.ToLower(strings.TrimSpace(environment)) {
	case "production", "prod", "sandbox", "staging", "stage":
		return "mailgun"
	case "local", "development", "dev", "test":
		if strings.TrimSpace(smtpPassword) != "" {
			return "smtp"
		}
		return "log"
	default:
		return "log"
	}
}
