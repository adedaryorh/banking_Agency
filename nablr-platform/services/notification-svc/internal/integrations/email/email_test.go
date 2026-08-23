package email

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nabla/notification-svc/internal/providers"
)

func TestMailgunPrimary(t *testing.T) {
	mailgun := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/mg.example.com/messages" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		username, password, ok := r.BasicAuth()
		if !ok || username != "api" || password != "mailgun-key" {
			t.Fatal("invalid Mailgun authentication")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil || r.FormValue("to") != "user@example.com" || r.FormValue("subject") != "Verify" {
			t.Fatalf("invalid form: %v %v", r.Form, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"mailgun-id","message":"Queued"}`))
	}))
	defer mailgun.Close()

	provider, err := New(testConfig(mailgun.URL, "http://unused"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.SendEmail(context.Background(), providers.EmailMessage{To: "user@example.com", Subject: "Verify", Text: "code"})
	if err != nil || result.Provider != "mailgun" || result.Reference != "mailgun-id" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSendGridFailover(t *testing.T) {
	mailgun := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary", http.StatusServiceUnavailable)
	}))
	defer mailgun.Close()
	sendgrid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/mail/send" || r.Header.Get("Authorization") != "Bearer sendgrid-key" {
			t.Fatalf("unexpected SendGrid request")
		}
		w.Header().Set("X-Message-Id", "sendgrid-id")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer sendgrid.Close()

	provider, err := New(testConfig(mailgun.URL, sendgrid.URL))
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.SendEmail(context.Background(), providers.EmailMessage{To: "user@example.com", Subject: "Verify", Text: "code"})
	if err != nil || result.Provider != "sendgrid" || result.Reference != "sendgrid-id" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if provider.Name() != "mailgun,sendgrid" {
		t.Fatalf("order = %q", provider.Name())
	}
}

func TestLogProviderRejectedOutsideDevelopment(t *testing.T) {
	_, err := New(Config{Providers: "log", Environment: "production"})
	if err == nil || !strings.Contains(err.Error(), "only allowed") {
		t.Fatalf("expected production rejection, got %v", err)
	}
}

func TestSMTPConfiguration(t *testing.T) {
	provider, err := New(Config{
		Providers:    "smtp",
		Environment:  "test",
		Timeout:      time.Second,
		FromAddress:  "noreply@mailer.nabla.ng",
		FromName:     "Nabla",
		SMTPHost:     "smtp.example.com",
		SMTPPort:     "587",
		SMTPUsername: "noreply@nablr.com",
		SMTPPassword: "test-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "smtp" {
		t.Fatalf("provider=%q", provider.Name())
	}
}

func TestSMTPRequiresCredentials(t *testing.T) {
	_, err := New(Config{Providers: "smtp", FromAddress: "noreply@mailer.nabla.ng"})
	if err == nil || !strings.Contains(err.Error(), "SMTP_PASSWORD") {
		t.Fatalf("expected credentials error, got %v", err)
	}
}

func TestBuildSMTPMessage(t *testing.T) {
	raw, messageID, err := buildSMTPMessage("Nabla", "noreply@mailer.nabla.ng", "user@example.com", providers.EmailMessage{Subject: "Verify your account", Text: "Code: 123456", HTML: "<strong>Code: 123456</strong>"})
	if err != nil {
		t.Fatal(err)
	}
	value := string(raw)
	for _, expected := range []string{"noreply@mailer.nabla.ng", "To: user@example.com", "Subject: Verify your account", "multipart/alternative", "Code: 123456"} {
		if !strings.Contains(value, expected) {
			t.Fatalf("message missing %q:\n%s", expected, value)
		}
	}
	if !strings.HasSuffix(messageID, "@mailer.nabla.ng") {
		t.Fatalf("message id=%q", messageID)
	}
}

func testConfig(mailgunURL, sendgridURL string) Config {
	return Config{Providers: "mailgun,sendgrid", Environment: "test", Timeout: time.Second, FromAddress: "noreply@example.com", FromName: "Nabla", MailgunBaseURL: mailgunURL, MailgunDomain: "mg.example.com", MailgunAPIKey: "mailgun-key", SendGridBaseURL: sendgridURL, SendGridAPIKey: "sendgrid-key"}
}
