package controllers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"nabla/identity-svc/internal/alerting"
	"nabla/identity-svc/internal/clients/transfers"
	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	"nabla/identity-svc/internal/models"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	repo "nabla/identity-svc/internal/repository"
)

type (
	ClientInfo struct{ UserAgent, IPAddress string }
	AuthResult struct {
		AccessToken  string             `json:"access_token"`
		RefreshToken string             `json:"refresh_token"`
		ExpiresIn    int64              `json:"expires_in"`
		User         *models.User       `json:"user"`
		KYC          *models.KYCProfile `json:"-"`
	}
)

type RegisterResult struct {
	User              *models.User `json:"user"`
	VerificationToken string       `json:"verification_token,omitempty"`
}
type TokenIssueResult struct {
	Token string `json:"token,omitempty"`
}

// AccountClosureChallenge is returned once facial re-verification passes for
// an account closure request. The code is shown directly to the user by the
// app (no SMS/email delivery) and must be typed back in to confirm.
type AccountClosureChallenge struct {
	RequestID uuid.UUID `json:"request_id"`
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AccountDeactivatedError is returned by Login instead of ErrAccountSuspended
// when the account is merely deactivated (not suspended/deleted), carrying a
// freshly issued restore token so the handler can let the app redeem it
// through the unauthenticated restore endpoint without ever issuing a full
// access token to a still-deactivated account.
type AccountDeactivatedError struct {
	RestoreToken string
}

func (e *AccountDeactivatedError) Error() string { return messages.ErrAccountDeactivated.Error() }

func (e *AccountDeactivatedError) Is(target error) bool {
	return target == messages.ErrAccountDeactivated
}

type PasswordResetStart struct {
	Method         string            `json:"method"` // "email" | "sms"
	Token          string            `json:"token,omitempty"`
	ChallengeToken string            `json:"challenge_token,omitempty"`
	Channel        models.OTPChannel `json:"channel,omitempty"`
	Destination    string            `json:"destination,omitempty"`
	ExpiresAt      *time.Time        `json:"expires_at,omitempty"`
}

type AuthConfig struct {
	JWTSecret, JWTIssuer                                                                                      string
	AccessTokenTTL, RefreshTokenTTL, VerificationTokenTTL, PasswordResetTTL, MagicLinkTTL, OnboardingTokenTTL time.Duration
}
type GoogleVerifier interface {
	Verify(context.Context, string) (string, string, error)
}
type AuthController interface {
	Register(context.Context, models.RegisterRequest, string) (*RegisterResult, error)
	Login(context.Context, models.LoginRequest, ClientInfo) (*AuthResult, error)
	Google(context.Context, models.GoogleAuthRequest, ClientInfo) (*AuthResult, error)
	Refresh(context.Context, models.RefreshTokenRequest, ClientInfo) (*AuthResult, error)
	Logout(context.Context, models.LogoutRequest) error
	VerifyEmail(context.Context, models.VerifyEmailRequest) error
	ResendVerification(context.Context, models.ResendVerificationRequest) (*TokenIssueResult, error)
	ForgotPassword(context.Context, models.ForgotPasswordRequest) (*PasswordResetStart, error)
	ResetPassword(context.Context, models.ResetPasswordRequest) error
	RequestPhonePasswordReset(context.Context, models.PhonePasswordResetRequest) (*OTPChallenge, error)
	ConfirmPhonePasswordReset(context.Context, models.ConfirmPhonePasswordResetRequest) error
	RequestMagicLink(context.Context, models.MagicLinkRequest, ClientInfo) (*TokenIssueResult, error)
	ConfirmMagicLink(context.Context, models.ConfirmMagicLinkRequest, ClientInfo) (*AuthResult, error)
	StartPhoneOnboarding(context.Context, string, string) (*models.User, string, error)
	RefreshPhoneOnboardingSession(context.Context, string) (*models.User, string, error)
	OnboardingUser(context.Context, string) (*models.User, error)
	MarkOnboardingPhoneVerified(context.Context, uuid.UUID) (*models.User, error)
	CompletePhoneOnboarding(context.Context, string, string, ClientInfo) (*AuthResult, error)
	// Account closure: reason -> facial re-verification -> confirmation code
	// -> deactivation. Replaces the old bare "deactivate" endpoint.
	RequestAccountClosure(ctx context.Context, userID uuid.UUID, reason models.AccountClosureReason, note string) (*models.AccountClosureRequest, error)
	IssueAccountClosureCode(ctx context.Context, userID, requestID uuid.UUID) (*AccountClosureChallenge, error)
	ConfirmAccountClosure(ctx context.Context, userID, requestID uuid.UUID, code string) (*models.User, error)
	// Restore. Self-serve in-app (still logged in) or via a login attempt
	// (see Login's handling of a deactivated account).
	ReactivateAccount(context.Context, uuid.UUID) (*models.User, error)
	// RestoreWithToken redeems the restore token issued by Login for a
	// deactivated account (see AccountDeactivatedError), without requiring an
	// access token.
	RestoreWithToken(context.Context, string) (*models.User, error)
}

func (c *authController) StartPhoneOnboarding(ctx context.Context, rawPhone, waitlistToken string) (*models.User, string, error) {
	phone, err := helpers.ValidatePhoneNumber(rawPhone)
	if err != nil {
		return nil, "", err
	}
	user, err := c.repo.UserByPhoneIncludingDeleted(ctx, phone)
	if err == nil {
		if user.Status == models.UserStatusDeactivated {
			// Reactivate the deactivated account and start onboarding
			user, err = c.claimWaitlistUsername(ctx, user, waitlistToken)
			if err != nil {
				return nil, "", err
			}
			return c.newOnboardingSession(ctx, user)
		}
		if user.PasswordSet || user.Status == models.UserStatusActive {
			return nil, "", messages.ErrPhoneExists
		}
		user, err = c.claimWaitlistUsername(ctx, user, waitlistToken)
		if err != nil {
			return nil, "", err
		}
		return c.newOnboardingSession(ctx, user)
	} else if !errors.Is(err, repo.ErrNotFound) {
		return nil, "", err
	}
	user, err = c.repo.CreatePhoneUser(ctx, phone)
	if err != nil {
		return nil, "", err
	}
	user, err = c.claimWaitlistUsername(ctx, user, waitlistToken)
	if err != nil {
		return nil, "", err
	}
	return c.newOnboardingSession(ctx, user)
}

func (c *authController) claimWaitlistUsername(ctx context.Context, user *models.User, rawToken string) (*models.User, error) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return user, nil
	}
	claimed, err := c.repo.ClaimWaitlistUsername(ctx, user.ID, helpers.HashToken(rawToken), c.now().UTC())
	if errors.Is(err, repo.ErrNotFound) {
		return nil, messages.ErrInvalidToken
	}
	if errors.Is(err, repo.ErrExpired) {
		return nil, messages.ErrWaitlistHoldExpired
	}
	if errors.Is(err, repo.ErrConflict) {
		return nil, messages.ErrUsernameTaken
	}
	return claimed, err
}

func (c *authController) RefreshPhoneOnboardingSession(ctx context.Context, rawPhone string) (*models.User, string, error) {
	phone, err := helpers.ValidatePhoneNumber(rawPhone)
	if err != nil {
		return nil, "", err
	}
	user, err := c.repo.UserByPhoneIncludingDeleted(ctx, phone)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, "", messages.ErrInvalidPhoneNumber
		}
		return nil, "", err
	}
	return c.newOnboardingSession(ctx, user)
}

func (c *authController) newOnboardingSession(ctx context.Context, user *models.User) (*models.User, string, error) {
	raw, err := helpers.GenerateOpaqueToken(32)
	if err != nil {
		return nil, "", err
	}
	now := c.now().UTC()
	ttl := c.config.OnboardingTokenTTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	_ = c.repo.InvalidateVerificationTokens(ctx, user.ID, models.VerificationTokenPhoneOnboarding, now)
	record := &models.VerificationToken{UserID: user.ID, Type: models.VerificationTokenPhoneOnboarding, TokenHash: helpers.HashToken(raw), ExpiresAt: now.Add(ttl), CreatedAt: now}
	log.Printf("issued onboarding token hash=%s... user=%s kind=%s expires=%s", helpers.HashToken(raw)[:12], user.ID, record.Type, record.ExpiresAt.Format(time.RFC3339))
	if err := c.repo.CreateVerificationToken(ctx, record); err != nil {
		return nil, "", err
	}
	return user, raw, nil
}

func (c *authController) OnboardingUser(ctx context.Context, raw string) (*models.User, error) {
	record, err := c.validVerification(ctx, raw, models.VerificationTokenPhoneOnboarding)
	if err != nil {
		return nil, err
	}
	return c.repo.UserByID(ctx, record.UserID)
}

func (c *authController) MarkOnboardingPhoneVerified(ctx context.Context, id uuid.UUID) (*models.User, error) {
	return c.repo.MarkPhoneVerified(ctx, id)
}

func (c *authController) CompletePhoneOnboarding(ctx context.Context, raw, password string, client ClientInfo) (*AuthResult, error) {
	record, err := c.validVerification(ctx, raw, models.VerificationTokenPhoneOnboarding)
	if err != nil {
		return nil, err
	}
	user, err := c.repo.UserByID(ctx, record.UserID)
	if err != nil || !user.PhoneVerified {
		return nil, messages.ErrPhoneNotVerified
	}
	if user.PasswordSet && user.Status == models.UserStatusActive {
		return nil, messages.ErrPhoneExists
	}
	hash, err := helpers.Hash(password)
	if err != nil {
		return nil, err
	}
	user, err = c.repo.SetPassword(ctx, user.ID, hash)
	if err != nil {
		return nil, err
	}
	now := c.now().UTC()
	user.Status = models.UserStatusActive
	if err := c.repo.UpdateUser(ctx, user); err != nil {
		return nil, err
	}
	if err := c.repo.ConsumeVerificationToken(ctx, record.ID, now); err != nil {
		return nil, messages.ErrInvalidToken
	}
	// The database transition atomically emits identity.onboarding.completed.
	// Transfers consumes it and idempotently provisions the wallet and funding
	// account, so a downstream outage never loses or delays this response.
	return c.issueSession(ctx, user, uuid.New(), client)
}

// deactivate is the actual account-deactivation mechanics: soft-delete the
// user, revoke every refresh token, and invalidate any outstanding
// verification tokens. It's shared by ConfirmAccountClosure now that the
// bare deactivate endpoint has been replaced by the closure flow.
func (c *authController) deactivate(ctx context.Context, userID uuid.UUID) (*models.User, error) {
	user, err := c.repo.SoftDeleteUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	// Revoke all refresh tokens for the user
	now := c.now().UTC()
	if err := c.repo.RevokeUserRefreshTokens(ctx, userID, now); err != nil {
		return nil, err
	}
	if err := c.repo.InvalidateVerificationTokens(ctx, userID, models.VerificationTokenEmailVerification, now); err != nil {
		return nil, err
	}
	if err := c.repo.InvalidateVerificationTokens(ctx, userID, models.VerificationTokenPasswordReset, now); err != nil {
		return nil, err
	}
	if err := c.repo.InvalidateVerificationTokens(ctx, userID, models.VerificationTokenMagicLogin, now); err != nil {
		return nil, err
	}
	if err := c.repo.InvalidateVerificationTokens(ctx, userID, models.VerificationTokenPhoneOnboarding, now); err != nil {
		return nil, err
	}
	return user, nil
}

// RequestAccountClosure records why the user wants to close their account.
// It's step 1 of 3; the account is not touched yet.
func (c *authController) RequestAccountClosure(ctx context.Context, userID uuid.UUID, reason models.AccountClosureReason, note string) (*models.AccountClosureRequest, error) {
	if !models.IsValidAccountClosureReason(reason) {
		return nil, messages.ErrClosureReasonInvalid
	}
	user, err := c.repo.UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user.Status == models.UserStatusDeactivated {
		return nil, messages.ErrAccountAlreadyDeactivated
	}
	request := &models.AccountClosureRequest{
		UserID:     userID,
		Reason:     reason,
		ReasonNote: strings.TrimSpace(note),
		Status:     models.AccountClosureStatusPendingVerification,
	}
	if err := c.repo.CreateAccountClosureRequest(ctx, request); err != nil {
		return nil, err
	}
	return request, nil
}

// IssueAccountClosureCode is step 2: called once the caller's facial
// liveness check (via KYCController.SubmitFacialVerification) has already
// passed. It mints a short-lived confirmation code for the app to display —
// there's no SMS/email leg, the code only needs to prove the person is still
// looking at the screen that just verified their face.
func (c *authController) IssueAccountClosureCode(ctx context.Context, userID, requestID uuid.UUID) (*AccountClosureChallenge, error) {
	request, err := c.repo.AccountClosureRequestByID(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if request.UserID != userID {
		return nil, messages.ErrClosureRequestNotFound
	}
	if request.Status == models.AccountClosureStatusConfirmed || request.Status == models.AccountClosureStatusCancelled {
		return nil, messages.ErrClosureRequestNotFound
	}
	code, err := helpers.GenerateNumericCode(messages.AccountClosureCodeLength)
	if err != nil {
		return nil, err
	}
	now := c.now().UTC()
	expiresAt := now.Add(messages.AccountClosureCodeTTL)
	request.Status = models.AccountClosureStatusPendingConfirmation
	request.FacialVerifiedAt = &now
	request.CodeHash = helpers.HashToken(code)
	request.CodeExpiresAt = &expiresAt
	request.CodeAttempts = 0
	if err := c.repo.UpdateAccountClosureRequest(ctx, request); err != nil {
		return nil, err
	}
	return &AccountClosureChallenge{RequestID: request.ID, Code: code, ExpiresAt: expiresAt}, nil
}

// ConfirmAccountClosure is step 3: check the code, check the wallet balance
// is zero, then actually deactivate the account.
func (c *authController) ConfirmAccountClosure(ctx context.Context, userID, requestID uuid.UUID, code string) (*models.User, error) {
	request, err := c.repo.AccountClosureRequestByID(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if request.UserID != userID {
		return nil, messages.ErrClosureRequestNotFound
	}
	if request.Status != models.AccountClosureStatusPendingConfirmation {
		return nil, messages.ErrClosureFaceNotVerified
	}
	now := c.now().UTC()
	if request.CodeExpiresAt == nil || now.After(*request.CodeExpiresAt) {
		return nil, messages.ErrClosureCodeExpired
	}
	if request.CodeAttempts >= messages.AccountClosureMaxCodeAttempts {
		return nil, messages.ErrClosureCodeAttempts
	}
	if !helpers.ConstantTimeEquals(helpers.HashToken(strings.TrimSpace(code)), request.CodeHash) {
		request.CodeAttempts++
		_ = c.repo.UpdateAccountClosureRequest(ctx, request)
		return nil, messages.ErrClosureCodeInvalid
	}

	// The balance check happens after the code check (why leak a balance
	// requirement to someone who hasn't even proven the code yet) and before
	// anything is actually deactivated.
	if c.transfers == nil {
		return nil, messages.ErrProviderUnavailable
	}
	wallet, err := c.transfers.GetWallet(ctx, userID, "NGN")
	if err != nil {
		return nil, err
	}
	if !wallet.Zero() {
		return nil, messages.ErrClosureBalanceNotZero
	}

	user, err := c.deactivate(ctx, userID)
	if err != nil {
		return nil, err
	}
	request.Status = models.AccountClosureStatusConfirmed
	request.ConfirmedAt = &now
	if err := c.repo.UpdateAccountClosureRequest(ctx, request); err != nil {
		return nil, err
	}
	return user, nil
}

func (c *authController) ReactivateAccount(ctx context.Context, userID uuid.UUID) (*models.User, error) {
	user, err := c.repo.UserByIDIncludingDeleted(ctx, userID)
	if err != nil {
		return nil, err
	}
	// Only allow reactivation if user is deactivated
	if user.Status != models.UserStatusDeactivated || user.DeletedAt == nil {
		return nil, messages.ErrAccountNotDeactivated
	}
	return c.repo.RestoreUser(ctx, userID)
}

// RestoreWithToken is the unauthenticated counterpart of ReactivateAccount:
// it lets someone who just failed to log in because their account is
// deactivated (see Login and AccountDeactivatedError) restore it using the
// token issued at that moment, without ever holding an access token for a
// still-deactivated account.
func (c *authController) RestoreWithToken(ctx context.Context, raw string) (*models.User, error) {
	record, err := c.validVerification(ctx, raw, models.VerificationTokenAccountRestore)
	if err != nil {
		return nil, err
	}
	user, err := c.repo.UserByIDIncludingDeleted(ctx, record.UserID)
	if err != nil {
		return nil, err
	}
	if user.Status != models.UserStatusDeactivated || user.DeletedAt == nil {
		return nil, messages.ErrAccountNotDeactivated
	}
	now := c.now().UTC()
	if err := c.repo.ConsumeVerificationToken(ctx, record.ID, now); err != nil {
		return nil, err
	}
	return c.repo.RestoreUser(ctx, record.UserID)
}

// KYCProfileProvider is the minimal KYC dependency the auth controller needs
// to attach the caller's KYC profile to login responses. KYCController
// satisfies this interface, so it can be passed directly.
type KYCProfileProvider interface {
	Profile(ctx context.Context, userID uuid.UUID) (*models.KYCProfile, error)
}

// TransfersWalletProvider is the minimal transfers-svc dependency needed to
// gate account closure on a zero wallet balance. *transfers.Client (see
// internal/clients/transfers) satisfies this directly.
type TransfersWalletProvider interface {
	GetWallet(ctx context.Context, userID uuid.UUID, currency string) (*transfers.Wallet, error)
	ProvisionCustomer(ctx context.Context, userID uuid.UUID, currency string) error
}

type authController struct {
	repo      repo.AuthRepository
	otp       OTPController
	delivery  TokenDelivery
	alerts    alerting.Notifier
	google    GoogleVerifier
	config    AuthConfig
	kyc       KYCProfileProvider
	transfers TransfersWalletProvider
	now       func() time.Time
}

func NewAuthController(repository repo.AuthRepository, otp OTPController, delivery TokenDelivery, google GoogleVerifier, config AuthConfig, alerts alerting.Notifier, kyc KYCProfileProvider, transfersClient TransfersWalletProvider) AuthController {
	return &authController{repo: repository, otp: otp, delivery: delivery, alerts: alerts, google: google, config: config, kyc: kyc, transfers: transfersClient, now: time.Now}
}

func (c *authController) Register(ctx context.Context, request models.RegisterRequest, waitlistToken string) (*RegisterResult, error) {
	email := strings.ToLower(strings.TrimSpace(request.Email))
	if _, err := c.repo.UserByEmailIncludingDeleted(ctx, email); err == nil {
		return nil, messages.ErrEmailExists
	} else if !errors.Is(err, repo.ErrNotFound) {
		return nil, err
	}
	phone, err := helpers.ValidatePhoneNumber(request.PhoneNumber)
	if err != nil {
		return nil, messages.ErrInvalidPhoneNumber
	}
	existingPhoneUser, err := c.repo.UserByPhoneIncludingDeleted(ctx, phone)
	if err == nil && !canAttachEmailRegistration(existingPhoneUser) {
		return nil, messages.ErrPhoneExists
	} else if !errors.Is(err, repo.ErrNotFound) {
		return nil, err
	}
	hash, err := helpers.Hash(request.Password)
	if err != nil {
		return nil, err
	}
	var user *models.User
	if existingPhoneUser != nil && canAttachEmailRegistration(existingPhoneUser) {
		existingPhoneUser.Email = email
		existingPhoneUser.PhoneNumber = phone
		existingPhoneUser.Role = models.UserRoleBuyer
		existingPhoneUser.Status = models.UserStatusPendingVerification
		if err := c.repo.UpdateUser(ctx, existingPhoneUser); err != nil {
			return nil, err
		}
		user, err = c.repo.SetPassword(ctx, existingPhoneUser.ID, hash)
		if err != nil {
			return nil, err
		}
	} else {
		user = &models.User{Email: email, PhoneNumber: phone, PasswordHash: &hash, Role: models.UserRoleBuyer, Status: models.UserStatusPendingVerification}
		if err := c.repo.CreateUser(ctx, user); err != nil {
			if errors.Is(err, repo.ErrConflict) {
				if _, lookupErr := c.repo.UserByEmail(ctx, email); lookupErr == nil {
					return nil, messages.ErrEmailExists
				}
				if _, lookupErr := c.repo.UserByPhone(ctx, phone); lookupErr == nil {
					return nil, messages.ErrPhoneExists
				}
			}
			return nil, err
		}
	}
	user, err = c.claimWaitlistUsername(ctx, user, waitlistToken)
	if err != nil {
		return nil, err
	}
	token, err := c.sendTokenAsync(ctx, user, models.VerificationTokenEmailVerification, c.config.VerificationTokenTTL, ClientInfo{})
	if err != nil {
		return nil, err
	}
	return &RegisterResult{User: user, VerificationToken: developmentToken(token)}, nil
}

func canAttachEmailRegistration(user *models.User) bool {
	if user == nil {
		return false
	}
	if strings.TrimSpace(user.Email) != "" {
		return false
	}
	if user.PasswordSet || user.PasswordHash != nil {
		return false
	}
	return user.Status != models.UserStatusActive && user.Status != models.UserStatusSuspended && user.Status != models.UserStatusDeleted && user.Status != models.UserStatusDeactivated
}

func (c *authController) Login(ctx context.Context, request models.LoginRequest, client ClientInfo) (*AuthResult, error) {
	user, err := c.loginUser(ctx, request)
	if err != nil || user.PasswordHash == nil || !helpers.Matches(*user.PasswordHash, request.Password) {
		return nil, messages.ErrInvalidCredentials
	}
	if user.Status == models.UserStatusSuspended || user.Status == models.UserStatusDeleted {
		return nil, messages.ErrAccountSuspended
	}
	if user.Status == models.UserStatusDeactivated {
		token, tokenErr := c.createVerificationToken(ctx, user, models.VerificationTokenAccountRestore, messages.AccountRestoreTokenTTL, client)
		if tokenErr != nil {
			return nil, tokenErr
		}
		return nil, &AccountDeactivatedError{RestoreToken: token}
	}
	if strings.TrimSpace(request.Email) != "" && user.EmailVerifiedAt == nil {
		return nil, messages.ErrEmailNotVerified
	}
	if strings.TrimSpace(request.PhoneNumber) != "" && !user.PhoneVerified {
		return nil, messages.ErrPhoneNotVerified
	}
	now := c.now().UTC()
	user.LastLoginAt = &now
	_ = c.repo.UpdateUser(ctx, user)
	result, err := c.issueSession(ctx, user, uuid.New(), client)
	if err != nil {
		return nil, err
	}
	if c.kyc != nil {
		if profile, kycErr := c.kyc.Profile(ctx, user.ID); kycErr == nil {
			result.KYC = profile
		}
	}
	return result, nil
}

func (c *authController) loginUser(ctx context.Context, request models.LoginRequest) (*models.User, error) {
	email := strings.TrimSpace(request.Email)
	if email != "" {
		return c.repo.UserByEmail(ctx, email)
	}
	phone, err := helpers.ValidatePhoneNumber(request.PhoneNumber)
	if err != nil {
		return nil, messages.ErrInvalidCredentials
	}
	return c.repo.UserByPhone(ctx, phone)
}

func (c *authController) Google(ctx context.Context, request models.GoogleAuthRequest, client ClientInfo) (*AuthResult, error) {
	if c.google == nil {
		return nil, messages.ErrProviderUnavailable
	}
	return nil, messages.ErrProviderUnavailable
}

func (c *authController) Refresh(ctx context.Context, request models.RefreshTokenRequest, client ClientInfo) (*AuthResult, error) {
	now := c.now().UTC()
	old, err := c.repo.RefreshTokenByHash(ctx, helpers.HashToken(request.RefreshToken))
	if err != nil || old.RevokedAt != nil || !old.ExpiresAt.After(now) {
		return nil, messages.ErrInvalidToken
	}
	user, err := c.repo.UserByID(ctx, old.UserID)
	if err != nil {
		return nil, messages.ErrInvalidToken
	}
	if err := c.repo.RevokeRefreshToken(ctx, old.ID, now); err != nil {
		return nil, messages.ErrInvalidToken
	}
	return c.issueSession(ctx, user, old.FamilyID, client)
}

func (c *authController) Logout(ctx context.Context, request models.LogoutRequest) error {
	record, err := c.repo.RefreshTokenByHash(ctx, helpers.HashToken(request.RefreshToken))
	if err != nil {
		return nil
	}
	return c.repo.RevokeRefreshToken(ctx, record.ID, c.now().UTC())
}

func (c *authController) VerifyEmail(ctx context.Context, request models.VerifyEmailRequest) error {
	record, err := c.validVerification(ctx, request.Token, models.VerificationTokenEmailVerification)
	if err != nil {
		return err
	}
	user, err := c.repo.UserByID(ctx, record.UserID)
	if err != nil {
		return messages.ErrInvalidToken
	}
	now := c.now().UTC()
	user.EmailVerifiedAt = &now
	user.Status = models.UserStatusActive
	if err := c.repo.UpdateUser(ctx, user); err != nil {
		return err
	}
	return c.repo.ConsumeVerificationToken(ctx, record.ID, now)
}

func (c *authController) ResendVerification(ctx context.Context, request models.ResendVerificationRequest) (*TokenIssueResult, error) {
	user, err := c.repo.UserByEmail(ctx, request.Email)
	if err != nil || user.EmailVerifiedAt != nil {
		return &TokenIssueResult{}, nil
	}
	token, err := c.sendTokenAsync(ctx, user, models.VerificationTokenEmailVerification, c.config.VerificationTokenTTL, ClientInfo{})
	return &TokenIssueResult{Token: developmentToken(token)}, err
}

func (c *authController) ForgotPassword(ctx context.Context, request models.ForgotPasswordRequest) (*PasswordResetStart, error) {
	phone := strings.TrimSpace(request.PhoneNumber)
	if phone != "" {
		return c.forgotPasswordByPhone(ctx, phone)
	}

	email := strings.TrimSpace(request.Email)
	if email == "" {
		return nil, messages.ErrInvalidCredentials
	}
	user, err := c.repo.UserByEmail(ctx, email)
	if err != nil {
		// Never reveal whether an account exists.
		return &PasswordResetStart{Method: "email"}, nil
	}
	token, err := c.sendTokenAsync(ctx, user, models.VerificationTokenPasswordReset, c.config.PasswordResetTTL, ClientInfo{})
	if err != nil {
		return nil, err
	}
	return &PasswordResetStart{Method: "email", Token: developmentToken(token)}, nil
}

func (c *authController) forgotPasswordByPhone(ctx context.Context, phone string) (*PasswordResetStart, error) {
	if c.otp == nil {
		return nil, messages.ErrProviderUnavailable
	}
	normalized, err := helpers.ValidatePhoneNumber(phone)
	if err != nil {
		return nil, messages.ErrInvalidPhoneNumber
	}
	user, err := c.repo.UserByPhone(ctx, normalized)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return &PasswordResetStart{Method: "sms"}, nil
		}
		return nil, err
	}
	challenge, err := c.otp.Issue(ctx, OTPIssueRequest{
		UserID:      user.ID,
		Purpose:     models.OTPPurposePINReset,
		Channel:     models.OTPChannelSMS,
		Destination: normalized,
	})
	if err != nil {
		return nil, err
	}
	return &PasswordResetStart{
		Method:         "sms",
		ChallengeToken: challenge.ChallengeToken,
		Channel:        challenge.Channel,
		Destination:    challenge.Destination,
		ExpiresAt:      &challenge.ExpiresAt,
	}, nil
}

func (c *authController) ResetPassword(ctx context.Context, request models.ResetPasswordRequest) error {
	if strings.TrimSpace(request.ChallengeToken) != "" {
		return c.resetPasswordByPhone(ctx, request)
	}
	return c.resetPasswordByEmail(ctx, request)
}

func (c *authController) resetPasswordByEmail(ctx context.Context, request models.ResetPasswordRequest) error {
	record, err := c.validVerification(ctx, request.Token, models.VerificationTokenPasswordReset)
	if err != nil {
		return err
	}
	if err := helpers.Validate(request.NewPassword); err != nil {
		return messages.ErrWeakPassword
	}
	hash, err := helpers.Hash(request.NewPassword)
	if err != nil {
		return err
	}
	user, err := c.repo.UserByID(ctx, record.UserID)
	if err != nil {
		return messages.ErrInvalidToken
	}
	user.PasswordHash = &hash
	if err := c.repo.UpdateUser(ctx, user); err != nil {
		return err
	}
	now := c.now().UTC()
	_ = c.repo.RevokeUserRefreshTokens(ctx, user.ID, now)
	return c.repo.ConsumeVerificationToken(ctx, record.ID, now)
}

func (c *authController) resetPasswordByPhone(ctx context.Context, request models.ResetPasswordRequest) error {
	if c.otp == nil {
		return messages.ErrProviderUnavailable
	}
	if err := validatePIN(request.NewPassword); err != nil {
		return err
	}
	record, err := c.otp.Verify(ctx, nil, request.ChallengeToken, request.Code, models.OTPPurposePINReset)
	if err != nil {
		return err
	}
	hash, err := helpers.Hash(request.NewPassword)
	if err != nil {
		return err
	}
	user, err := c.repo.SetPassword(ctx, record.UserID, hash)
	if err != nil {
		return err
	}
	user.Status = models.UserStatusActive
	if err := c.repo.UpdateUser(ctx, user); err != nil {
		return err
	}
	_ = c.repo.RevokeUserRefreshTokens(ctx, user.ID, c.now().UTC())
	return nil
}

func (c *authController) RequestPhonePasswordReset(ctx context.Context, request models.PhonePasswordResetRequest) (*OTPChallenge, error) {
	if c.otp == nil {
		return nil, messages.ErrProviderUnavailable
	}
	phone, err := helpers.ValidatePhoneNumber(request.PhoneNumber)
	if err != nil {
		return nil, messages.ErrInvalidPhoneNumber
	}
	user, err := c.repo.UserByPhone(ctx, phone)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return c.otp.Issue(ctx, OTPIssueRequest{
		UserID:      user.ID,
		Purpose:     models.OTPPurposePINReset,
		Channel:     models.OTPChannelSMS,
		Destination: phone,
	})
}

func (c *authController) ConfirmPhonePasswordReset(ctx context.Context, request models.ConfirmPhonePasswordResetRequest) error {
	if c.otp == nil {
		return messages.ErrProviderUnavailable
	}
	record, err := c.otp.Verify(ctx, nil, request.ChallengeToken, request.Code, models.OTPPurposePINReset)
	if err != nil {
		return err
	}
	hash, err := helpers.Hash(request.NewPassword)
	if err != nil {
		return err
	}
	user, err := c.repo.SetPassword(ctx, record.UserID, hash)
	if err != nil {
		return err
	}
	user.Status = models.UserStatusActive
	if err := c.repo.UpdateUser(ctx, user); err != nil {
		return err
	}
	_ = c.repo.RevokeUserRefreshTokens(ctx, user.ID, c.now().UTC())
	return nil
}

func (c *authController) RequestMagicLink(ctx context.Context, request models.MagicLinkRequest, client ClientInfo) (*TokenIssueResult, error) {
	user, err := c.repo.UserByEmail(ctx, request.Email)
	if err != nil {
		return &TokenIssueResult{}, nil
	}
	token, err := c.sendTokenAsync(ctx, user, models.VerificationTokenMagicLogin, c.config.MagicLinkTTL, client)
	return &TokenIssueResult{Token: developmentToken(token)}, err
}

func (c *authController) ConfirmMagicLink(ctx context.Context, request models.ConfirmMagicLinkRequest, client ClientInfo) (*AuthResult, error) {
	record, err := c.validVerification(ctx, request.Token, models.VerificationTokenMagicLogin)
	if err != nil {
		return nil, err
	}
	user, err := c.repo.UserByID(ctx, record.UserID)
	if err != nil {
		return nil, messages.ErrInvalidToken
	}
	if err := c.repo.ConsumeVerificationToken(ctx, record.ID, c.now().UTC()); err != nil {
		return nil, messages.ErrInvalidToken
	}
	return c.issueSession(ctx, user, uuid.New(), client)
}

func (c *authController) validVerification(ctx context.Context, raw string, kind models.VerificationTokenType) (*models.VerificationToken, error) {
	hashed := helpers.HashToken(raw)
	record, err := c.repo.VerificationTokenByHash(ctx, hashed)
	now := c.now().UTC()
	switch {
	case err != nil:
		log.Printf("validVerification: token lookup failed hash=%s... kind=%s err=%v", hashed[:12], kind, err)
		return nil, messages.ErrInvalidToken
	case record.Type != kind:
		log.Printf("validVerification: type mismatch hash=%s... got=%q want=%q", hashed[:12], record.Type, kind)
		return nil, messages.ErrInvalidToken
	case record.UsedAt != nil:
		log.Printf("validVerification: token already used hash=%s... kind=%s used_at=%s", hashed[:12], kind, record.UsedAt)
		return nil, messages.ErrInvalidToken
	case !record.ExpiresAt.After(now):
		log.Printf("validVerification: token expired hash=%s... kind=%s expires=%s now=%s", hashed[:12], kind, record.ExpiresAt, now)
		return nil, messages.ErrInvalidToken
	}
	return record, nil
}

func (c *authController) sendTokenAsync(ctx context.Context, user *models.User, kind models.VerificationTokenType, ttl time.Duration, client ClientInfo) (string, error) {
	raw, err := c.createVerificationToken(ctx, user, kind, ttl, client)
	if err != nil {
		return "", err
	}
	go func() {
		deferredCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("async token delivery panic user_id=%s type=%s panic=%v", user.ID, kind, recovered)
				if c.alerts != nil {
					c.alerts.Alert(context.Background(), "Identity async token delivery panic", "A panic occurred while delivering an auth token.", map[string]string{
						"user_id": user.ID.String(),
						"type":    string(kind),
						"panic":   logValue(recovered),
					})
				}
			}
		}()
		if err := c.deliverToken(deferredCtx, user, kind, raw); err != nil {
			log.Printf("async token delivery failed user_id=%s type=%s error=%v", user.ID, kind, err)
			if c.alerts != nil {
				c.alerts.Alert(context.Background(), "Identity async token delivery failed", err.Error(), map[string]string{
					"user_id": user.ID.String(),
					"type":    string(kind),
				})
			}
		}
	}()
	return raw, nil
}

func logValue(value any) string {
	return strings.TrimSpace(fmt.Sprint(value))
}

func developmentToken(raw string) string {
	if strings.EqualFold(os.Getenv("ENVIRONMENT"), "production") {
		return ""
	}
	return raw
}

func (c *authController) createVerificationToken(ctx context.Context, user *models.User, kind models.VerificationTokenType, ttl time.Duration, client ClientInfo) (string, error) {
	if ttl <= 0 {
		ttl = time.Hour
	}
	raw, err := helpers.GenerateOpaqueToken(32)
	if err != nil {
		return "", err
	}
	now := c.now().UTC()
	_ = c.repo.InvalidateVerificationTokens(ctx, user.ID, kind, now)
	record := &models.VerificationToken{UserID: user.ID, Type: kind, TokenHash: helpers.HashToken(raw), RequestedIP: client.IPAddress, UserAgent: client.UserAgent, ExpiresAt: now.Add(ttl), CreatedAt: now}
	if err := c.repo.CreateVerificationToken(ctx, record); err != nil {
		return "", err
	}
	return raw, nil
}

func (c *authController) deliverToken(ctx context.Context, user *models.User, kind models.VerificationTokenType, raw string) error {
	if c.delivery == nil {
		return messages.ErrProviderUnavailable
	}
	switch kind {
	case models.VerificationTokenEmailVerification:
		return c.delivery.SendVerification(ctx, user.Email, raw)
	case models.VerificationTokenPasswordReset:
		return c.delivery.SendPasswordReset(ctx, user.Email, raw)
	default:
		return c.delivery.SendMagicLink(ctx, user.Email, raw)
	}
}

func (c *authController) issueSession(ctx context.Context, user *models.User, family uuid.UUID, client ClientInfo) (*AuthResult, error) {
	now := c.now().UTC()
	accessTTL := c.config.AccessTokenTTL
	if accessTTL <= 0 {
		accessTTL = 15 * time.Minute
	}
	refreshTTL := c.config.RefreshTokenTTL
	if refreshTTL <= 0 {
		refreshTTL = 30 * 24 * time.Hour
	}
	claims := jwt.MapClaims{"sub": user.ID.String(), "role": string(user.Role), "iss": c.config.JWTIssuer, "iat": now.Unix(), "exp": now.Add(accessTTL).Unix()}
	access, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(c.config.JWTSecret))
	if err != nil {
		return nil, err
	}
	raw, err := helpers.GenerateOpaqueToken(48)
	if err != nil {
		return nil, err
	}
	record := &models.RefreshToken{UserID: user.ID, TokenHash: helpers.HashToken(raw), FamilyID: family, UserAgent: client.UserAgent, IPAddress: client.IPAddress, ExpiresAt: now.Add(refreshTTL), CreatedAt: now}
	if err := c.repo.CreateRefreshToken(ctx, record); err != nil {
		return nil, err
	}
	return &AuthResult{AccessToken: access, RefreshToken: raw, ExpiresIn: int64(accessTTL.Seconds()), User: user}, nil
}
