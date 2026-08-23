package models

type UserRole string

const (
	UserRoleCreator UserRole = "creator"
	UserRoleBuyer   UserRole = "buyer"
	UserRoleAdmin   UserRole = "admin"
)

type UserStatus string

const (
	UserStatusPendingVerification UserStatus = "pending_verification"
	UserStatusActive              UserStatus = "active"
	UserStatusSuspended           UserStatus = "suspended"
	UserStatusDeactivated         UserStatus = "deactivated"
	UserStatusDeleted             UserStatus = "deleted"
)

type VerificationTokenType string

const (
	VerificationTokenEmailVerification VerificationTokenType = "email_verification"
	VerificationTokenPasswordReset     VerificationTokenType = "password_reset"
	VerificationTokenMagicLogin        VerificationTokenType = "magic_login"
	VerificationTokenPhoneOnboarding   VerificationTokenType = "phone_onboarding"
	VerificationTokenAccountRestore    VerificationTokenType = "account_restore"
)

type TransferStatus string

const (
	TransferStatusPending   TransferStatus = "pending"
	TransferStatusCompleted TransferStatus = "completed"
	TransferStatusFailed    TransferStatus = "failed"
)

type MoneyRecordStatus string

const (
	MoneyRecordStatusPending  MoneyRecordStatus = "pending"
	MoneyRecordStatusSuccess  MoneyRecordStatus = "success"
	MoneyRecordStatusFailed   MoneyRecordStatus = "failed"
	MoneyRecordStatusReversed MoneyRecordStatus = "reversed"
)

type EntryDirection string

const (
	EntryDirectionCredit EntryDirection = "credit"
	EntryDirectionDebit  EntryDirection = "debit"
)
