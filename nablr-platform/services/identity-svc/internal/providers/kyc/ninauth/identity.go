package ninauth

import (
	"context"
	"nabla/identity-svc/internal/providers"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	BaseURL, Path, APIKey string
	Timeout               time.Duration
}
type Provider struct {
	client *providers.Client
	path   string
}

func New(c Config) (*Provider, error) {
	if c.BaseURL == "" || c.APIKey == "" {
		return nil, providers.ErrNotConfigured
	}
	if c.Path == "" {
		c.Path = "/api/v1/nin/verify"
	}
	return &Provider{client: providers.NewClient(c.BaseURL, c.Timeout, map[string]string{"Authorization": "Bearer " + c.APIKey}), path: c.Path}, nil
}
func (p *Provider) Name() string { return "ninauth" }

func (p *Provider) VerifyBVN(context.Context, string) (*providers.IdentityVerification, error) {
	return nil, providers.ErrNotConfigured
}
func (p *Provider) VerifyNIN(ctx context.Context, nin string) (*providers.IdentityVerification, error) {
	r, e := p.client.Do(ctx, providers.Request{Method: http.MethodPost, Path: p.path, Body: map[string]any{"nin": nin, "consent": true}})
	if e != nil {
		return nil, e
	}
	var d struct {
		Success        bool   `json:"success"`
		Status         bool   `json:"status"`
		Reference      string `json:"reference"`
		VerificationID string `json:"verification_id"`
		Data           struct {
			FirstName   string `json:"first_name"`
			Firstname   string `json:"firstname"`
			MiddleName  string `json:"middle_name"`
			Lastname    string `json:"last_name"`
			Surname     string `json:"surname"`
			DateOfBirth string `json:"date_of_birth"`
			Birthdate   string `json:"birthdate"`
			Gender      string `json:"gender"`
			Phone       string `json:"phone_number"`
			Telephone   string `json:"telephoneno"`
		} `json:"data"`
	}
	if e := providers.DecodeInto(r, &d); e != nil {
		return nil, e
	}
	if !d.Success && !d.Status {
		return nil, providers.ErrRejected
	}
	v := &providers.IdentityVerification{Verified: true, Reference: first(d.Reference, d.VerificationID), FirstName: first(d.Data.FirstName, d.Data.Firstname), MiddleName: d.Data.MiddleName, LastName: first(d.Data.Lastname, d.Data.Surname), Gender: d.Data.Gender, PhoneNumber: first(d.Data.Phone, d.Data.Telephone), MatchScore: 100, Raw: r.Body}
	if t, ok := date(first(d.Data.DateOfBirth, d.Data.Birthdate)); ok {
		v.DateOfBirth = &t
	}
	return v, nil
}
func (p *Provider) VerifyNINWithSelfie(ctx context.Context, nin, selfie string) (*providers.NINSelfieVerification, error) {
	return p.VerifyNINLiveness(ctx, providers.NINLivenessInput{NIN: nin, SelfieImageBase64: selfie})
}
func (p *Provider) VerifyNINLiveness(ctx context.Context, in providers.NINLivenessInput) (*providers.NINSelfieVerification, error) {
	r, e := p.client.Do(ctx, providers.Request{Method: http.MethodPost, Path: p.path, Body: map[string]any{"nin": in.NIN, "selfie_image": in.SelfieImageBase64, "encrypted_payload": in.EncryptedLivenessPayload, "frame_images": in.LivenessFrameImagesBase64, "subject_id": in.UserID, "request_id": in.VerificationRequestID, "consent": true}})
	if e != nil {
		return nil, e
	}
	var d struct {
		Success        bool   `json:"success"`
		Status         bool   `json:"status"`
		Reference      string `json:"reference"`
		VerificationID string `json:"verification_id"`
		Data           struct {
			FirstName      string  `json:"first_name"`
			Firstname      string  `json:"firstname"`
			MiddleName     string  `json:"middle_name"`
			LastName       string  `json:"last_name"`
			Surname        string  `json:"surname"`
			DateOfBirth    string  `json:"date_of_birth"`
			Birthdate      string  `json:"birthdate"`
			Gender         string  `json:"gender"`
			Phone          string  `json:"phone_number"`
			Telephone      string  `json:"telephoneno"`
			LivenessPassed bool    `json:"liveness_passed"`
			LivenessScore  float64 `json:"liveness_score"`
			FaceMatched    bool    `json:"face_matched"`
			FaceMatchScore float64 `json:"face_match_score"`
		} `json:"data"`
		Entity struct {
			Liveness struct {
				Passed      bool    `json:"passed"`
				Check       bool    `json:"liveness_check"`
				Score       float64 `json:"score"`
				Probability float64 `json:"liveness_probability"`
			} `json:"liveness"`
			FaceMatch struct {
				Match      bool    `json:"match"`
				Score      float64 `json:"score"`
				Confidence float64 `json:"confidence_value"`
			} `json:"face_match"`
		} `json:"entity"`
	}
	if e = providers.DecodeInto(r, &d); e != nil {
		return nil, e
	}
	if !d.Success && !d.Status {
		return nil, providers.ErrRejected
	}
	ls := d.Data.LivenessScore
	if ls == 0 {
		ls = firstFloat(d.Entity.Liveness.Score, d.Entity.Liveness.Probability)
	}
	fs := d.Data.FaceMatchScore
	if fs == 0 {
		fs = firstFloat(d.Entity.FaceMatch.Score, d.Entity.FaceMatch.Confidence)
	}
	id := &providers.IdentityVerification{Verified: true, Reference: first(d.Reference, d.VerificationID), FirstName: first(d.Data.FirstName, d.Data.Firstname), MiddleName: d.Data.MiddleName, LastName: first(d.Data.LastName, d.Data.Surname), Gender: d.Data.Gender, PhoneNumber: first(d.Data.Phone, d.Data.Telephone), MatchScore: int(fs), Raw: r.Body}
	if t, ok := date(first(d.Data.DateOfBirth, d.Data.Birthdate)); ok {
		id.DateOfBirth = &t
	}
	return &providers.NINSelfieVerification{Identity: id, LivenessPassed: d.Data.LivenessPassed || d.Entity.Liveness.Passed || d.Entity.Liveness.Check, LivenessScore: ls, FaceMatched: d.Data.FaceMatched || d.Entity.FaceMatch.Match, FaceMatchScore: fs}, nil
}
func firstFloat(v ...float64) float64 {
	for _, n := range v {
		if n != 0 {
			return n
		}
	}
	return 0
}
func first(v ...string) string {
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}
func date(v string) (time.Time, bool) {
	for _, l := range []string{"2006-01-02", "02-01-2006", "02/01/2006"} {
		if t, e := time.Parse(l, strings.TrimSpace(v)); e == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
