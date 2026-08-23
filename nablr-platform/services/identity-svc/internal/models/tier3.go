package models

import (
	"time"

	"github.com/google/uuid"
)

type Tier3Status string

const (
	Tier3NotStarted       Tier3Status = "not_started"
	Tier3InProgress       Tier3Status = "in_progress"
	Tier3PendingNIN       Tier3Status = "pending_nin"
	Tier3PendingDocuments Tier3Status = "pending_documents"
	Tier3PendingReview    Tier3Status = "pending_review"
	Tier3Approved         Tier3Status = "approved"
	Tier3Rejected         Tier3Status = "rejected"
)

type ComponentStatus string

const (
	ComponentMissing   ComponentStatus = "missing"
	ComponentSubmitted ComponentStatus = "submitted"
	ComponentVerified  ComponentStatus = "verified"
	ComponentRejected  ComponentStatus = "rejected"
	ComponentUploading ComponentStatus = "uploading"
)

type Tier3Application struct {
	ID                     uuid.UUID       `json:"id"`
	UserID                 uuid.UUID       `json:"user_id"`
	Status                 Tier3Status     `json:"status"`
	NINStatus              ComponentStatus `json:"nin_status"`
	AddressStatus          ComponentStatus `json:"address_status"`
	LocationStatus         ComponentStatus `json:"location_status"`
	UtilityDocumentStatus  ComponentStatus `json:"utility_document_status"`
	AddressText            string          `json:"address_text"`
	AddressLine1           string          `json:"address_line_one"`
	AddressLine2           string          `json:"address_line_two"`
	City                   string          `json:"city"`
	State                  string          `json:"state"`
	CountryCode            string          `json:"country_code"`
	PostalCode             string          `json:"postal_code"`
	Latitude               float64         `json:"latitude"`
	Longitude              float64         `json:"longitude"`
	AccuracyMeters         float64         `json:"accuracy_meters"`
	LocationProvider       string          `json:"location_provider"`
	LocationMocked         bool            `json:"location_mocked"`
	LocationCapturedAt     *time.Time      `json:"location_captured_at"`
	LocationConsentAt      *time.Time      `json:"location_consent_at"`
	ReverseGeocodedAddress string          `json:"reverse_geocoded_address"`
	DistanceMeters         *float64        `json:"distance_meters"`
	SubmittedAt            *time.Time      `json:"submitted_at"`
	ApprovedAt             *time.Time      `json:"approved_at"`
	ReviewedAt             *time.Time      `json:"reviewed_at"`
	ReviewedBy             *uuid.UUID      `json:"reviewed_by"`
	RejectionReason        string          `json:"rejection_reason"`
	CreatedAt              time.Time       `json:"created_at"`
	UpdatedAt              time.Time       `json:"updated_at"`
}

func (Tier3Application) TableName() string { return "tier3_applications" }

type Tier3Document struct {
	ID               uuid.UUID       `json:"id"`
	ApplicationID    uuid.UUID       `json:"application_id"`
	UserID           uuid.UUID       `json:"user_id"`
	DocumentType     string          `json:"document_type"`
	OriginalFilename string          `json:"original_filename"`
	IssueDate        time.Time       `json:"issue_date"`
	ObjectKey        string          `json:"-"`
	ContentType      string          `json:"content_type"`
	SizeBytes        int64           `json:"size_bytes"`
	SHA256           string          `json:"-"`
	ETag             string          `json:"-"`
	UploadExpiresAt  time.Time       `json:"upload_expires_at"`
	UploadedAt       *time.Time      `json:"uploaded_at,omitempty"`
	ExtractedName    string          `json:"extracted_name"`
	ExtractedAddress string          `json:"extracted_address"`
	Status           ComponentStatus `json:"status"`
	RejectionReason  string          `json:"rejection_reason"`
	CreatedAt        time.Time       `json:"created_at"`
	ReviewedAt       *time.Time      `json:"reviewed_at"`
}

func (Tier3Document) TableName() string { return "tier3_documents" }
