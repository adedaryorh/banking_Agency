package config

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	infisical "github.com/infisical/go-sdk"
)

type env struct {
	mu    sync.RWMutex
	cache map[string]string
}

// get returns a value in this order:
//
// 1. Infisical cache
// 2. Process environment
// 3. Default value
func (e *env) get(key, defaultValue string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if value, ok := e.cache[key]; ok {
		return value
	}

	if value := os.Getenv(key); value != "" {
		return value
	}

	return defaultValue
}

// mustGet returns a required configuration value.
//
// It panics if the value is missing. This is intentional because
// configuration errors should prevent the service from starting.
func (e *env) mustGet(key string) string {
	value := strings.TrimSpace(e.get(key, ""))
	if value == "" {
		panic("required configuration is missing: " + key)
	}

	return value
}

// loadFromInfisical authenticates with Infisical and loads all secrets
// from the configured secret path.
func (e *env) loadFromInfisical() error {
	siteURL := strings.TrimSpace(os.Getenv("INFISICAL_URL"))
	if siteURL == "" {
		return fmt.Errorf("INFISICAL_URL is required")
	}

	clientID := strings.TrimSpace(os.Getenv("INFISICAL_CLIENT_ID"))
	if clientID == "" {
		return fmt.Errorf("INFISICAL_CLIENT_ID is required")
	}

	clientSecret := strings.TrimSpace(os.Getenv("INFISICAL_CLIENT_SECRET"))
	if clientSecret == "" {
		return fmt.Errorf("INFISICAL_CLIENT_SECRET is required")
	}

	projectSlug := strings.TrimSpace(os.Getenv("INFISICAL_PROJECT_SLUG"))
	if projectSlug == "" {
		return fmt.Errorf("INFISICAL_PROJECT_SLUG is required")
	}

	environment := strings.TrimSpace(os.Getenv("INFISICAL_ENVIRONMENT"))
	if environment == "" {
		return fmt.Errorf("INFISICAL_ENVIRONMENT is required")
	}

	secretPath := strings.TrimSpace(os.Getenv("INFISICAL_SECRET_PATHS"))
	if secretPath == "" {
		return fmt.Errorf("INFISICAL_SECRET_PATHS is required")
	}

	client := infisical.NewInfisicalClient(context.Background(), infisical.Config{SiteUrl: siteURL})
	if _, err := client.Auth().UniversalAuthLogin(clientID, clientSecret); err != nil {
		return fmt.Errorf("infisical authentication failed: %w", err)
	}

	paths := strings.Split(secretPath, ",")
	newCache := make(map[string]string)

	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		result, err := client.Secrets().ListSecrets(
			infisical.ListSecretsOptions{
				ProjectSlug: projectSlug,
				Environment: environment,
				SecretPath:  path,
			},
		)
		if err != nil {
			return fmt.Errorf("failed to list Infisical secrets from %s: %w", path, err)
		}

		for _, secret := range result.Secrets {
			newCache[secret.SecretKey] = secret.SecretValue
		}
	}

	// Replace the entire cache atomically.
	e.mu.Lock()
	e.cache = newCache
	e.mu.Unlock()

	fmt.Printf(
		"[INFO] loaded %d Infisical secrets from %s at %s\n",
		len(newCache),
		secretPath,
		time.Now().Format(time.RFC3339),
	)

	return nil
}

// startRefresh periodically reloads secrets from Infisical.
//
// If a refresh fails, the old cache is kept and the application
// continues using the last known-good configuration.
func (e *env) startRefresh(interval time.Duration, onRefresh func()) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			if err := e.loadFromInfisical(); err != nil {
				fmt.Printf("[WARN] Infisical refresh failed; keeping cached secrets: %v\n", err)
				continue
			}
			fmt.Printf("[INFO] Infisical secrets refreshed at %s\n", time.Now().Format(time.RFC3339))
			if onRefresh != nil {
				onRefresh()
			}
		}
	}()
}

// getInt returns an integer configuration value.
func (e *env) getInt(key string, defaultValue int) int {
	value := strings.TrimSpace(e.get(key, ""))

	if value == "" {
		return defaultValue
	}

	result, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue
	}

	return result
}

// getBool returns a boolean configuration value.
func (e *env) getBool(key string, defaultValue bool) bool {
	value := strings.TrimSpace(
		e.get(key, ""),
	)

	if value == "" {
		return defaultValue
	}

	result, err := strconv.ParseBool(value)
	if err != nil {
		return defaultValue
	}

	return result
}

// getDuration returns a duration configuration value.
//
// Examples:
//
//	30s
//	5m
//	1h
func (e *env) getDuration(key string, defaultValue time.Duration) time.Duration {
	value := strings.TrimSpace(e.get(key, ""))
	if value == "" {
		return defaultValue
	}

	result, err := time.ParseDuration(value)
	if err != nil {
		return defaultValue
	}

	return result
}

func (e *env) getCSV(key string) []string {
	value := strings.TrimSpace(e.get(key, ""))

	if value == "" {
		return nil
	}

	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)

		if part != "" {
			result = append(result, part)
		}
	}

	return result
}
