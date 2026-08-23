package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type SMSConfig struct {
	Provider, BaseURL, SenderID        string
	TermiiAPIKey                       string
	DigitalPulseAPIKey                 string
	DigitalPulseRoute                  string
	TwilioSID, TwilioToken, TwilioFrom string
}

func NewSMSProvider(cfg SMSConfig) (SMSProvider, error) {
	switch strings.ToLower(cfg.Provider) {
	case "termii":
		if cfg.TermiiAPIKey == "" {
			return nil, errors.New("termii api key is required")
		}
		return &termiiSMS{client: NewClient(cfg.BaseURL, 15*time.Second, nil), apiKey: cfg.TermiiAPIKey, sender: cfg.SenderID}, nil
	case "digitalpulse":
		if cfg.DigitalPulseAPIKey == "" {
			return nil, errors.New("digitalpulse api key is required")
		}
		route := strings.TrimSpace(cfg.DigitalPulseRoute)
		if route == "" {
			route = "anq"
		}
		return &digitalPulseSMS{client: NewClient(cfg.BaseURL, 15*time.Second, map[string]string{"api-key": cfg.DigitalPulseAPIKey}), route: route, sender: cfg.SenderID}, nil
	case "twilio":
		if cfg.TwilioSID == "" || cfg.TwilioToken == "" || cfg.TwilioFrom == "" {
			return nil, errors.New("twilio credentials and from number are required")
		}
		return &twilioSMS{client: NewClient(cfg.BaseURL, 15*time.Second, nil), sid: cfg.TwilioSID, token: cfg.TwilioToken, from: cfg.TwilioFrom}, nil
	case "log":
		return logSMS{}, nil
	default:
		return nil, fmt.Errorf("unsupported sms provider %q", cfg.Provider)
	}
}

type digitalPulseSMS struct {
	client        *Client
	route, sender string
}

func (p *digitalPulseSMS) Name() string { return "digitalpulse" }
func (p *digitalPulseSMS) SendSMS(ctx context.Context, m SMSMessage) (*SMSResult, error) {
	sender := strings.TrimSpace(m.Sender)
	if sender == "" {
		sender = p.sender
	}
	if sender == "" {
		return nil, errors.New("SMS_SENDER_ID is required for digitalpulse")
	}
	res, err := p.client.Do(ctx, Request{Method: http.MethodPost, Path: "/1.0/send-sms/" + url.PathEscape(p.route), Body: map[string]any{
		"sender":   sender,
		"message":  m.Body,
		"receiver": m.To,
	}})
	if err != nil {
		return nil, err
	}
	var body struct {
		ID        string `json:"id"`
		MessageID string `json:"message_id"`
		Reference string `json:"reference"`
		Status    string `json:"status"`
	}
	_ = json.Unmarshal(res.Body, &body)
	reference := firstNonEmpty(body.MessageID, body.ID, body.Reference, fmt.Sprintf("digitalpulse-%d", time.Now().UnixNano()))
	return &SMSResult{Reference: reference, Provider: p.Name()}, nil
}

type termiiSMS struct {
	client         *Client
	apiKey, sender string
}

func (p *termiiSMS) Name() string { return "termii" }
func (p *termiiSMS) SendSMS(ctx context.Context, m SMSMessage) (*SMSResult, error) {
	sender := m.Sender
	if sender == "" {
		sender = p.sender
	}
	res, err := p.client.Do(ctx, Request{Method: http.MethodPost, Path: "/api/sms/send", Body: map[string]any{
		"to": m.To, "from": sender, "sms": m.Body, "type": "plain", "channel": "generic", "api_key": p.apiKey,
	}})
	if err != nil {
		return nil, err
	}
	var body struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(res.Body, &body); err != nil {
		return nil, fmt.Errorf("decode termii response: %w", err)
	}
	if body.MessageID == "" {
		return nil, errors.New("termii response missing message_id")
	}
	return &SMSResult{Reference: body.MessageID, Provider: p.Name()}, nil
}

type twilioSMS struct {
	client           *Client
	sid, token, from string
}

func basicAuth(user, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
}
func (p *twilioSMS) Name() string { return "twilio" }
func (p *twilioSMS) SendSMS(ctx context.Context, m SMSMessage) (*SMSResult, error) {
	form := url.Values{"To": {m.To}, "From": {p.from}, "Body": {m.Body}}
	res, err := p.client.Do(ctx, Request{Method: http.MethodPost, Path: "/2010-04-01/Accounts/" + url.PathEscape(p.sid) + "/Messages.json", Body: nil, Headers: map[string]string{
		"Authorization": "Basic " + basicAuth(p.sid, p.token), "Content-Type": "application/x-www-form-urlencoded",
	}, RawBody: strings.NewReader(form.Encode())})
	if err != nil {
		return nil, err
	}
	var body struct {
		SID string `json:"sid"`
	}
	if err := json.Unmarshal(res.Body, &body); err != nil {
		return nil, fmt.Errorf("decode twilio response: %w", err)
	}
	if body.SID == "" {
		return nil, errors.New("twilio response missing sid")
	}
	return &SMSResult{Reference: body.SID, Provider: p.Name()}, nil
}

type logSMS struct{}

func (logSMS) Name() string { return "log" }
func (logSMS) SendSMS(_ context.Context, m SMSMessage) (*SMSResult, error) {
	log.Printf("DEVELOPMENT OTP SMS to=%s body=%q", m.To, m.Body)
	return &SMSResult{Reference: fmt.Sprintf("log-%d", time.Now().UnixNano()), Provider: "log"}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
