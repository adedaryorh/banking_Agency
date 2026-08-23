package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"nabla/notification-svc/internal/providers"
)

type Config struct {
	Providers   string
	Environment string
	Timeout     time.Duration
	FromAddress string
	FromName    string

	MailgunBaseURL string
	MailgunDomain  string
	MailgunAPIKey  string

	SendGridBaseURL string
	SendGridAPIKey  string

	SMTPHost     string
	SMTPPort     string
	SMTPUsername string
	SMTPPassword string
}

func New(cfg Config) (providers.EmailProvider, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Providers == "" {
		cfg.Providers = "mailgun,sendgrid"
	}
	values := make([]providers.EmailProvider, 0, 2)
	seen := map[string]bool{}
	for _, raw := range strings.Split(cfg.Providers, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if seen[name] {
			return nil, fmt.Errorf("EMAIL_PROVIDERS contains duplicate provider %q", name)
		}
		seen[name] = true
		switch name {
		case "mailgun":
			provider, err := newMailgun(cfg)
			if err != nil {
				return nil, err
			}
			values = append(values, provider)
		case "sendgrid":
			provider, err := newSendGrid(cfg)
			if err != nil {
				return nil, err
			}
			values = append(values, provider)
		case "smtp", "resend":
			provider, err := newSMTP(cfg, name)
			if err != nil {
				return nil, err
			}
			values = append(values, provider)
		case "log":
			if !isDevelopment(cfg.Environment) {
				return nil, errors.New("EMAIL_PROVIDERS=log is only allowed in local, development, or test")
			}
			values = append(values, logProvider{})
		default:
			return nil, fmt.Errorf("unsupported email provider %q", name)
		}
	}
	if len(values) == 0 {
		return nil, errors.New("EMAIL_PROVIDERS must contain smtp, resend, mailgun, sendgrid, or development-only log")
	}
	return &failover{providers: values}, nil
}

type failover struct{ providers []providers.EmailProvider }

func (f *failover) Name() string {
	names := make([]string, 0, len(f.providers))
	for _, provider := range f.providers {
		names = append(names, provider.Name())
	}
	return strings.Join(names, ",")
}

func (f *failover) SendEmail(ctx context.Context, message providers.EmailMessage) (*providers.EmailResult, error) {
	if err := validate(message); err != nil {
		return nil, err
	}
	var last error
	for _, provider := range f.providers {
		result, err := provider.SendEmail(ctx, message)
		if err == nil {
			return result, nil
		}
		last = fmt.Errorf("%s: %w", provider.Name(), err)
	}
	return nil, last
}

type mailgunProvider struct {
	cfg    Config
	client *http.Client
}

func newMailgun(cfg Config) (*mailgunProvider, error) {
	if cfg.MailgunDomain == "" || cfg.MailgunAPIKey == "" || cfg.FromAddress == "" {
		return nil, errors.New("MAILGUN_DOMAIN, MAILGUN_API_KEY, and EMAIL_FROM_ADDRESS are required")
	}
	if cfg.MailgunBaseURL == "" {
		cfg.MailgunBaseURL = "https://api.mailgun.net"
	}
	return &mailgunProvider{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}, nil
}

func (*mailgunProvider) Name() string { return "mailgun" }

func (p *mailgunProvider) SendEmail(ctx context.Context, message providers.EmailMessage) (*providers.EmailResult, error) {
	var payloadBody bytes.Buffer
	form := multipart.NewWriter(&payloadBody)
	for key, value := range map[string]string{"from": formatFrom(p.cfg.FromName, p.cfg.FromAddress), "to": message.To, "subject": message.Subject, "text": message.Text} {
		if err := form.WriteField(key, value); err != nil {
			return nil, err
		}
	}
	if message.HTML != "" {
		if err := form.WriteField("html", message.HTML); err != nil {
			return nil, err
		}
	}
	if err := form.Close(); err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(p.cfg.MailgunBaseURL, "/") + "/v3/" + p.cfg.MailgunDomain + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &payloadBody)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth("api", p.cfg.MailgunAPIKey)
	req.Header.Set("Content-Type", form.FormDataContentType())
	response, err := p.client.Do(req)
	if err != nil {
		return nil, classifyHTTPError(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, classifyStatus(response.StatusCode, fmt.Sprintf("mailgun returned %d: %s", response.StatusCode, strings.TrimSpace(string(body))))
	}
	var decoded struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &decoded)
	return &providers.EmailResult{Reference: decoded.ID, Provider: p.Name()}, nil
}

type sendGridProvider struct {
	cfg    Config
	client *http.Client
}

func newSendGrid(cfg Config) (*sendGridProvider, error) {
	if cfg.SendGridAPIKey == "" || cfg.FromAddress == "" {
		return nil, errors.New("SENDGRID_API_KEY and EMAIL_FROM_ADDRESS are required")
	}
	if cfg.SendGridBaseURL == "" {
		cfg.SendGridBaseURL = "https://api.sendgrid.com"
	}
	return &sendGridProvider{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}, nil
}

func (*sendGridProvider) Name() string { return "sendgrid" }

func (p *sendGridProvider) SendEmail(ctx context.Context, message providers.EmailMessage) (*providers.EmailResult, error) {
	content := []map[string]string{{"type": "text/plain", "value": message.Text}}
	if message.HTML != "" {
		content = append(content, map[string]string{"type": "text/html", "value": message.HTML})
	}
	payload := map[string]any{"personalizations": []map[string]any{{"to": []map[string]string{{"email": message.To}}, "subject": message.Subject}}, "from": map[string]string{"email": p.cfg.FromAddress, "name": p.cfg.FromName}, "content": content}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.cfg.SendGridBaseURL, "/")+"/v3/mail/send", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.SendGridAPIKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(req)
	if err != nil {
		return nil, classifyHTTPError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, classifyStatus(response.StatusCode, fmt.Sprintf("sendgrid returned %d: %s", response.StatusCode, strings.TrimSpace(string(limited))))
	}
	return &providers.EmailResult{Reference: response.Header.Get("X-Message-Id"), Provider: p.Name()}, nil
}

type logProvider struct{}

func (logProvider) Name() string { return "log" }
func (logProvider) SendEmail(_ context.Context, message providers.EmailMessage) (*providers.EmailResult, error) {
	log.Printf("development email to=%q subject=%q", message.To, message.Subject)
	return &providers.EmailResult{Reference: fmt.Sprintf("log-%d", time.Now().UnixNano()), Provider: "log"}, nil
}

func validate(message providers.EmailMessage) error {
	if _, err := mail.ParseAddress(message.To); err != nil {
		return errors.New("email destination is invalid")
	}
	if strings.TrimSpace(message.Subject) == "" || (strings.TrimSpace(message.Text) == "" && strings.TrimSpace(message.HTML) == "") {
		return errors.New("email subject and body are required")
	}
	return nil
}

func formatFrom(name, address string) string {
	return (&mail.Address{Name: name, Address: address}).String()
}

func isDevelopment(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "local", "development", "test":
		return true
	}
	return false
}

// classifyHTTPError maps transport failures onto the same sentinels the rest of
// the service uses. Timeouts are indeterminate: the vendor may already have
// accepted the message. Anything else is a confirmed non-delivery.
func classifyHTTPError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return fmt.Errorf("%w: %v", providers.ErrIndeterminate, err)
	}
	return fmt.Errorf("%w: %v", providers.ErrUnavailable, err)
}

// classifyStatus maps an HTTP status onto the delivery sentinels so retries and
// metrics agree with the SMS path: 429/5xx is a retryable outage, other 4xx is
// a permanent rejection.
func classifyStatus(status int, detail string) error {
	if status == http.StatusTooManyRequests || status >= http.StatusInternalServerError {
		return fmt.Errorf("%w: %s", providers.ErrUnavailable, detail)
	}
	return fmt.Errorf("%w: %s", providers.ErrRejected, detail)
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}
