package facededup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"nabla/identity-svc/internal/providers"
)

func (p *Provider) VerifyNINLiveness(ctx context.Context, input providers.NINLivenessInput) (*providers.NINSelfieVerification, error) {
	requestID := input.VerificationRequestID
	log.Printf("[FaceDedup NIN] Starting identity verification for request_id=%s", requestID)
	if p.mockNINVerify {
		log.Printf("[FaceDedup NIN] Mock NIN verification enabled for request_id=%s", requestID)
		return mockNINVerification(input), nil
	}

	livenessResp, err := livenessFromPayload(input.EncryptedLivenessPayload, input.ResultToken, requestID)
	if err != nil {
		return nil, err
	}

	livenessPassed := false
	livenessScore := livenessResp.Score
	if livenessResp.Outcome == "live" || livenessScore >= 0.6 {
		livenessPassed = true
	} else if livenessScore >= 0.53 {
		log.Printf("[FaceDedup NIN] Liveness referred with score %.2f", livenessScore)
	} else {
		log.Printf("[FaceDedup NIN] Liveness failed with score %.2f", livenessScore)
	}

	if !livenessPassed {
		log.Printf("[FaceDedup NIN] Liveness check failed: outcome=%s, score=%.2f",
			livenessResp.Outcome, livenessScore)
		return &providers.NINSelfieVerification{
			Identity:       &providers.IdentityVerification{Provider: p.Name(), Verified: false, Reference: livenessResp.RequestID, Raw: faceDedupRawPayload(livenessResp, nil)},
			LivenessPassed: false,
			LivenessScore:  livenessScore,
			FaceMatched:    false,
			FaceMatchScore: 0,
		}, nil
	}

	log.Printf("[FaceDedup NIN] Liveness verified successfully: score=%.2f, result_token=%s",
		livenessScore, maskToken(livenessResp.ResultToken))

	identityReq := IdentityVerifyRequest{
		IDType:      "nin",
		IDNumber:    input.NIN,
		SelfieB64:   input.SelfieImageBase64,
		ResultToken: livenessResp.ResultToken,
	}

	identityResp, err := p.VerifyIdentity(ctx, identityReq)
	if err != nil {
		return nil, fmt.Errorf("identity verification failed: %w", err)
	}

	// Step 5: Evaluate face match result
	// NCC §4 band: ≥85 match · 70–84 refer · <70 no_match
	faceMatched := false
	matchScore := identityResp.MatchScore

	if identityResp.Decision == "match" || matchScore >= 85.0 {
		faceMatched = true
	} else if matchScore >= 70.0 {
		// Refer decision
		log.Printf("[FaceDedup NIN] Face match referred with score %.2f", matchScore)
	} else {
		log.Printf("[FaceDedup NIN] Face match failed with score %.2f", matchScore)
	}

	if !identityResp.Found {
		log.Printf("[FaceDedup NIN] Identity not found in authority database")
		return nil, providers.ErrUnavailable
	}

	// Step 6: Build identity verification result
	identity := &providers.IdentityVerification{
		Provider:   p.Name(),
		Verified:   faceMatched,
		Reference:  livenessResp.RequestID,
		MatchScore: int(matchScore),
	}
	identity.Raw = faceDedupRawPayload(livenessResp, identityResp)

	// Map profile data if available
	if identityResp.Profile != nil {
		identity.FirstName = identityResp.Profile.FirstName
		identity.MiddleName = identityResp.Profile.MiddleName
		identity.LastName = identityResp.Profile.LastName
		identity.Gender = identityResp.Profile.Gender
		identity.PhoneNumber = identityResp.Profile.PhoneNumber

		if dob := identityResp.Profile.DateOfBirth; dob != "" {
			if parsed, ok := parseDate(dob); ok {
				identity.DateOfBirth = &parsed
			}
		}
	}
	if identity.FirstName == "" && strings.TrimSpace(identityResp.FullName) != "" {
		parts := strings.Fields(identityResp.FullName)
		if len(parts) > 0 {
			identity.FirstName = parts[0]
		}
		if len(parts) > 1 {
			identity.LastName = parts[len(parts)-1]
		}
		if len(parts) > 2 {
			identity.MiddleName = strings.Join(parts[1:len(parts)-1], " ")
		}
	}
	if identity.Gender == "" {
		identity.Gender = identityResp.Gender
	}
	if identity.PhoneNumber == "" {
		identity.PhoneNumber = identityResp.PhoneNumber
	}
	if identity.DateOfBirth == nil && identityResp.DateOfBirth != "" {
		if parsed, ok := parseDate(identityResp.DateOfBirth); ok {
			identity.DateOfBirth = &parsed
		}
	}
	result := &providers.NINSelfieVerification{
		Identity:       identity,
		LivenessPassed: livenessPassed,
		LivenessScore:  livenessScore,
		FaceMatched:    faceMatched,
		FaceMatchScore: matchScore,
	}

	log.Printf("[FaceDedup NIN] Verification complete: liveness_passed=%t, face_matched=%t, match_score=%.2f",
		livenessPassed, faceMatched, matchScore)

	return result, nil
}

func livenessFromPayload(encryptedPayload, explicitResultToken, requestID string) (*LivenessVerifyResponse, error) {
	explicitResultToken = strings.TrimSpace(explicitResultToken)
	if explicitResultToken != "" {
		return &LivenessVerifyResponse{
			RequestID:       requestID,
			Outcome:         "live",
			Score:           1,
			ResultToken:     explicitResultToken,
			DecisionID:      requestID,
			FaceGenuineness: "GENUINE",
		}, nil
	}

	payload := strings.TrimSpace(encryptedPayload)
	if payload == "" {
		return nil, fmt.Errorf("%w: missing encrypted_liveness_payload", providers.ErrRejected)
	}

	values := possibleJSONPayloads(payload)
	for _, value := range values {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			continue
		}
		token := findString(decoded, "result_token", "resultToken")
		if token == "" {
			continue
		}
		outcome := strings.ToLower(firstNonEmpty(
			findString(decoded, "outcome"),
			findString(decoded, "status"),
			findString(decoded, "decision"),
		))
		if outcome == "success" || outcome == "passed" || outcome == "verified" || outcome == "match" {
			outcome = "live"
		}
		if outcome == "" {
			outcome = "live"
		}
		score := findFloat(decoded, "score", "liveness_score", "livenessScore")
		if score == 0 {
			score = 1
		}
		return &LivenessVerifyResponse{
			DecisionID:  firstNonEmpty(findString(decoded, "decision_id", "decisionId"), requestID),
			RequestID:   firstNonEmpty(findString(decoded, "request_id", "requestId"), requestID),
			Outcome:     outcome,
			Score:       score,
			ResultToken: token,
		}, nil
	}

	return nil, fmt.Errorf("%w: unable to extract facededup result_token from encrypted_liveness_payload", providers.ErrRejected)
}

func mockNINVerification(input providers.NINLivenessInput) *providers.NINSelfieVerification {
	reference := firstNonEmpty(input.VerificationRequestID, "mock-nin-verification")
	identity := &providers.IdentityVerification{
		Provider:   "facededup_mock",
		Verified:   true,
		Reference:  reference,
		NIN:        input.NIN,
		FirstName:  "Mock",
		LastName:   "User",
		MatchScore: 100,
		Raw:        []byte(`{"provider":"facededup_mock","decision":"match","match_score":100,"found":true}`),
	}
	return &providers.NINSelfieVerification{
		Identity:       identity,
		LivenessPassed: true,
		LivenessScore:  1,
		FaceMatched:    true,
		FaceMatchScore: 100,
	}
}

func possibleJSONPayloads(value string) []string {
	values := []string{value}
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "Bearer ")
	trimmed = strings.TrimPrefix(trimmed, "FD")
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, encoding := range encodings {
		if raw, err := encoding.DecodeString(trimmed); err == nil && len(raw) > 0 {
			values = append(values, string(raw))
		}
	}
	return values
}

func findString(value any, keys ...string) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if raw, ok := typed[key]; ok {
				if text := findString(raw); text != "" {
					return text
				}
			}
		}
		for _, raw := range typed {
			if text := findString(raw, keys...); text != "" {
				return text
			}
		}
	case []any:
		for _, raw := range typed {
			if text := findString(raw, keys...); text != "" {
				return text
			}
		}
	case string:
		return strings.TrimSpace(typed)
	}
	return ""
}

func findFloat(value any, keys ...string) float64 {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if raw, ok := typed[key]; ok {
				if number := findFloat(raw); number != 0 {
					return number
				}
			}
		}
		for _, raw := range typed {
			if number := findFloat(raw, keys...); number != 0 {
				return number
			}
		}
	case []any:
		for _, raw := range typed {
			if number := findFloat(raw, keys...); number != 0 {
				return number
			}
		}
	case float64:
		return typed
	case json.Number:
		if number, err := typed.Float64(); err == nil {
			return number
		}
	}
	return 0
}
func parseDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	layouts := []string{
		"2006-01-02",
		"02-01-2006",
		"02/01/2006",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05-07:00",
	}

	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// VerifyNINWithSelfie is a simplified wrapper for backward compatibility
func (p *Provider) VerifyNINWithSelfie(ctx context.Context, nin string, selfieBase64 string) (*providers.NINSelfieVerification, error) {
	// This method is for backward compatibility with the old NINSelfieProvider interface
	// The proper flow should use VerifyNINLiveness with full liveness data
	return nil, fmt.Errorf("use VerifyNINLiveness for FaceDedup biometric verification")
}

// maskToken masks token for logging
func maskToken(token string) string {
	if len(token) <= 8 {
		return "****"
	}
	return token[:4] + "..." + token[len(token)-4:]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// Ensure Provider implements NINLivenessProvider interface
var _ providers.NINLivenessProvider = (*Provider)(nil)

var _ providers.NINSelfieProvider = (*Provider)(nil)
