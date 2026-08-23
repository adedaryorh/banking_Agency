package bootstrap

import (
	"context"
	"fmt"
	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/config"
	"nabla/identity-svc/internal/integrations/notification-svc"
	"nabla/identity-svc/internal/providers"
	"nabla/identity-svc/internal/providers/kyc/dojah"
	"nabla/identity-svc/internal/providers/kyc/facededup"
	"nabla/identity-svc/internal/providers/kyc/ninauth"
	"nabla/identity-svc/internal/providers/kyc/swiftend"
	"strings"
	"time"
)

type Providers struct {
	SMS       providers.SMSProvider
	Email     providers.EmailProvider
	Identity  providers.IdentityProvider
	Directory providers.DirectoryLookupProvider
	Cipher    *helpers.FieldCipher
	Storage   providers.ObjectStorage
}

func BuildProviders(ctx context.Context, cfg *config.Config) (Providers, error) {
	var result Providers
	var err error

	if cfg.InternalServiceToken != "" {
		client, clientErr := notification.New(
			cfg.NotificationHTTPURL,
			cfg.InternalServiceToken,
			30*time.Second,
		)
		if clientErr != nil {
			return result, fmt.Errorf("configure notification client: %w", clientErr)
		}

		result.SMS = client
		result.Email = client
	} else if cfg.IsProduction() {
		return result, fmt.Errorf("INTERNAL_SERVICE_TOKEN is required in production")
	} else {
		baseURL := cfg.TermiiBaseURL

		if strings.EqualFold(cfg.SMSProvider, "twilio") {
			baseURL = cfg.TwilioBaseURL
		}

		result.SMS, err = providers.NewSMSProvider(providers.SMSConfig{
			Provider:     cfg.SMSProvider,
			BaseURL:      baseURL,
			SenderID:     cfg.SMSSenderID,
			TermiiAPIKey: cfg.TermiiAPIKey,
			TwilioSID:    cfg.TwilioSID,
			TwilioToken:  cfg.TwilioToken,
			TwilioFrom:   cfg.TwilioFrom,
		})
		if err != nil {
			return result, fmt.Errorf("configure SMS: %w", err)
		}

		result.Email = providers.NewLoggingEmailProvider()
	}

	if cfg.EncryptionKey != "" {
		result.Cipher, err = helpers.NewFieldCipher(cfg.EncryptionKey)
		if err != nil {
			return result, fmt.Errorf("configure encryption: %w", err)
		}
	} else if cfg.IsProduction() {
		return result, fmt.Errorf("ENCRYPTION_KEY is required in production")
	}

	result.Identity, result.Directory, err = identityProviders(cfg)
	if err != nil {
		return result, fmt.Errorf("configure identity providers: %w", err)
	}

	result.Storage, err = objectStorage(ctx, cfg)
	if err != nil {
		return result, fmt.Errorf("configure object storage: %w", err)
	}

	return result, nil
}

func identityProviders(cfg *config.Config) (providers.IdentityProvider, providers.DirectoryLookupProvider, error) {
	timeout := 30 * time.Second

	if cfg.UseFaceDedup && cfg.FaceDedupLicenseKey != "" {
		facedupProvider, err := facededup.New(facededup.Config{
			BaseURL:       cfg.FaceDedupBaseURL,
			LicenseKey:    cfg.FaceDedupLicenseKey,
			MockNINVerify: cfg.FaceDedupMockNINVerify && !cfg.IsProduction(),
			Timeout:       timeout,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("facededup: %w", err)
		}

		var directory providers.DirectoryLookupProvider

		if cfg.SwiftendServiceID != "" {
			swiftendProvider, err := swiftend.New(swiftend.Config{
				BaseURL:   cfg.SwiftendBaseURL,
				ServiceID: cfg.SwiftendServiceID,
				Timeout:   timeout,
			})
			if err == nil {
				directory = swiftendProvider
			}
		}

		return splitIdentity{
			bvn: facedupProvider,
			nin: facedupProvider,
		}, directory, nil
	}

	ordered := make([]providers.IdentityProvider, 0, 2)
	var directory providers.DirectoryLookupProvider
	seen := map[string]bool{}

	for _, name := range cfg.BVNProviders {
		name = strings.ToLower(strings.TrimSpace(name))

		if name == "" {
			continue
		}

		if seen[name] {
			return nil, nil, fmt.Errorf("BVN_PROVIDERS contains duplicate provider %q", name)
		}

		seen[name] = true

		switch name {
		case "swiftend":
			value, err := swiftend.New(swiftend.Config{
				BaseURL:   cfg.SwiftendBaseURL,
				ServiceID: cfg.SwiftendServiceID,
				Timeout:   timeout,
			})
			if err != nil {
				return nil, nil, fmt.Errorf("swiftend: %w", err)
			}

			directory = value
			ordered = append(ordered, value)

		case "dojah":
			value, err := dojah.New(dojah.Config{
				BaseURL:   cfg.DojahBaseURL,
				AppID:     cfg.DojahAppID,
				SecretKey: cfg.DojahSecretKey,
				Timeout:   timeout,
			})
			if err != nil {
				return nil, nil, fmt.Errorf("dojah: %w", err)
			}
			ordered = append(ordered, value)

		default:
			return nil, nil, fmt.Errorf("unsupported BVN provider %q", name)
		}
	}

	if len(ordered) == 0 {
		return unavailableIdentity{}, nil, nil
	}

	bvn, err := providers.NewFailoverIdentityProvider(cfg.BVNFailover, ordered...)
	if err != nil {
		return nil, nil, err
	}

	var nin providers.IdentityProvider = unavailableIdentity{}

	if cfg.NINAuthBaseURL != "" && cfg.NINAuthAPIKey != "" {
		value, createErr := ninauth.New(ninauth.Config{
			BaseURL: cfg.NINAuthBaseURL,
			APIKey:  cfg.NINAuthAPIKey,
			Timeout: timeout,
		})

		if createErr == nil && value != nil {
			nin = value
		}
	}

	return splitIdentity{
		bvn: bvn,
		nin: nin,
	}, directory, nil
}

func objectStorage(ctx context.Context, cfg *config.Config) (providers.ObjectStorage, error) {
	if strings.EqualFold(cfg.ObjectStorageProvider, "local") {
		return providers.NewLocalObjectStorage(cfg.DocumentStoragePath)
	}

	if cfg.ObjectStorageBucket == "" {
		return nil, nil
	}

	return providers.NewS3ObjectStorage(ctx, providers.S3StorageConfig{
		Region:          cfg.ObjectStorageRegion,
		Bucket:          cfg.ObjectStorageBucket,
		Endpoint:        cfg.ObjectStorageEndpoint,
		AccessKeyID:     cfg.ObjectStorageAccessKeyID,
		SecretAccessKey: cfg.ObjectStorageSecretAccessKey,
		ForcePathStyle:  cfg.ObjectStorageForcePathStyle,
	})
}

type unavailableIdentity struct{}

func (unavailableIdentity) Name() string { return "unconfigured" }

func (unavailableIdentity) VerifyBVN(context.Context, string) (*providers.IdentityVerification, error) {
	return nil, providers.ErrNotConfigured
}

func (unavailableIdentity) VerifyNIN(context.Context, string) (*providers.IdentityVerification, error) {
	return nil, providers.ErrNotConfigured
}

type splitIdentity struct{ bvn, nin providers.IdentityProvider }

func (p splitIdentity) Name() string { return p.bvn.Name() + "/" + p.nin.Name() }

func (p splitIdentity) VerifyBVN(ctx context.Context, value string) (*providers.IdentityVerification, error) {
	return p.bvn.VerifyBVN(ctx, value)
}

func (p splitIdentity) VerifyNIN(ctx context.Context, value string) (*providers.IdentityVerification, error) {
	return p.nin.VerifyNIN(ctx, value)
}

func (p splitIdentity) VerifyNINLiveness(ctx context.Context, input providers.NINLivenessInput) (*providers.NINSelfieVerification, error) {
	provider, ok := p.nin.(providers.NINLivenessProvider)
	if !ok {
		return nil, providers.ErrNotConfigured
	}
	return provider.VerifyNINLiveness(ctx, input)
}

func (p splitIdentity) VerifyBVNLiveness(ctx context.Context, input providers.BVNLivenessInput) (*providers.BVNSelfieVerification, error) {
	provider, ok := p.bvn.(providers.BVNLivenessProvider)
	if !ok {
		return nil, providers.ErrNotConfigured
	}
	return provider.VerifyBVNLiveness(ctx, input)
}
