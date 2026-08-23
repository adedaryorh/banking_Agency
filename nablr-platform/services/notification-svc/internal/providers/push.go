package providers

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type PushConfig struct{ Provider, Environment, ConfigJSON, CredentialsBase64, CredentialsPath string }

type firebaseCredentials struct {
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

func NewPushProvider(_ context.Context, cfg PushConfig) (PushProvider, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "firebase":
		var raw []byte
		var err error
		if strings.TrimSpace(cfg.ConfigJSON) != "" {
			raw = []byte(strings.TrimSpace(cfg.ConfigJSON))
		} else if cfg.CredentialsBase64 != "" {
			raw, err = base64.StdEncoding.DecodeString(cfg.CredentialsBase64)
		} else if cfg.CredentialsPath != "" {
			raw, err = os.ReadFile(cfg.CredentialsPath)
		} else {
			return nil, errors.New("FIREBASE_CONFIG_JSON, FIREBASE_CREDENTIALS_BASE64, or FIREBASE_CREDENTIALS_PATH is required")
		}
		if err != nil {
			return nil, fmt.Errorf("load Firebase credentials: %w", err)
		}
		var credentials firebaseCredentials
		if err = json.Unmarshal(raw, &credentials); err != nil {
			return nil, fmt.Errorf("parse Firebase credentials: %w", err)
		}
		block, _ := pem.Decode([]byte(credentials.PrivateKey))
		if block == nil {
			return nil, errors.New("Firebase private key is invalid")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse Firebase private key: %w", err)
		}
		privateKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("Firebase private key must be RSA")
		}
		if credentials.ProjectID == "" || credentials.ClientEmail == "" {
			return nil, errors.New("Firebase credentials require project_id and client_email")
		}
		if credentials.TokenURI == "" {
			credentials.TokenURI = "https://oauth2.googleapis.com/token"
		}
		return &firebasePush{credentials: credentials, privateKey: privateKey, client: &http.Client{Timeout: 30 * time.Second}}, nil
	case "log":
		if !isDevelopment(cfg.Environment) {
			return nil, errors.New("PUSH_PROVIDER=log is only allowed in local, development, or test")
		}
		return logPush{}, nil
	default:
		return nil, errors.New("PUSH_PROVIDER must be firebase or log")
	}
}

type firebasePush struct {
	credentials firebaseCredentials
	privateKey  *rsa.PrivateKey
	client      *http.Client
	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

func (*firebasePush) Name() string { return "firebase" }
func (f *firebasePush) SendPush(ctx context.Context, message PushMessage) (*PushResult, error) {
	if strings.TrimSpace(message.DeviceToken) == "" {
		return nil, errors.New("push device token is required")
	}
	token, err := f.token(ctx)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{"message": map[string]any{"token": message.DeviceToken, "notification": map[string]string{"title": message.Title, "body": message.Body}, "data": message.Data}})
	endpoint := "https://fcm.googleapis.com/v1/projects/" + url.PathEscape(f.credentials.ProjectID) + "/messages:send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return nil, fmt.Errorf("%w: %v", ErrIndeterminate, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := fmt.Sprintf("firebase returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
			return nil, fmt.Errorf("%w: %s", ErrUnavailable, detail)
		}
		return nil, fmt.Errorf("%w: %s", ErrRejected, detail)
	}
	var result struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(body, &result)
	return &PushResult{Reference: result.Name, Provider: f.Name()}, nil
}
func (f *firebasePush) token(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.accessToken != "" && time.Now().Before(f.tokenExpiry.Add(-time.Minute)) {
		return f.accessToken, nil
	}
	now := time.Now().Unix()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iss": f.credentials.ClientEmail, "scope": "https://www.googleapis.com/auth/firebase.messaging", "aud": f.credentials.TokenURI, "iat": now, "exp": now + 3600})
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, f.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	assertion := unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, f.credentials.TokenURI, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Firebase OAuth returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err = json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	if result.AccessToken == "" {
		return "", errors.New("Firebase OAuth returned no access token")
	}
	f.accessToken = result.AccessToken
	f.tokenExpiry = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	return f.accessToken, nil
}

type logPush struct{}

func (logPush) Name() string { return "log" }
func (logPush) SendPush(_ context.Context, message PushMessage) (*PushResult, error) {
	if strings.TrimSpace(message.DeviceToken) == "" {
		return nil, errors.New("push device token is required")
	}
	log.Printf("development push token=%q title=%q body=%q", message.DeviceToken, message.Title, message.Body)
	return &PushResult{Reference: fmt.Sprintf("log-%d", time.Now().UnixNano()), Provider: "log"}, nil
}
