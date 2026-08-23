package controllers

import (
	"context"
	"fmt"
	"html"
	"time"

	"nabla/identity-svc/internal/providers"
)

type WaitlistDelivery interface {
	SendConfirmation(ctx context.Context, email, username string, expiresAt time.Time) error
}

type emailWaitlistDelivery struct {
	email providers.EmailProvider
}

func NewEmailWaitlistDelivery(email providers.EmailProvider) WaitlistDelivery {
	return &emailWaitlistDelivery{email: email}
}

func (d *emailWaitlistDelivery) SendConfirmation(ctx context.Context, email, username string, expiresAt time.Time) error {
	if d.email == nil {
		return providers.ErrNotConfigured
	}
	validUntil := expiresAt.Format("January 2, 2006")
	safeUsername := html.EscapeString(username)
	text := fmt.Sprintf(
		"You're on the Nablr waitlist!\n\nThanks for joining. We'll email you when your early access spot opens up.\n\n@%s is reserved for you until %s. If you haven't onboarded by then, it will be released.\n\nYou're receiving this because you joined the Nablr waitlist. If this wasn't you, you can safely ignore this email.",
		username, validUntil,
	)
	htmlBody := fmt.Sprintf(
		`<div style="font-family:sans-serif;max-width:480px;margin:0 auto;padding:24px;background:#f4f4f4;border-radius:16px">`+
			`<p style="text-align:center;margin:0 0 24px;font-family:'Arial Black',Arial,sans-serif;font-weight:900;font-size:32px;letter-spacing:-1px;color:#9FE870">Nablr</p>`+
			`<div style="position:relative;width:130px;height:80px;margin:0 auto 4px">`+
			`<span style="position:absolute;top:2px;left:22px;width:7px;height:7px;border-radius:50%%;background:#F0492B"></span>`+
			`<span style="position:absolute;top:6px;left:38px;font-family:Georgia,'Times New Roman',serif;font-size:46px;font-weight:700;line-height:1;color:#8CE196;text-shadow:3px 3px 0 #0B6623">@</span>`+
			`<span style="position:absolute;top:10px;right:6px;font-size:22px;line-height:1">🌙</span>`+
			`<span style="position:absolute;bottom:6px;left:4px;width:10px;height:10px;background:#34D399;border-radius:3px;transform:rotate(20deg)"></span>`+
			`<span style="position:absolute;bottom:14px;right:14px;width:18px;height:6px;background:#5EEAD4;border-radius:3px;transform:rotate(-12deg)"></span>`+
			`</div>`+
			`<h2 style="text-align:center">You're on the list!</h2>`+
			`<p style="text-align:center;color:#444">Thanks for joining the Nablr waitlist. We'll email you when your early access spot opens up.</p>`+
			`<p style="text-align:center"><span style="display:inline-block;background:#e6f7ec;color:#1a7f37;padding:8px 16px;border-radius:999px;font-weight:600">@%s is reserved</span></p>`+
			`<p style="text-align:center;color:#666;font-size:13px">This hold is valid through %s. If you haven't completed onboarding by then, the username will be released.</p>`+
			`<hr style="border:none;border-top:1px solid #ddd;margin:24px 0">`+
			`<p style="text-align:center;color:#888;font-size:12px">You're receiving this because you joined the Nablr waitlist. If this wasn't you, you can safely ignore this email.</p>`+
			`</div>`,
		safeUsername, validUntil,
	)
	_, err := d.email.SendEmail(ctx, providers.EmailMessage{
		To:      email,
		Subject: "You're on the Nablr waitlist \U0001F389",
		Text:    text,
		HTML:    htmlBody,
	})
	return err
}
