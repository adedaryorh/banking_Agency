package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Port           string
	Environment    string
	JWTSecret      string
	JWTIssuer      string
	RedisURL       string
	RateLimitRate  int64
	Services       ServiceConfig
	CircuitBreaker CircuitBreakerConfig
	Cache          CacheConfig

	// AdminAPIKey gates the operator-only /admin/* routes, which proxy raw
	// SQL and wallet-funding requests through to the owning backend service.
	// Optional and NOT validated as required: an empty key means those
	// routes refuse every request rather than the gateway failing to boot.
	// Must match the ADMIN_API_KEY configured on each backend service.
	AdminAPIKey string
}

type ServiceConfig struct {
	Identity     ServiceEndpoint
	Transfers    ServiceEndpoint
	VAS          ServiceEndpoint
	Notification ServiceEndpoint
}

type ServiceEndpoint struct {
	HTTPBaseURL string
	GRPCAddress string
	Timeout     time.Duration
}

type CircuitBreakerConfig struct {
	MaxRequests      uint32
	Interval         time.Duration
	Timeout          time.Duration
	FailureThreshold uint32
}

type CacheConfig struct {
	Enabled bool
	TTL     time.Duration
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

	// Keep the live configuration updated from Infisical.
	if !isLocal {
		e.startRefresh(
			5*time.Minute, func() {
				cfg.loadFrom(e)
				if err := cfg.Validate(); err != nil {
					fmt.Printf(
						"[WARN] configuration validation failed after Infisical refresh: %v\n",
						err,
					)
				}
			},
		)
	}

	return cfg, nil
}

// loadFrom populates the typed Config from the current environment/cache.
func (c *Config) loadFrom(e *env) {
	c.Port = e.get("PORT", "8000")
	c.Environment = e.get("ENVIRONMENT", "development")

	c.JWTSecret = e.mustGet("JWT_SECRET")
	c.JWTIssuer = e.get("JWT_ISSUER", "nablr")

	c.RedisURL = e.get("REDIS_URL", "localhost:6379")
	c.RateLimitRate = int64(e.getInt("RATE_LIMIT_RATE", 100))

	c.Services = ServiceConfig{
		Identity: ServiceEndpoint{
			HTTPBaseURL: e.get("IDENTITY_HTTP_URL", "http://localhost:8001"),
			GRPCAddress: e.get("IDENTITY_GRPC_URL", "localhost:9001"),
			Timeout:     e.getDuration("IDENTITY_TIMEOUT", 30*time.Second),
		},

		Transfers: ServiceEndpoint{
			HTTPBaseURL: e.get("TRANSFERS_HTTP_URL", "http://localhost:8002"),
			GRPCAddress: e.get("TRANSFERS_GRPC_URL", "localhost:9002"),
			Timeout:     e.getDuration("TRANSFERS_TIMEOUT", 30*time.Second),
		},

		VAS: ServiceEndpoint{
			HTTPBaseURL: e.get("VAS_HTTP_URL", "http://localhost:8003"),
			GRPCAddress: e.get("VAS_GRPC_URL", "localhost:9003"),
			Timeout:     e.getDuration("VAS_TIMEOUT", 30*time.Second),
		},

		Notification: ServiceEndpoint{
			HTTPBaseURL: e.get("NOTIFICATION_HTTP_URL", "http://localhost:8004"),
			GRPCAddress: e.get("NOTIFICATION_GRPC_URL", "localhost:9004"),
			Timeout:     e.getDuration("NOTIFICATION_TIMEOUT", 30*time.Second),
		},
	}

	c.CircuitBreaker = CircuitBreakerConfig{
		MaxRequests:      uint32(e.getInt("CIRCUIT_BREAKER_MAX_REQUESTS", 3)),
		Interval:         e.getDuration("CIRCUIT_BREAKER_INTERVAL", 10*time.Second),
		Timeout:          e.getDuration("CIRCUIT_BREAKER_TIMEOUT", 60*time.Second),
		FailureThreshold: uint32(e.getInt("CIRCUIT_BREAKER_FAILURE_THRESHOLD", 5)),
	}

	c.Cache = CacheConfig{
		Enabled: e.getBool("CACHE_ENABLED", true),
		TTL:     e.getDuration("CACHE_TTL", 5*time.Minute),
	}

	c.AdminAPIKey = e.get("ADMIN_API_KEY", "")
}

// Validate fails fast when required configuration is missing.
func (c *Config) Validate() error {

	if strings.TrimSpace(c.JWTSecret) == "" {
		return fmt.Errorf("JWT_SECRET is required and must match identity-svc")
	}

	if strings.TrimSpace(c.JWTIssuer) == "" {
		return fmt.Errorf("JWT_ISSUER is required and must match identity-svc")
	}

	if strings.TrimSpace(c.RedisURL) == "" {
		return fmt.Errorf("REDIS_URL is required")
	}

	if strings.TrimSpace(c.Port) == "" {
		return fmt.Errorf("PORT is required")
	}

	return nil
}
