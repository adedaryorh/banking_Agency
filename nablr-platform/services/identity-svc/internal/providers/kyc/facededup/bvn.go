package facededup

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"nabla/identity-svc/internal/providers"
)

func (p *Provider) VerifyBVNLiveness(ctx context.Context, input providers.BVNLivenessInput) (*providers.BVNSelfieVerification, error) {
	log.Printf("[FaceDedup BVN] Starting liveness verification for request_id=%s", input.VerificationRequestID)
	requestID := input.VerificationRequestID
	sessionID := input.UserID

	log.Printf("[FaceDedup BVN] Using request_id=%s, session_id=%s", requestID, sessionID)

	frames := make([]LivenessFrame, 0, len(input.LivenessFrameImagesBase64)+1)
	for _, frameB64 := range input.LivenessFrameImagesBase64 {
		if strings.TrimSpace(frameB64) != "" {
			frames = append(frames, LivenessFrame{
				ImageB64:     frameB64,
				ProvesAction: nil,
			})
		}
	}
	if strings.TrimSpace(input.SelfieImageBase64) != "" {
		frames = append(frames, LivenessFrame{
			ImageB64:     input.SelfieImageBase64,
			ProvesAction: nil,
		})
	}

	livenessReq := LivenessVerifyRequest{
		RequestID:    requestID,
		SessionID:    sessionID,
		Nonce:        "",
		Frames:       frames,
		AudioPresent: false,
	}
	//Verify liveness server-side with FaceDedup
	livenessResp, err := p.VerifyLiveness(ctx, livenessReq)
	if err != nil {
		return nil, fmt.Errorf("server-side liveness verification failed: %w", err)
	}
	livenessPassed := false
	livenessScore := livenessResp.Score
	if livenessResp.Outcome == "live" || livenessScore >= 0.6 {
		livenessPassed = true
	} else if livenessScore >= 0.53 {
		log.Printf("[FaceDedup BVN] Liveness referred with score %.2f", livenessScore)
	} else {
		log.Printf("[FaceDedup BVN] Liveness failed with score %.2f", livenessScore)
	}
	if !livenessPassed {
		log.Printf("[FaceDedup BVN] Liveness check failed: outcome=%s, score=%.2f",
			livenessResp.Outcome, livenessScore)
		return &providers.BVNSelfieVerification{
			Identity:       &providers.IdentityVerification{Provider: p.Name(), Verified: false, Reference: livenessResp.RequestID, Raw: faceDedupRawPayload(livenessResp, nil)},
			LivenessPassed: false,
			LivenessScore:  livenessScore,
			FaceMatched:    false,
			FaceMatchScore: 0,
		}, nil
	}

	log.Printf("[FaceDedup BVN] Liveness verified successfully: score=%.2f, result_token=%s",
		livenessScore, maskToken(livenessResp.ResultToken))

	// Perform identity verification (1:1 face match with BVN authority photo)
	identityReq := IdentityVerifyRequest{
		IDType:      "bvn",
		IDNumber:    input.BVN,
		SelfieB64:   input.SelfieImageBase64,
		ResultToken: livenessResp.ResultToken,
	}

	identityResp, err := p.VerifyIdentity(ctx, identityReq)
	if err != nil {
		return nil, fmt.Errorf("identity verification failed: %w", err)
	}
	faceMatched := false
	matchScore := identityResp.MatchScore

	if identityResp.Decision == "match" || matchScore >= 85.0 {
		faceMatched = true
	} else if matchScore >= 70.0 {
		log.Printf("[FaceDedup BVN] Face match referred with score %.2f", matchScore)
	} else {
		log.Printf("[FaceDedup BVN] Face match failed with score %.2f", matchScore)
	}

	if !identityResp.Found {
		log.Printf("[FaceDedup BVN] Identity not found in authority database")
		return nil, providers.ErrUnavailable
	}
	identity := &providers.IdentityVerification{
		Provider:   p.Name(),
		Verified:   faceMatched,
		Reference:  livenessResp.RequestID,
		MatchScore: int(matchScore),
	}
	identity.Raw = faceDedupRawPayload(livenessResp, identityResp)
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

	result := &providers.BVNSelfieVerification{
		Identity:       identity,
		LivenessPassed: livenessPassed,
		LivenessScore:  livenessScore,
		FaceMatched:    faceMatched,
		FaceMatchScore: matchScore,
	}

	log.Printf("[FaceDedup BVN] Verification complete: liveness_passed=%t, face_matched=%t, match_score=%.2f",
		livenessPassed, faceMatched, matchScore)

	return result, nil
}

func faceDedupRawPayload(livenessResp *LivenessVerifyResponse, identityResp *IdentityVerifyResponse) []byte {
	if livenessResp == nil && identityResp == nil {
		return nil
	}
	payload := map[string]any{
		"provider": "facededup",
	}
	if livenessResp != nil {
		livenessCopy := *livenessResp
		if livenessCopy.ResultToken != "" {
			livenessCopy.ResultToken = maskToken(livenessCopy.ResultToken)
		}
		payload["liveness"] = livenessCopy
	}
	if identityResp != nil {
		payload["identity"] = identityResp
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return raw
}

func faceDedupCompareRawPayload(compareResp *CompareVerifyResponse) []byte {
	if compareResp == nil {
		return nil
	}
	payload := map[string]any{
		"provider": "facededup",
		"compare":  compareResp,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return raw
}

func (p *Provider) VerifyBVNWithSelfie(ctx context.Context, bvn string, selfieBase64 string) (*providers.BVNSelfieVerification, error) {
	return nil, fmt.Errorf("use VerifyBVNLiveness for FaceDedup biometric verification")
}

var _ providers.BVNSelfieProvider = (*Provider)(nil)
