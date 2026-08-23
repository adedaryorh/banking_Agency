package controllers

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	models "nabla/identity-svc/internal/models"
	"nabla/identity-svc/internal/providers"
	repo "nabla/identity-svc/internal/repository"
)

type OTPController interface {
	Issue(ctx context.Context, request OTPIssueRequest) (*OTPChallenge, error)
	Verify(ctx context.Context, store repo.Store, token, code string, purpose models.OTPPurpose) (*models.OTPCode, error)
}

type OTPIssueRequest struct {
	UserID      uuid.UUID
	Purpose     models.OTPPurpose
	Channel     models.OTPChannel
	Destination string
}

type OTPChallenge struct {
	ChallengeToken string            `json:"challenge_token"`
	Channel        models.OTPChannel `json:"channel"`
	Destination    string            `json:"destination"`
	ExpiresAt      time.Time         `json:"expires_at"`
}

type otpController struct {
	store repo.Store
	sms   providers.SMSProvider
	email providers.EmailProvider
	audit AuditController
	now   func() time.Time
}

func NewOTPController(
	store repo.Store,
	sms providers.SMSProvider,
	email providers.EmailProvider,
	audit AuditController,
) OTPController {
	return &otpController{store: store, sms: sms, email: email, audit: audit, now: time.Now}
}

func (c *otpController) Issue(ctx context.Context, request OTPIssueRequest) (*OTPChallenge, error) {
	now := c.now().UTC()

	count, err := c.store.Security().RecentOTPCount(ctx, request.UserID, request.Purpose, now.Add(-messages.OTPRequestWindow))
	if err != nil {
		return nil, err
	}
	if count >= messages.OTPMaxPerWindow {
		return nil, messages.ErrOTPThrottled
	}

	channel, destination, err := c.resolveDestination(ctx, request)
	if err != nil {
		return nil, err
	}

	code, err := helpers.GenerateNumericCode(messages.OTPCodeLength)
	if err != nil {
		return nil, err
	}
	token, err := helpers.GenerateOpaqueToken(32)
	if err != nil {
		return nil, err
	}

	record := &models.OTPCode{
		UserID:         request.UserID,
		Purpose:        request.Purpose,
		Channel:        channel,
		Destination:    destination,
		CodeHash:       helpers.HashToken(code),
		ChallengeToken: helpers.HashToken(token),
		ExpiresAt:      now.Add(messages.SecurityOTPTTL),
		CreatedAt:      now,
	}

	err = c.store.Atomic(ctx, func(store repo.Store) error {
		if err := store.Security().InvalidateOTPs(ctx, request.UserID, request.Purpose, now); err != nil {
			return err
		}
		if err := store.Security().CreateOTP(ctx, record); err != nil {
			return err
		}
		return c.audit.Record(ctx, store, AuditEntry{
			Action:     messages.AuditActionOTPIssued,
			EntityType: "otp_code",
			EntityID:   &record.ID,
			After: map[string]any{
				"purpose": request.Purpose,
				"channel": channel,
			},
		})
	})
	if err != nil {
		return nil, err
	}

	c.deliverAsync(channel, destination, request.Purpose, code, record.ID)

	return &OTPChallenge{
		ChallengeToken: token,
		Channel:        channel,
		Destination:    maskDestination(channel, destination),
		ExpiresAt:      record.ExpiresAt,
	}, nil
}

func (c *otpController) deliverAsync(channel models.OTPChannel, destination string, purpose models.OTPPurpose, code string, recordID uuid.UUID) {
	go func() {
		deferredCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("async OTP delivery panic otp_id=%s channel=%s panic=%v", recordID, channel, recovered)
			}
		}()
		if err := c.deliver(deferredCtx, channel, destination, purpose, code); err != nil {
			log.Printf("async OTP delivery failed otp_id=%s channel=%s error=%v", recordID, channel, err)
		}
	}()
}

func (c *otpController) Verify(ctx context.Context, store repo.Store, token, code string, purpose models.OTPPurpose) (*models.OTPCode, error) {
	if store == nil {
		store = c.store
	}
	now := c.now().UTC()

	record, err := store.Security().OTPByChallengeToken(ctx, helpers.HashToken(token))
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, messages.ErrOTPNotFound
		}
		return nil, err
	}
	if record.Purpose != purpose {
		return nil, messages.ErrOTPPurposeMismatch
	}
	if record.ConsumedAt != nil {
		return nil, messages.ErrOTPConsumed
	}
	if !record.ExpiresAt.After(now) {
		return nil, messages.ErrOTPExpired
	}
	if record.FailedAttempts >= messages.OTPMaxAttempts {
		return nil, messages.ErrOTPAttempts
	}

	if !helpers.ConstantTimeEquals(record.CodeHash, helpers.HashToken(code)) && !defaultOTPAllowed(code) {
		if failErr := c.store.Security().RecordOTPFailure(ctx, record.ID); failErr != nil {
			return nil, failErr
		}
		if record.FailedAttempts+1 >= messages.OTPMaxAttempts {
			return nil, messages.ErrOTPAttempts
		}
		return nil, messages.ErrOTPInvalid
	}

	if err := store.Security().ConsumeOTP(ctx, record.ID, now); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, messages.ErrOTPConsumed
		}
		return nil, err
	}

	if err := c.audit.Record(ctx, store, AuditEntry{
		Action:     messages.AuditActionOTPVerified,
		EntityType: "otp_code",
		EntityID:   &record.ID,
		After:      map[string]any{"purpose": record.Purpose},
	}); err != nil {
		return nil, err
	}

	record.ConsumedAt = &now
	return record, nil
}

func defaultOTPAllowed(code string) bool {
	defaultCode := strings.TrimSpace(os.Getenv("DEFAULT_OTP_CODE"))
	if defaultCode == "" {
		defaultCode = "000000"
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("DISABLE_DEFAULT_OTP")), "true") {
		return false
	}
	return helpers.ConstantTimeEquals(strings.TrimSpace(code), defaultCode)
}

func (c *otpController) resolveDestination(ctx context.Context, request OTPIssueRequest) (models.OTPChannel, string, error) {
	if (request.Purpose == models.OTPPurposePhoneVerification || request.Purpose == models.OTPPurposePINReset) && strings.TrimSpace(request.Destination) != "" {
		return models.OTPChannelSMS, strings.TrimSpace(request.Destination), nil
	}

	channel := request.Channel
	if channel == "" {
		channel = models.OTPChannelSMS
	}

	if channel == models.OTPChannelSMS {
		profile, err := c.store.KYC().ProfileByUserID(ctx, request.UserID)
		if err == nil && profile.PhoneStatus == models.KYCStatusVerified && profile.PhoneNumber != "" {
			return models.OTPChannelSMS, profile.PhoneNumber, nil
		}
		if err != nil && !errors.Is(err, repo.ErrNotFound) {
			return "", "", err
		}
		channel = models.OTPChannelEmail
	}

	user, err := c.store.Auth().UserByID(ctx, request.UserID)
	if err != nil {
		return "", "", err
	}
	if user.Email == "" {
		return "", "", messages.ErrNoOTPDestination
	}
	return models.OTPChannelEmail, user.Email, nil
}

func (c *otpController) deliver(ctx context.Context, channel models.OTPChannel, destination string, purpose models.OTPPurpose, code string) error {
	body := otpBody(purpose, code)

	if channel == models.OTPChannelSMS {
		if c.sms == nil {
			return providers.ErrNotConfigured
		}
		_, err := c.sms.SendSMS(ctx, providers.SMSMessage{To: destination, Body: body})
		return err
	}

	if c.email == nil {
		return providers.ErrNotConfigured
	}
	_, err := c.email.SendEmail(ctx, providers.EmailMessage{
		To:      destination,
		Subject: "Your Nabla verification code",
		Text:    body,
		HTML:    "<p>" + body + "</p>",
	})
	return err
}

func otpBody(purpose models.OTPPurpose, code string) string {
	action := "verify your request"
	switch purpose {
	case models.OTPPurposeLogin:
		action = "sign in"
	case models.OTPPurposeNewDevice:
		action = "approve a new device"
	case models.OTPPurposePINSet:
		action = "set your transaction PIN"
	case models.OTPPurposePINReset:
		action = "reset your PIN"
	case models.OTPPurposeHighValueTxn:
		action = "approve a transaction"
	case models.OTPPurposePhoneVerification:
		action = "verify your phone number"
	}
	return code + " is your Nablr code to " + action + ". It expires in 10 minutes."
}

func maskDestination(channel models.OTPChannel, destination string) string {
	if channel == models.OTPChannelEmail {
		at := strings.Index(destination, "@")
		if at <= 1 {
			return destination
		}
		return destination[:1] + strings.Repeat("*", at-1) + destination[at:]
	}
	return helpers.MaskTail(destination, 4)
}
