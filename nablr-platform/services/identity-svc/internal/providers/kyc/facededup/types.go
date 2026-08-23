package facededup

import (
	"encoding/json"
	"time"
)

// Config holds FaceDedup API configuration
type Config struct {
	BaseURL       string
	LicenseKey    string
	MockNINVerify bool
	Timeout       time.Duration
}

// LivenessVerifyRequest represents the liveness verification request to FaceDedup
type LivenessVerifyRequest struct {
	RequestID        string          `json:"request_id"`
	SessionID        string          `json:"session_id"`
	Nonce            string          `json:"nonce"`
	Frames           []LivenessFrame `json:"frames"`
	AudioPresent     bool            `json:"audio_present"`
	AttestationToken string          `json:"attestation_token,omitempty"`
	Illumination     *Illumination   `json:"illumination,omitempty"`
	PAD              *PADInfo        `json:"pad,omitempty"`
}

// LivenessFrame represents a single frame in liveness verification
type LivenessFrame struct {
	ImageB64     string  `json:"image_b64"`
	ProvesAction *string `json:"proves_action"` // null for portrait frames
}

// Illumination represents active-illumination reflection evidence
type Illumination struct {
	Responded bool    `json:"responded"`
	Delta     float64 `json:"delta"`
}

// PADInfo represents on-device screen/replay Presentation Attack Detection
type PADInfo struct {
	ScreenScore float64 `json:"screen_score"`
	Verdict     string  `json:"verdict"` // "genuine" or other
}

// LivenessVerifyResponse represents the response from liveness verification
type LivenessVerifyResponse struct {
	DecisionID          string  `json:"decision_id,omitempty"`
	RequestID           string  `json:"request_id"`
	SessionID           string  `json:"session_id"`
	Outcome             string  `json:"outcome"` // "live", "not_live", "referred"
	Score               float64 `json:"score"`
	Threshold           float64 `json:"threshold,omitempty"`
	AttestationVerified bool    `json:"attestation_verified,omitempty"`
	FaceGenuineness     string  `json:"face_genuineness,omitempty"`
	PADGate             *struct {
		Triggered bool     `json:"triggered"`
		Reasons   []string `json:"reasons"`
	} `json:"pad_gate,omitempty"`
	ResultToken string `json:"result_token"` // Single-use token for identity step
	Timestamp   string `json:"timestamp"`
	Checks      []struct {
		Name   string `json:"name"`
		Passed bool   `json:"passed"`
	} `json:"checks,omitempty"`
	Details *struct {
		LivenessScore float64 `json:"liveness_score"`
		Quality       float64 `json:"quality"`
		Confidence    float64 `json:"confidence"`
	} `json:"details,omitempty"`
}

// IdentityVerifyRequest represents the identity verification request (1:1 match)
type IdentityVerifyRequest struct {
	IDType      string `json:"id_type"`      // "nin" or "bvn"
	IDNumber    string `json:"id_number"`    // NIN or BVN number
	SelfieB64   string `json:"selfie_b64"`   // Live selfie from liveness check
	ResultToken string `json:"result_token"` // Token from liveness verification
}

// IdentityVerifyResponse represents the response from identity verification
type IdentityVerifyResponse struct {
	RequestID         string  `json:"request_id"`
	IDType            string  `json:"id_type"`
	IDNumber          string  `json:"id_number"` // May be masked/redacted
	IDNumberMasked    string  `json:"id_number_masked,omitempty"`
	Found             bool    `json:"found"`
	Decision          string  `json:"decision"`    // "match", "no_match", "refer"
	MatchScore        float64 `json:"match_score"` // Face match confidence
	FullName          string  `json:"full_name,omitempty"`
	DateOfBirth       string  `json:"date_of_birth,omitempty"`
	Gender            string  `json:"gender,omitempty"`
	PhoneNumber       string  `json:"phone_number,omitempty"`
	Source            string  `json:"source"` // Authority source (e.g., "NIMC", "BVN")
	AuthorityPhotoB64 string  `json:"authority_photo_b64,omitempty"`
	Spoof             *bool   `json:"spoof,omitempty"`
	Timestamp         string  `json:"timestamp"`
	Profile           *struct {
		FirstName   string `json:"first_name"`
		MiddleName  string `json:"middle_name,omitempty"`
		LastName    string `json:"last_name"`
		DateOfBirth string `json:"date_of_birth,omitempty"`
		Gender      string `json:"gender,omitempty"`
		PhoneNumber string `json:"phone_number,omitempty"`
	} `json:"profile,omitempty"`
}

// CompareVerifyResponse represents the WhoisID/FaceDedup compare-v2 response.
type CompareVerifyResponse struct {
	DecisionID          string          `json:"decision_id,omitempty"`
	RequestID           string          `json:"request_id,omitempty"`
	Outcome             string          `json:"outcome,omitempty"`
	Decision            string          `json:"decision,omitempty"`
	Score               float64         `json:"score,omitempty"`
	MatchScore          float64         `json:"match_score,omitempty"`
	Threshold           float64         `json:"threshold,omitempty"`
	AttestationVerified bool            `json:"attestation_verified,omitempty"`
	FaceGenuineness     string          `json:"face_genuineness,omitempty"`
	ResultToken         string          `json:"result_token,omitempty"`
	PADGate             json.RawMessage `json:"pad_gate,omitempty"`
	Checks              json.RawMessage `json:"checks,omitempty"`
	Raw                 json.RawMessage `json:"-"`
}
