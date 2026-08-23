package email

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"nabla/notification-svc/internal/providers"
)

type smtpProvider struct {
	host, port, username, password string
	fromAddress, fromName          string
	timeout                        time.Duration
	name                           string
}

func newSMTP(cfg Config, providerName string) (*smtpProvider, error) {
	host := strings.TrimSpace(cfg.SMTPHost)
	if host == "" {
		host = "smtp.resend.com"
	}
	port := strings.TrimSpace(cfg.SMTPPort)
	if port == "" {
		port = "587"
	}
	username := strings.TrimSpace(cfg.SMTPUsername)
	if username == "" {
		username = "resend"
	}
	if strings.TrimSpace(providerName) == "" {
		providerName = "smtp"
	}
	if strings.TrimSpace(cfg.SMTPPassword) == "" || strings.TrimSpace(cfg.FromAddress) == "" {
		return nil, errors.New("SMTP_PASSWORD and EMAIL_FROM_ADDRESS are required")
	}
	if _, err := mail.ParseAddress(cfg.FromAddress); err != nil {
		return nil, errors.New("EMAIL_FROM_ADDRESS is invalid")
	}
	return &smtpProvider{host: host, port: port, username: username, password: cfg.SMTPPassword, fromAddress: cfg.FromAddress, fromName: cfg.FromName, timeout: cfg.Timeout, name: providerName}, nil
}

func (p *smtpProvider) Name() string { return p.name }

func (p *smtpProvider) SendEmail(ctx context.Context, message providers.EmailMessage) (*providers.EmailResult, error) {
	if err := validate(message); err != nil {
		return nil, err
	}
	recipient, _ := mail.ParseAddress(message.To)
	raw, messageID, err := buildSMTPMessage(p.fromName, p.fromAddress, recipient.Address, message)
	if err != nil {
		return nil, err
	}

	dialer := net.Dialer{Timeout: p.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(p.host, p.port))
	if err != nil {
		return nil, fmt.Errorf("connect to SMTP: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(p.timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = conn.SetDeadline(deadline)

	client, err := smtp.NewClient(conn, p.host)
	if err != nil {
		return nil, fmt.Errorf("start SMTP session: %w", err)
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return nil, errors.New("SMTP server did not advertise STARTTLS")
	}
	if err = client.StartTLS(&tls.Config{ServerName: p.host, MinVersion: tls.VersionTLS12}); err != nil {
		return nil, fmt.Errorf("start SMTP TLS: %w", err)
	}
	if err = client.Auth(smtp.PlainAuth("", p.username, p.password, p.host)); err != nil {
		return nil, fmt.Errorf("authenticate with SMTP: %w", err)
	}
	if err = client.Mail(p.fromAddress); err != nil {
		return nil, fmt.Errorf("set SMTP sender: %w", err)
	}
	if err = client.Rcpt(recipient.Address); err != nil {
		return nil, fmt.Errorf("set SMTP recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return nil, fmt.Errorf("open SMTP message: %w", err)
	}
	if _, err = writer.Write(raw); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("write SMTP message: %w", err)
	}
	if err = writer.Close(); err != nil {
		return nil, fmt.Errorf("submit SMTP message: %w", err)
	}
	if err = client.Quit(); err != nil {
		return nil, fmt.Errorf("finish SMTP session: %w", err)
	}
	return &providers.EmailResult{Reference: messageID, Provider: p.Name()}, nil
}

func buildSMTPMessage(fromName, fromAddress, recipient string, message providers.EmailMessage) ([]byte, string, error) {
	if strings.ContainsAny(message.Subject, "\r\n") {
		return nil, "", errors.New("email subject contains invalid characters")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, "", err
	}
	messageID := hex.EncodeToString(random) + "@" + strings.SplitN(fromAddress, "@", 2)[1]
	var body bytes.Buffer
	fmt.Fprintf(&body, "From: %s\r\n", formatFrom(fromName, fromAddress))
	fmt.Fprintf(&body, "To: %s\r\n", recipient)
	fmt.Fprintf(&body, "Subject: %s\r\n", message.Subject)
	fmt.Fprintf(&body, "Message-ID: <%s>\r\n", messageID)
	fmt.Fprintf(&body, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	body.WriteString("MIME-Version: 1.0\r\n")

	if message.Text != "" && message.HTML != "" {
		multipartWriter := multipart.NewWriter(&body)
		body.WriteString("Content-Type: multipart/alternative; boundary=\"" + multipartWriter.Boundary() + "\"\r\n\r\n")
		if err := writeMIMEPart(multipartWriter, "text/plain; charset=UTF-8", message.Text); err != nil {
			return nil, "", err
		}
		if err := writeMIMEPart(multipartWriter, "text/html; charset=UTF-8", message.HTML); err != nil {
			return nil, "", err
		}
		if err := multipartWriter.Close(); err != nil {
			return nil, "", err
		}
	} else {
		contentType, content := "text/plain; charset=UTF-8", message.Text
		if message.HTML != "" {
			contentType, content = "text/html; charset=UTF-8", message.HTML
		}
		body.WriteString("Content-Type: " + contentType + "\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		writer := quotedprintable.NewWriter(&body)
		_, _ = writer.Write([]byte(content))
		_ = writer.Close()
	}
	return body.Bytes(), messageID, nil
}

func writeMIMEPart(writer *multipart.Writer, contentType, content string) error {
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", contentType)
	header.Set("Content-Transfer-Encoding", "quoted-printable")
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	encoded := quotedprintable.NewWriter(part)
	if _, err = encoded.Write([]byte(content)); err != nil {
		return err
	}
	return encoded.Close()
}
