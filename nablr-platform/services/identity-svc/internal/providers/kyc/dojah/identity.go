package dojah

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nabla/identity-svc/internal/providers"
)

type Config struct {
	BaseURL, AppID, SecretKey string
	Timeout                   time.Duration
}
type Provider struct{ client *providers.Client }

func New(cfg Config) (*Provider, error) {
	if cfg.AppID == "" || cfg.SecretKey == "" {
		return nil, providers.ErrNotConfigured
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.dojah.io"
	}
	return &Provider{client: providers.NewClient(cfg.BaseURL, cfg.Timeout, map[string]string{
		"AppId": cfg.AppID, "Authorization": cfg.SecretKey,
	})}, nil
}
func (p *Provider) Name() string { return "dojah" }

type response struct {
	Reference string `json:"reference"`
	Entity    struct {
		BVN struct {
			FirstName    string `json:"first_name"`
			MiddleName   string `json:"middle_name"`
			LastName     string `json:"last_name"`
			DateOfBirth  string `json:"date_of_birth"`
			Gender       string `json:"gender"`
			PhoneNumber1 string `json:"phone_number1"`
			PhoneNumber  string `json:"phone_number"`
			Image        string `json:"image"`
		} `json:"bvn"`
		FirstName    string `json:"first_name"`
		MiddleName   string `json:"middle_name"`
		LastName     string `json:"last_name"`
		DateOfBirth  string `json:"date_of_birth"`
		Gender       string `json:"gender"`
		PhoneNumber1 string `json:"phone_number1"`
		PhoneNumber  string `json:"phone_number"`
		Image        string `json:"image"`
	} `json:"entity"`
}

func (p *Provider) VerifyBVN(ctx context.Context, bvn string) (*providers.IdentityVerification, error) {
	res, err := p.client.Do(ctx, providers.Request{Method: http.MethodGet, Path: "/api/v1/kyc/bvn/full", Query: url.Values{"bvn": {bvn}}})
	if err != nil {
		return nil, err
	}
	var decoded response
	if err := providers.DecodeInto(res, &decoded); err != nil {
		return nil, err
	}
	e := decoded.Entity
	b := e.BVN
	result := &providers.IdentityVerification{Provider: p.Name(), Verified: true, Reference: decoded.Reference, FirstName: first(b.FirstName, e.FirstName), MiddleName: first(b.MiddleName, e.MiddleName), LastName: first(b.LastName, e.LastName), Gender: first(b.Gender, e.Gender), PhoneNumber: first(b.PhoneNumber1, b.PhoneNumber, e.PhoneNumber1, e.PhoneNumber), PhotoBase64: first(b.Image, e.Image), MatchScore: 100, Raw: res.Body}
	if parsed, ok := parseDate(first(b.DateOfBirth, e.DateOfBirth)); ok {
		result.DateOfBirth = &parsed
	}
	return result, nil
}
func (p *Provider) VerifyNIN(context.Context, string) (*providers.IdentityVerification, error) {
	return nil, providers.ErrNotConfigured
}

func (p *Provider) VerifyBVNWithSelfie(ctx context.Context, bvn, selfieBase64 string) (*providers.BVNSelfieVerification, error) {
	liveResponse, err := p.client.Do(ctx, providers.Request{Method: http.MethodPost, Path: "/api/v1/ml/liveness/", Body: map[string]string{"image": selfieBase64}})
	if err != nil {
		return nil, err
	}
	var live struct {
		Entity struct {
			Liveness struct {
				Check       bool    `json:"liveness_check"`
				Probability float64 `json:"liveness_probability"`
			} `json:"liveness"`
			Face struct {
				Detected bool `json:"face_detected"`
				Multi    bool `json:"multiface_detected"`
			} `json:"face"`
		} `json:"entity"`
	}
	if err := providers.DecodeInto(liveResponse, &live); err != nil {
		return nil, err
	}
	livenessPassed := live.Entity.Liveness.Check && live.Entity.Liveness.Probability > 50 && live.Entity.Face.Detected && !live.Entity.Face.Multi
	if !livenessPassed {
		return &providers.BVNSelfieVerification{LivenessScore: live.Entity.Liveness.Probability}, nil
	}
	matchResponse, err := p.client.Do(ctx, providers.Request{Method: http.MethodPost, Path: "/api/v1/kyc/bvn/verify", Body: map[string]string{"bvn": bvn, "selfie_image": selfieBase64}})
	if err != nil {
		return nil, err
	}
	var matched struct {
		Entity struct {
			FirstName   string `json:"first_name"`
			MiddleName  string `json:"middle_name"`
			LastName    string `json:"last_name"`
			DateOfBirth string `json:"date_of_birth"`
			Gender      string `json:"gender"`
			Phone       string `json:"phone_number1"`
			Image       string `json:"image"`
			Selfie      struct {
				Confidence float64 `json:"confidence_value"`
				Match      bool    `json:"match"`
			} `json:"selfie_verification"`
		} `json:"entity"`
	}
	if err := providers.DecodeInto(matchResponse, &matched); err != nil {
		return nil, err
	}
	e := matched.Entity
	identity := &providers.IdentityVerification{Provider: p.Name(), Verified: e.Selfie.Match, FirstName: e.FirstName, MiddleName: e.MiddleName, LastName: e.LastName, Gender: e.Gender, PhoneNumber: e.Phone, MatchScore: int(e.Selfie.Confidence), Raw: matchResponse.Body}
	if parsed, ok := parseDate(e.DateOfBirth); ok {
		identity.DateOfBirth = &parsed
	}
	return &providers.BVNSelfieVerification{Identity: identity, LivenessPassed: true, LivenessScore: live.Entity.Liveness.Probability, FaceMatched: e.Selfie.Match && e.Selfie.Confidence >= 90, FaceMatchScore: e.Selfie.Confidence}, nil
}
func first(v ...string) string {
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}
func parseDate(v string) (time.Time, bool) {
	for _, l := range []string{"2006-01-02", "02-01-2006", "02/01/2006"} {
		if t, e := time.Parse(l, strings.TrimSpace(v)); e == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
