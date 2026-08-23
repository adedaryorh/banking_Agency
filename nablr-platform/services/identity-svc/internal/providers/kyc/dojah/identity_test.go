package dojah

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifyBVNMapsLinkedPhone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/kyc/bvn/full" || r.URL.Query().Get("bvn") != "12345678901" {
			t.Errorf("unexpected request %s", r.URL.String())
		}
		if r.Header.Get("AppId") != "app" || r.Header.Get("Authorization") != "secret" {
			t.Error("missing Dojah credentials")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"reference": "ref-1", "entity": map[string]any{"bvn": map[string]any{"first_name": "Ada", "last_name": "Okafor", "phone_number1": "08012345678", "date_of_birth": "1990-01-02"}}})
	}))
	defer server.Close()
	p, err := New(Config{BaseURL: server.URL, AppID: "app", SecretKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.VerifyBVN(context.Background(), "12345678901")
	if err != nil {
		t.Fatal(err)
	}
	if got.PhoneNumber != "08012345678" || got.FirstName != "Ada" || got.Reference != "ref-1" {
		t.Fatalf("unexpected result %#v", got)
	}
}

func TestVerifyBVNWithSelfieRequiresLivenessAndMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/ml/liveness/":
			_ = json.NewEncoder(w).Encode(map[string]any{"entity": map[string]any{"liveness": map[string]any{"liveness_check": true, "liveness_probability": 96.2}, "face": map[string]any{"face_detected": true, "multiface_detected": false}}})
		case "/api/v1/kyc/bvn/verify":
			_ = json.NewEncoder(w).Encode(map[string]any{"entity": map[string]any{"first_name": "Ada", "last_name": "Okafor", "selfie_verification": map[string]any{"confidence_value": 98.1, "match": true}}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	p, err := New(Config{BaseURL: server.URL, AppID: "app", SecretKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.VerifyBVNWithSelfie(context.Background(), "12345678901", "base64-image")
	if err != nil {
		t.Fatal(err)
	}
	if !got.LivenessPassed || !got.FaceMatched || got.LivenessScore != 96.2 || got.FaceMatchScore != 98.1 {
		t.Fatalf("unexpected result %#v", got)
	}
}
