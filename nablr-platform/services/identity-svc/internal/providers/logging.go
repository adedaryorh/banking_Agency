package providers

import (
	"context"
	"fmt"
	"log"
	"time"
)

type LoggingEmailProvider struct{}

func NewLoggingEmailProvider() LoggingEmailProvider { return LoggingEmailProvider{} }
func (LoggingEmailProvider) Name() string           { return "log" }
func (LoggingEmailProvider) SendEmail(_ context.Context, message EmailMessage) (*EmailResult, error) {
	reference := fmt.Sprintf("log-%d", time.Now().UnixNano())
	log.Printf("[email] SENT provider=log to=%s subject=%q body=%q reference=%s", message.To, message.Subject, message.Text, reference)
	return &EmailResult{Reference: reference, Provider: "log"}, nil
}

type LoggingSMSProvider struct {
	next SMSProvider
}

func NewLoggingSMSProvider(next SMSProvider) *LoggingSMSProvider {
	return &LoggingSMSProvider{next: next}
}

func (p *LoggingSMSProvider) Name() string { return p.next.Name() }

func (p *LoggingSMSProvider) SendSMS(ctx context.Context, m SMSMessage) (*SMSResult, error) {
	start := time.Now()
	result, err := p.next.SendSMS(ctx, m)
	elapsed := time.Since(start)

	if err != nil {
		log.Printf("[sms] FAILED provider=%s to=%s body=%q elapsed=%s error=%v", p.next.Name(), m.To, m.Body, elapsed, err)
		return nil, err
	}

	log.Printf("[sms] SENT provider=%s to=%s body=%q reference=%s elapsed=%s", result.Provider, m.To, m.Body, result.Reference, elapsed)
	return result, nil
}
