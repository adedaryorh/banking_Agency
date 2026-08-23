package facededup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"nabla/identity-svc/internal/providers"
)

type Provider struct {
	client        *providers.Client
	httpClient    *http.Client
	licenseKey    string
	baseURL       string
	mockNINVerify bool
}

func New(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.LicenseKey) == "" {
		return nil, providers.ErrNotConfigured
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = "https://facededup.ai"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	return &Provider{
		client: providers.NewClient(cfg.BaseURL, cfg.Timeout, map[string]string{
			"X-License-Key": cfg.LicenseKey,
		}),
		httpClient:    &http.Client{Timeout: cfg.Timeout},
		licenseKey:    cfg.LicenseKey,
		baseURL:       cfg.BaseURL,
		mockNINVerify: cfg.MockNINVerify,
	}, nil
}

func (*Provider) Name() string { return "facededup" }

func (p *Provider) IsMockNINLiveness() bool {
	return p != nil && p.mockNINVerify
}

// calls FaceDedup's /v1/verify endpoint to validate liveness
func (p *Provider) VerifyLiveness(ctx context.Context, req LivenessVerifyRequest) (*LivenessVerifyResponse, error) {
	// Log request (without sensitive image data)
	log.Printf("[FaceDedup] Liveness verification request: request_id=%s, session_id=%s, frames=%d",
		req.RequestID, req.SessionID, len(req.Frames))

	res, err := p.client.Do(ctx, providers.Request{
		Method: http.MethodPost,
		Path:   "/v1/verify",
		Body:   req,
	})
	if err != nil {
		log.Printf("[FaceDedup] Liveness verification failed: %v", err)
		return nil, fmt.Errorf("facededup liveness verification failed: %w", err)
	}

	var response LivenessVerifyResponse
	if err := providers.DecodeInto(res, &response); err != nil {
		log.Printf("[FaceDedup] Failed to decode liveness response: %v", err)
		return nil, fmt.Errorf("failed to decode facededup liveness response: %w", err)
	}

	// Log response
	if jsonData, err := json.MarshalIndent(response, "", "  "); err == nil {
		log.Printf("[FaceDedup] Liveness verification response: %s", string(jsonData))
	}

	// Validate response
	if response.ResultToken == "" {
		log.Printf("[FaceDedup] Missing result token in liveness response")
		return nil, fmt.Errorf("facededup liveness response missing result_token")
	}

	return &response, nil
}

// VerifyIdentity calls FaceDedup's /v1/identity/verify endpoint for NIN/BVN 1:1 match
func (p *Provider) VerifyIdentity(ctx context.Context, req IdentityVerifyRequest) (*IdentityVerifyResponse, error) {
	// Mask ID number for logging
	maskedID := maskIdentityNumber(req.IDNumber)
	log.Printf("[FaceDedup] Identity verification request: id_type=%s, id_number=%s",
		req.IDType, maskedID)

	res, err := p.client.Do(ctx, providers.Request{
		Method: http.MethodPost,
		Path:   "/v1/identity/verify",
		Body:   req,
	})
	if err != nil {
		log.Printf("[FaceDedup] Identity verification failed for %s %s: %v",
			req.IDType, maskedID, err)
		return nil, fmt.Errorf("facededup identity verification failed: %w", err)
	}

	var response IdentityVerifyResponse
	if err := providers.DecodeInto(res, &response); err != nil {
		log.Printf("[FaceDedup] Failed to decode identity response: %v", err)
		return nil, fmt.Errorf("failed to decode facededup identity response: %w", err)
	}

	// Log response (safely, without PII)
	log.Printf("[FaceDedup] Identity verification response: id_type=%s, found=%t, decision=%s, match_score=%.2f, source=%s",
		response.IDType, response.Found, response.Decision, response.MatchScore, response.Source)

	return &response, nil
}

//	calls WhoisID/FaceDedup compare-v2. Mobile already completed
//
// liveness and sends the encrypted metadata; the backend fetches the authority NIN photo and compares it against the live selfie.
func (p *Provider) CompareFaces(ctx context.Context, metadata, authorityImageBase64, selfieImageBase64, requestID string) (*CompareVerifyResponse, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{
		"metadata":       metadata,
		"image_a_base64": authorityImageBase64,
		"image_b_base64": selfieImageBase64,
		"request_id":     requestID,
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return nil, fmt.Errorf("build facededup compare request: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close facededup compare request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL, &body)
	if err != nil {
		return nil, fmt.Errorf("build facededup compare request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-License-Key", p.licenseKey)

	res, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: facededup compare request: %v", providers.ErrUnavailable, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read facededup compare response: %v", providers.ErrIndeterminate, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if res.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: facededup compare status %d", providers.ErrNotFound, res.StatusCode)
		}
		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
			return nil, fmt.Errorf("%w: facededup compare status %d: %s", providers.ErrUnavailable, res.StatusCode, compareSnippet(raw))
		}
		return nil, fmt.Errorf("%w: facededup compare status %d: %s", providers.ErrRejected, res.StatusCode, compareSnippet(raw))
	}

	var response CompareVerifyResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("%w: decode facededup compare response: %v", providers.ErrUnavailable, err)
	}
	response.Raw = append(response.Raw[:0], raw...)
	return &response, nil
}

func compareSnippet(raw []byte) string {
	const max = 300
	if len(raw) <= max {
		return string(raw)
	}
	return string(raw[:max]) + "..."
}

// maskIdentityNumber masks all but last 4 digits of identity number
func maskIdentityNumber(idNumber string) string {
	if len(idNumber) <= 4 {
		return "****"
	}
	return strings.Repeat("*", len(idNumber)-4) + idNumber[len(idNumber)-4:]
}

// VerifyBVN is a basic BVN verification method (for backward compatibility)
// For biometric verification with liveness, use VerifyBVNLiveness instead
func (p *Provider) VerifyBVN(ctx context.Context, bvn string) (*providers.IdentityVerification, error) {
	return nil, fmt.Errorf("FaceDedup requires liveness verification - use VerifyBVNLiveness instead")
}

// VerifyNIN is a basic NIN verification method (for backward compatibility)
// For biometric verification with liveness, use VerifyNINLiveness instead
func (p *Provider) VerifyNIN(ctx context.Context, nin string) (*providers.IdentityVerification, error) {
	// FaceDedup requires liveness verification for biometric checks
	// This method is for backward compatibility but should not be used
	return nil, fmt.Errorf("FaceDedup requires liveness verification - use VerifyNINLiveness instead")
}

// Ensure Provider implements IdentityProvider interface
var _ providers.IdentityProvider = (*Provider)(nil)
