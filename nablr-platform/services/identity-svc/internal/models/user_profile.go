package models

import (
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID              uuid.UUID  `json:"id"`
	Email           string     `json:"email"`
	PhoneNumber     string     `json:"phone_number"`
	NablrUsername   string     `json:"nablr_username,omitempty"`
	AccountNumber   string     `json:"account_number,omitempty"`
	PasswordHash    *string    `json:"-"`
	PasswordSet     bool       `json:"password_set"`
	PhoneVerified   bool       `json:"phone_verified"`
	PhoneVerifiedAt *time.Time `json:"phone_verified_at,omitempty"`
	NinStatus       string     `json:"nin_status,omitempty"`
	NinVerified     bool       `json:"nin_verified"`
	FacialStatus    string     `json:"facial_status,omitempty"`
	FacialVerified  bool       `json:"facial_verified"`
	Role            UserRole   `json:"role"`
	Status          UserStatus `json:"status"`
	EmailVerifiedAt *time.Time `json:"email_verified_at,omitempty"`
	LastLoginAt     *time.Time `json:"last_login_at,omitempty"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
	AvatarURL       string     `json:"avatar_url,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (User) TableName() string { return "users" }
