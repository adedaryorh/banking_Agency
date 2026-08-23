package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
	headers map[string]string
}

const maxResponseBytes = 4 << 20

func NewClient(baseURL string, timeout time.Duration, headers map[string]string) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
		headers: headers,
	}
}

type Request struct {
	Method  string
	Path    string
	Query   url.Values
	Body    any
	RawBody io.Reader
	Headers map[string]string
}

type Response struct {
	Status int
	Body   []byte
}

func (c *Client) Do(ctx context.Context, request Request) (*Response, error) {
	var body io.Reader
	if request.RawBody != nil {
		body = request.RawBody
	} else if request.Body != nil {
		raw, err := json.Marshal(request.Body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(raw)
	}

	endpoint := c.baseURL + request.Path
	if len(request.Query) > 0 {
		endpoint += "?" + request.Query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, request.Method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if request.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}
	for key, value := range request.Headers {
		req.Header.Set(key, value)
	}

	res, err := c.http.Do(req)
	if err != nil {
		// A timeout on a request that may have already been accepted by the
		// provider is indeterminate, not a failure. Callers in the money path
		// must requery rather than assume nothing happened.
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return nil, fmt.Errorf("%w: %v", ErrIndeterminate, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: read body: %v", ErrIndeterminate, err)
	}

	response := &Response{Status: res.StatusCode, Body: raw}

	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return response, nil
	case res.StatusCode == http.StatusNotFound:
		return response, fmt.Errorf("%w: status %d", ErrNotFound, res.StatusCode)
	case res.StatusCode == http.StatusTooManyRequests, res.StatusCode >= 500:
		return response, fmt.Errorf("%w: status %d: %s", ErrUnavailable, res.StatusCode, snippet(raw))
	default:
		return response, fmt.Errorf("%w: status %d: %s", ErrRejected, res.StatusCode, snippet(raw))
	}
}

// DecodeInto unmarshals a response body, wrapping the failure so a vendor that
// changed its response shape is obvious in the logs.
func DecodeInto(response *Response, target any) error {
	if response == nil {
		return ErrUnavailable
	}
	if err := json.Unmarshal(response.Body, target); err != nil {
		return fmt.Errorf("%w: decode response: %v", ErrUnavailable, err)
	}
	return nil
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

func snippet(raw []byte) string {
	const max = 300
	if len(raw) <= max {
		return string(raw)
	}
	return string(raw[:max]) + "..."
}
