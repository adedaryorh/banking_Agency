package controllers

import (
	"context"
	"html"
	"net/url"
	"strings"

	"nabla/identity-svc/internal/providers"
)

type TokenDelivery interface {
	SendVerification(context.Context, string, string) error
	SendPasswordReset(context.Context, string, string) error
	SendMagicLink(context.Context, string, string) error
}

type emailTokenDelivery struct {
	email       providers.EmailProvider
	frontendURL string
}

func (d *emailTokenDelivery) SendMagicLink(ctx context.Context, email, rawToken string) error {
	if d.email == nil {
		return providers.ErrNotConfigured
	}
	link := strings.TrimRight(d.frontendURL, "/") + "/auth/magic-link?" + url.Values{"token": {rawToken}}.Encode()
	safeLink := html.EscapeString(link)
	_, err := d.email.SendEmail(ctx, providers.EmailMessage{
		To: email, Subject: "Sign in to your Nabla account",
		Text: "Use this secure link to sign in. It expires shortly and can only be used once: " + link,
		HTML: "<p>Use this secure, single-use link to sign in:</p><p><a href=\"" + safeLink + "\">Sign in to Nabla</a></p>",
	})
	return err
}

func NewEmailTokenDelivery(email providers.EmailProvider, frontendURL string) TokenDelivery {
	return &emailTokenDelivery{email: email, frontendURL: frontendURL}
}

func (d *emailTokenDelivery) SendVerification(ctx context.Context, email, rawToken string) error {
	if d.email == nil {
		return providers.ErrNotConfigured
	}
	link := strings.TrimRight(d.frontendURL, "/") + "/verify-email?" + url.Values{"token": {rawToken}}.Encode()
	safeLink := html.EscapeString(link)
	_, err := d.email.SendEmail(ctx, providers.EmailMessage{
		To:      email,
		Subject: "Verify your Nabla account",
		Text:    "Verify your email: " + link,
		HTML:    "<p>Verify your email: <a href=\"" + safeLink + "\">" + safeLink + "</a></p>",
	})
	return err
}

func (d *emailTokenDelivery) SendPasswordReset(ctx context.Context, email, rawToken string) error {
	if d.email == nil {
		return providers.ErrNotConfigured
	}
	link := d.frontendURL + "/reset-password?token=" + rawToken
	_, err := d.email.SendEmail(ctx, providers.EmailMessage{
		To:      email,
		Subject: "Reset your Nabla password",
		Text:    "Reset your password: " + link,
		HTML:    "<p>Reset your password: <a href=\"" + link + "\">" + link + "</a></p>",
	})
	return err
}
