package providers

import "context"

type SMSMessage struct {
	To     string `json:"to"`
	Body   string `json:"body"`
	Sender string `json:"sender,omitempty"`
}

type SMSResult struct {
	Reference string `json:"reference"`
	Provider  string `json:"provider"`
}

type SMSProvider interface {
	Name() string
	SendSMS(ctx context.Context, message SMSMessage) (*SMSResult, error)
}

type EmailMessage struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	HTML    string `json:"html,omitempty"`
	Text    string `json:"text"`
}

type EmailResult struct {
	Reference string `json:"reference"`
	Provider  string `json:"provider"`
}

type EmailProvider interface {
	Name() string
	SendEmail(ctx context.Context, message EmailMessage) (*EmailResult, error)
}

type PushMessage struct {
	DeviceToken string            `json:"device_token"`
	Title       string            `json:"title"`
	Body        string            `json:"body"`
	Data        map[string]string `json:"data,omitempty"`
}

type PushResult struct {
	Reference string `json:"reference"`
	Provider  string `json:"provider"`
}

type PushProvider interface {
	Name() string
	SendPush(ctx context.Context, message PushMessage) (*PushResult, error)
}
