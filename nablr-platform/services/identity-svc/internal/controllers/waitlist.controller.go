package controllers

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"nabla/identity-svc/internal/alerting"
	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	"nabla/identity-svc/internal/models"
	repo "nabla/identity-svc/internal/repository"
)

type UsernameAvailability struct {
	Username  string `json:"username"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type WaitlistReservation struct {
	Username         string    `json:"username"`
	ReservationToken string    `json:"reservation_token"`
	HoldExpiresAt    time.Time `json:"hold_expires_at"`
}

type WaitlistJoinResult struct {
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expires_at"`
}

type WaitlistController interface {
	CheckUsername(ctx context.Context, rawUsername string) (*UsernameAvailability, error)
	ReserveUsername(ctx context.Context, request models.WaitlistReserveUsernameRequest, client ClientInfo) (*WaitlistReservation, error)
	Join(ctx context.Context, request models.WaitlistJoinRequest) (*WaitlistJoinResult, error)
}

// Handles reserved directly by the platform; never claimable from the
// waitlist form regardless of case.
var reservedUsernames = map[string]bool{
	"admin": true, "administrator": true, "support": true, "help": true,
	"nablr": true, "root": true, "system": true, "api": true, "security": true,
	"billing": true, "official": true, "moderator": true, "staff": true,
	"settings": true, "null": true, "undefined": true, "test": true,
}

type waitlistController struct {
	repo     repo.Store
	delivery WaitlistDelivery
	alerts   alerting.Notifier
	now      func() time.Time
}

func NewWaitlistController(store repo.Store, delivery WaitlistDelivery, alerts alerting.Notifier) WaitlistController {
	return &waitlistController{repo: store, delivery: delivery, alerts: alerts, now: time.Now}
}

func (c *waitlistController) CheckUsername(ctx context.Context, rawUsername string) (*UsernameAvailability, error) {
	username := strings.TrimSpace(rawUsername)
	if !helpers.ValidUsernameFormat(username) {
		return nil, messages.ErrUsernameInvalid
	}
	if reservedUsernames[strings.ToLower(username)] {
		return &UsernameAvailability{Username: username, Available: false, Reason: "reserved"}, nil
	}
	now := c.now().UTC()
	// Lazily flip a lapsed hold/confirmation for this one username so a
	// name someone abandoned or that outlived its 30-day confirmation
	// frees back up without needing a background sweeper.
	_ = c.repo.Waitlist().ExpireStaleByUsername(ctx, username, now)
	taken, err := usernameTaken(ctx, c.repo, username)
	if err != nil {
		return nil, err
	}
	if taken {
		return &UsernameAvailability{Username: username, Available: false, Reason: "taken"}, nil
	}
	return &UsernameAvailability{Username: username, Available: true}, nil
}

func usernameTaken(ctx context.Context, store repo.Store, username string) (bool, error) {
	exists, err := store.Waitlist().UsernameExists(ctx, username)
	if err != nil {
		return false, err
	}
	if exists {
		return true, nil
	}
	if _, err := store.Waitlist().ActiveByUsername(ctx, username); err == nil {
		return true, nil
	} else if !errors.Is(err, repo.ErrNotFound) {
		return false, err
	}
	return false, nil
}

func (c *waitlistController) ReserveUsername(ctx context.Context, request models.WaitlistReserveUsernameRequest, client ClientInfo) (*WaitlistReservation, error) {
	username := strings.TrimSpace(request.Username)
	if !helpers.ValidUsernameFormat(username) {
		return nil, messages.ErrUsernameInvalid
	}
	if reservedUsernames[strings.ToLower(username)] {
		return nil, messages.ErrUsernameReserved
	}
	raw, err := helpers.GenerateOpaqueToken(32)
	if err != nil {
		return nil, err
	}
	now := c.now().UTC()
	entry := &models.WaitlistEntry{
		NablrUsername:        username,
		ReservationTokenHash: helpers.HashToken(raw),
		HeldAt:                now,
		HoldExpiresAt:         now.Add(messages.WaitlistHoldTTL),
		RequestedIP:           client.IPAddress,
		UserAgent:             client.UserAgent,
	}
	err = c.repo.Atomic(ctx, func(tx repo.Store) error {
		_ = tx.Waitlist().ExpireStaleByUsername(ctx, username, now)
		taken, err := usernameTaken(ctx, tx, username)
		if err != nil {
			return err
		}
		if taken {
			return messages.ErrUsernameTaken
		}
		return tx.Waitlist().CreateWaitlistEntry(ctx, entry)
	})
	if err != nil {
		if errors.Is(err, repo.ErrConflict) {
			return nil, messages.ErrUsernameTaken
		}
		return nil, err
	}
	return &WaitlistReservation{Username: entry.NablrUsername, ReservationToken: raw, HoldExpiresAt: entry.HoldExpiresAt}, nil
}

func (c *waitlistController) Join(ctx context.Context, request models.WaitlistJoinRequest) (*WaitlistJoinResult, error) {
	hash := helpers.HashToken(request.ReservationToken)
	entry, err := c.repo.Waitlist().ByReservationTokenHash(ctx, hash)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, messages.ErrInvalidToken
		}
		return nil, err
	}
	now := c.now().UTC()
	if entry.Status != models.WaitlistStatusHeld {
		return nil, messages.ErrWaitlistHoldExpired
	}
	if !entry.HoldExpiresAt.After(now) {
		_ = c.repo.Waitlist().ExpireWaitlistEntry(ctx, entry.ID, now)
		return nil, messages.ErrWaitlistHoldExpired
	}
	email := strings.ToLower(strings.TrimSpace(request.Email))
	if existing, err := c.repo.Waitlist().ConfirmedWaitlistEntryByEmail(ctx, email); err == nil {
		if existing.ID != entry.ID {
			return nil, messages.ErrWaitlistEmailExists
		}
	} else if !errors.Is(err, repo.ErrNotFound) {
		return nil, err
	}
	expiresAt := now.Add(messages.WaitlistReservationTTL)
	confirmed, err := c.repo.Waitlist().ConfirmWaitlistEntry(ctx, entry.ID, email, now, expiresAt)
	if err != nil {
		if errors.Is(err, repo.ErrConflict) {
			return nil, messages.ErrWaitlistEmailExists
		}
		if errors.Is(err, repo.ErrNotFound) {
			return nil, messages.ErrWaitlistHoldExpired
		}
		return nil, err
	}
	c.sendConfirmationAsync(confirmed.Email, confirmed.NablrUsername, *confirmed.ExpiresAt)
	return &WaitlistJoinResult{Username: confirmed.NablrUsername, Email: confirmed.Email, ExpiresAt: *confirmed.ExpiresAt}, nil
}

func (c *waitlistController) sendConfirmationAsync(email, username string, expiresAt time.Time) {
	if c.delivery == nil {
		return
	}
	go func() {
		deferredCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("waitlist confirmation email panic username=%s panic=%v", username, recovered)
				if c.alerts != nil {
					c.alerts.Alert(context.Background(), "Waitlist confirmation email panic", "A panic occurred while sending a waitlist confirmation email.", map[string]string{"username": username})
				}
			}
		}()
		if err := c.delivery.SendConfirmation(deferredCtx, email, username, expiresAt); err != nil {
			log.Printf("waitlist confirmation email failed username=%s error=%v", username, err)
			if c.alerts != nil {
				c.alerts.Alert(context.Background(), "Waitlist confirmation email failed", err.Error(), map[string]string{"username": username})
			}
		}
	}()
}

var _ WaitlistController = (*waitlistController)(nil)
