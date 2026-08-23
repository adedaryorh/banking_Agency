package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"nabla/identity-svc/internal/models"
	"nabla/identity-svc/internal/providers"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, serviceToken string, timeout time.Duration) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(serviceToken) == "" {
		return nil, errors.New("NOTIFICATION_HTTP_URL and INTERNAL_SERVICE_TOKEN are required")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   serviceToken,
		http:    &http.Client{Timeout: timeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	}, nil
}

func (*Client) Name() string { return "notification-svc" }

func (c *Client) SendEmail(ctx context.Context, message providers.EmailMessage) (*providers.EmailResult, error) {
	var result providers.EmailResult
	if err := c.send(ctx, "/internal/v1/email", message, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) SendSMS(ctx context.Context, message providers.SMSMessage) (*providers.SMSResult, error) {
	var result providers.SMSResult
	if err := c.send(ctx, "/internal/v1/sms", message, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) SendPush(ctx context.Context, message providers.PushMessage) (*providers.PushResult, error) {
	var result providers.PushResult
	if err := c.send(ctx, "/internal/v1/push", message, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) send(ctx context.Context, path string, message, result any) error {
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Token", c.token)
	if requestID := models.MetadataFromContext(ctx).RequestID; requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		limited, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("notification service returned %d: %s", response.StatusCode, strings.TrimSpace(string(limited)))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(result); err != nil {
		return err
	}
	return nil
}

var _ providers.EmailProvider = (*Client)(nil)
var _ providers.SMSProvider = (*Client)(nil)
var _ providers.PushProvider = (*Client)(nil)
