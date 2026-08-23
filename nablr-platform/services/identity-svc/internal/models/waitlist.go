package models

import (
	"time"

	"github.com/google/uuid"
)

const (
	WaitlistStatusHeld      = "held"
	WaitlistStatusConfirmed = "confirmed"
	WaitlistStatusExpired   = "expired"
	WaitlistStatusOnboarded = "onboarded"
)

type WaitlistEntry struct {
	ID                   uuid.UUID  `json:"id"`
	NablrUsername        string     `json:"nablr_username"`
	Email                string     `json:"email,omitempty"`
	ReservationTokenHash string     `json:"-"`
	Status               string     `json:"status"`
	HeldAt               time.Time  `json:"held_at"`
	HoldExpiresAt        time.Time  `json:"hold_expires_at"`
	ConfirmedAt          *time.Time `json:"confirmed_at,omitempty"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	ClaimedUserID        *uuid.UUID `json:"claimed_user_id,omitempty"`
	ClaimedAt            *time.Time `json:"claimed_at,omitempty"`
	RequestedIP          string     `json:"-"`
	UserAgent            string     `json:"-"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

func (WaitlistEntry) TableName() string { return "waitlist_entries" }
