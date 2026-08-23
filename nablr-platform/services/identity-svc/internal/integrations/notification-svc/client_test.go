package notification

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nabla/identity-svc/internal/providers"
)

func TestSendSMSContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/internal/v1/sms" {
			t.Errorf("path=%s", request.URL.Path)
		}
		if request.Header.Get("X-Internal-Service-Token") != "secret" {
			t.Error("missing internal service token")
		}
		var message providers.SMSMessage
		if err := json.NewDecoder(request.Body).Decode(&message); err != nil {
			t.Fatal(err)
		}
		if message.To != "+2348012345678" || message.Body != "123456" {
			t.Errorf("message=%+v", message)
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(providers.SMSResult{Reference: "sms-1", Provider: "log"})
	}))
	defer server.Close()

	client, err := New(server.URL, "secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.SendSMS(context.Background(), providers.SMSMessage{To: "+2348012345678", Body: "123456"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reference != "sms-1" || result.Provider != "log" {
		t.Fatalf("result=%+v", result)
	}
}
