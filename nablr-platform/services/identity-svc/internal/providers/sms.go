package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type SMSConfig struct {
	Provider, BaseURL, SenderID        string
	TermiiAPIKey                       string
	TwilioSID, TwilioToken, TwilioFrom string
}

func NewSMSProvider(cfg SMSConfig) (SMSProvider, error) {
	switch strings.ToLower(cfg.Provider) {
	case "termii":
		if cfg.TermiiAPIKey == "" {
			return nil, errors.New("termii api key is required")
		}
		return &termiiSMS{client: NewClient(cfg.BaseURL, 15*time.Second, nil), apiKey: cfg.TermiiAPIKey, sender: cfg.SenderID}, nil
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
	to := strings.TrimPrefix(m.To, "+")
	res, err := p.client.Do(ctx, Request{Method: http.MethodPost, Path: "/api/sms/send", Body: map[string]any{
		"to": to, "from": sender, "sms": m.Body, "type": "plain", "channel": "generic", "api_key": p.apiKey,
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
	return &SMSResult{Reference: fmt.Sprintf("log-%d", time.Now().UnixNano()), Provider: "log"}, nil
}
