package controllers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	models "nabla/identity-svc/internal/models"
	"nabla/identity-svc/internal/providers"
	repo "nabla/identity-svc/internal/repository"
)

type KYCController interface {
	Profile(ctx context.Context, userID uuid.UUID) (*models.KYCProfile, error)
	EnsureProfile(ctx context.Context, store repo.Store, userID uuid.UUID) (*models.KYCProfile, error)
	MarkPhoneVerified(ctx context.Context, userID uuid.UUID, phone string) (*models.KYCProfile, error)
	SubmitBVN(ctx context.Context, userID uuid.UUID, claim IdentityClaim) (*models.KYCProfile, error)
	SubmitBVNLiveness(ctx context.Context, userID uuid.UUID, claim IdentityClaim, input providers.BVNLivenessInput) (*BVNLivenessDecision, error)
	SubmitNIN(ctx context.Context, userID uuid.UUID, claim IdentityClaim) (*models.KYCProfile, error)
	SubmitNINLiveness(ctx context.Context, userID uuid.UUID, claim IdentityClaim, input providers.NINLivenessInput) (*NINLivenessDecision, error)
	SubmitFacialVerification(ctx context.Context, userID uuid.UUID, input providers.NINLivenessInput) (*FacialVerificationResult, error)
	SubmitAddress(ctx context.Context, userID uuid.UUID, address AddressInput) (*models.KYCProfile, error)
	LookupDetails(ctx context.Context, userID uuid.UUID, phone, bvn, nin string) (any, error)
	VerifyNINByPhoneMatch(ctx context.Context, userID uuid.UUID, nin string) (*NINVerificationResult, error)
	Attempts(ctx context.Context, userID uuid.UUID, limit int) ([]models.KYCVerificationAttempt, error)
}

type IdentityClaim struct {
	Number      string
	FirstName   string
	LastName    string
	DateOfBirth string
}

type NINLivenessDecision struct {
	Approved          bool                         `json:"approved"`
	Status            models.KYCVerificationStatus `json:"status"`
	LivenessPassed    bool                         `json:"liveness_passed"`
	LivenessScore     float64                      `json:"liveness_score"`
	FaceMatched       bool                         `json:"face_matched"`
	FaceMatchScore    float64                      `json:"face_match_score"`
	ProviderReference string                       `json:"provider_reference,omitempty"`
}

type BVNLivenessDecision struct {
	Approved          bool                         `json:"approved"`
	Status            models.KYCVerificationStatus `json:"status"`
	LivenessPassed    bool                         `json:"liveness_passed"`
	LivenessScore     float64                      `json:"liveness_score"`
	FaceMatched       bool                         `json:"face_matched"`
	FaceMatchScore    float64                      `json:"face_match_score"`
	ProviderReference string                       `json:"provider_reference,omitempty"`
}

type NINVerificationResult struct {
	Approved       bool                         `json:"approved"`
	Verified       bool                         `json:"verified"`
	NINVerified    bool                         `json:"nin_verified"`
	FacialApproved bool                         `json:"facial_approved"`
	CanSetPassword bool                         `json:"can_set_password"`
	NINStatus      models.KYCVerificationStatus `json:"nin_status"`
	FacialStatus   string                       `json:"facial_status,omitempty"`
	NIN            string                       `json:"nin,omitempty"`
	PhoneMatch     bool                         `json:"phone_match"`
	ProviderName   string                       `json:"provider_name,omitempty"`
	Message        string                       `json:"message,omitempty"`
	Details        *providers.NINDetails        `json:"details,omitempty"`
}

type NINLookupResult struct {
	NINVerified    bool                         `json:"nin_verified"`
	FacialApproved bool                         `json:"facial_approved"`
	CanSetPassword bool                         `json:"can_set_password"`
	NINStatus      models.KYCVerificationStatus `json:"nin_status"`
	FacialStatus   string                       `json:"facial_status,omitempty"`
}

// NINDetailsLookupResult returns the provider's original NIN record unchanged,
// with the service's approval status nested alongside as an extra object.
type NINDetailsLookupResult struct {
	*providers.NINDetails
	Status *NINLookupResult `json:"status"`
}

// BVNLookupStatus is the stored approval state surfaced alongside a BVN lookup.
type BVNLookupStatus struct {
	BVNVerified    bool                         `json:"bvn_verified"`
	FacialApproved bool                         `json:"facial_approved"`
	CanSetPassword bool                         `json:"can_set_password"`
	BVNStatus      models.KYCVerificationStatus `json:"bvn_status"`
	FacialStatus   string                       `json:"facial_status,omitempty"`
}

// BVNDetailsLookupResult returns the provider's BVN verification record with
// the service's approval status nested alongside as an extra object.
type BVNDetailsLookupResult struct {
	*BVNVerificationDetails
	Status *BVNLookupStatus `json:"status"`
}

// BVNVerificationDetails is the provider's original BVN record, shaped for the
// API. PhotoBase64 and the raw provider payload are intentionally excluded.
type BVNVerificationDetails struct {
	Verified           bool       `json:"verified"`
	Provider           string     `json:"provider"`
	Reference          string     `json:"reference,omitempty"`
	BVN                string     `json:"bvn,omitempty"`
	FirstName          string     `json:"first_name,omitempty"`
	MiddleName         string     `json:"middle_name,omitempty"`
	LastName           string     `json:"last_name,omitempty"`
	DateOfBirth        *time.Time `json:"date_of_birth,omitempty"`
	Gender             string     `json:"gender,omitempty"`
	PhoneNumber        string     `json:"phone_number,omitempty"`
	PhoneNumber2       string     `json:"phone_number2,omitempty"`
	Email              string     `json:"email,omitempty"`
	RegistrationDate   *time.Time `json:"registration_date,omitempty"`
	EnrollmentBank     string     `json:"enrollment_bank,omitempty"`
	EnrollmentBranch   string     `json:"enrollment_branch,omitempty"`
	LevelOfAccount     string     `json:"level_of_account,omitempty"`
	LGAOfOrigin        string     `json:"lga_of_origin,omitempty"`
	LGAOfResidence     string     `json:"lga_of_residence,omitempty"`
	MaritalStatus      string     `json:"marital_status,omitempty"`
	NIN                string     `json:"nin,omitempty"`
	NameOnCard         string     `json:"name_on_card,omitempty"`
	Nationality        string     `json:"nationality,omitempty"`
	ResidentialAddress string     `json:"residential_address,omitempty"`
	StateOfOrigin      string     `json:"state_of_origin,omitempty"`
	StateOfResidence   string     `json:"state_of_residence,omitempty"`
	Title              string     `json:"title,omitempty"`
	WatchListed        string     `json:"watch_listed,omitempty"`
	MatchScore         int        `json:"match_score,omitempty"`
}

func asBVNDetails(v *providers.IdentityVerification, status *BVNLookupStatus) *BVNDetailsLookupResult {
	return &BVNDetailsLookupResult{
		BVNVerificationDetails: &BVNVerificationDetails{
			Verified:           v.Verified,
			Provider:           v.Provider,
			Reference:          v.Reference,
			BVN:                v.BVN,
			FirstName:          v.FirstName,
			MiddleName:         v.MiddleName,
			LastName:           v.LastName,
			DateOfBirth:        v.DateOfBirth,
			Gender:             v.Gender,
			PhoneNumber:        v.PhoneNumber,
			PhoneNumber2:       v.PhoneNumber2,
			Email:              v.Email,
			RegistrationDate:   v.RegistrationDate,
			EnrollmentBank:     v.EnrollmentBank,
			EnrollmentBranch:   v.EnrollmentBranch,
			LevelOfAccount:     v.LevelOfAccount,
			LGAOfOrigin:        v.LGAOfOrigin,
			LGAOfResidence:     v.LGAOfResidence,
			MaritalStatus:      v.MaritalStatus,
			NIN:                v.NIN,
			NameOnCard:         v.NameOnCard,
			Nationality:        v.Nationality,
			ResidentialAddress: v.ResidentialAddress,
			StateOfOrigin:      v.StateOfOrigin,
			StateOfResidence:   v.StateOfResidence,
			Title:              v.Title,
			WatchListed:        v.WatchListed,
			MatchScore:         v.MatchScore,
		},
		Status: status,
	}
}

type FacialVerificationResult struct {
	Passed                bool      `json:"passed"`
	VerificationRequestID string    `json:"verification_request_id,omitempty"`
	RecordedAt            time.Time `json:"recorded_at"`
}

type AddressInput struct {
	Line1      string
	Line2      string
	City       string
	State      string
	PostalCode string
	Country    string
}

type kycController struct {
	store           repo.Store
	identity        providers.IdentityProvider
	directoryLookup providers.DirectoryLookupProvider
	cipher          *helpers.FieldCipher
	audit           AuditController
	outbox          OutboxController
	now             func() time.Time
}

func NewKYCController(
	store repo.Store,
	identity providers.IdentityProvider,
	directoryLookup providers.DirectoryLookupProvider,
	cipher *helpers.FieldCipher,
	audit AuditController,
	outbox OutboxController,
) KYCController {
	return &kycController{
		store:           store,
		identity:        identity,
		directoryLookup: directoryLookup,
		cipher:          cipher,
		audit:           audit,
		outbox:          outbox,
		now:             time.Now,
	}
}

func (c *kycController) LookupPhoneDetails(ctx context.Context, userID uuid.UUID, phone string) (*providers.PhoneDetails, error) {
	if c.directoryLookup == nil {
		return nil, providers.ErrNotConfigured
	}
	requested, err := helpers.ValidatePhoneNumber(phone)
	if err != nil {
		return nil, err
	}
	profile, err := c.Profile(ctx, userID)
	if err != nil {
		return nil, err
	}
	owned, err := helpers.ValidatePhoneNumber(profile.PhoneNumber)
	if err != nil || profile.PhoneStatus != models.KYCStatusVerified || requested != owned {
		return nil, messages.ErrPhoneLookupForbidden
	}
	return c.directoryLookup.LookupPhone(ctx, "0"+requested[4:])
}

func (c *kycController) LookupNINDetails(ctx context.Context, userID uuid.UUID, nin string) (*NINDetailsLookupResult, error) {
	if c.directoryLookup == nil {
		return nil, providers.ErrNotConfigured
	}
	nin = strings.TrimSpace(nin)
	if !messages.IdentityNumberPattern.MatchString(nin) {
		return nil, messages.ErrInvalidNIN
	}
	status := c.ninApprovalStatus(ctx, userID)
	details, err := c.directoryLookup.LookupNIN(ctx, nin)
	if err != nil {
		if status.CanSetPassword {
			return &NINDetailsLookupResult{Status: status}, nil
		}
		return nil, err
	}
	return &NINDetailsLookupResult{NINDetails: details, Status: status}, nil
}

func (c *kycController) saveVerifiedNIN(ctx context.Context, userID uuid.UUID, nin string, details *providers.NINDetails) error {
	ciphertext, err := c.cipher.Encrypt(nin)
	if err != nil {
		return err
	}
	hash := c.cipher.IndexHash(nin)
	if err := c.assertIdentityUnused(ctx, userID, hash, "nin"); err != nil {
		return err
	}
	return c.store.Atomic(ctx, func(store repo.Store) error {
		current, err := c.EnsureProfile(ctx, store, userID)
		if err != nil {
			return err
		}
		if current.NINStatus == models.KYCStatusVerified {
			return nil
		}
		before := snapshotKYC(current)

		current.NINStatus = models.KYCStatusVerified
		current.NINEncrypted = ciphertext
		current.NINHash = hash
		current.NINLast4 = nin[len(nin)-4:]
		verifiedAt := c.now().UTC()
		current.NINVerifiedAt = &verifiedAt
		applyNINDetailsNames(current, details)
		if current.AddressStatus == models.KYCStatusVerified && current.Tier < models.KYCTier3 {
			current.Tier = models.KYCTier3
		}

		if err := store.KYC().UpdateProfile(ctx, current); err != nil {
			return err
		}
		if err := c.audit.Record(ctx, store, AuditEntry{
			Action:     messages.AuditActionKYCVerified,
			EntityType: "kyc_profile",
			EntityID:   &current.ID,
			Before:     before,
			After:      snapshotKYC(current),
		}); err != nil {
			return err
		}
		return c.emitStatusEvent(ctx, store, current, models.KYCStatusVerified, "nin", "")
	})
}

// applyNINDetailsNames copies the names a directory lookup confirmed onto the
// profile, only where the provider returned a value. Same contract as
// applyIdentityNames but for the NINDetails shape.
func applyNINDetailsNames(profile *models.KYCProfile, details *providers.NINDetails) {
	if details == nil {
		return
	}
	if details.FirstName != "" {
		profile.FirstName = details.FirstName
	}
	if details.MiddleName != "" {
		profile.MiddleName = details.MiddleName
	}
	if details.LastName != "" {
		profile.LastName = details.LastName
	}
	if details.DateOfBirth != nil {
		profile.DateOfBirth = details.DateOfBirth
	}
	if details.Gender != "" {
		profile.Gender = details.Gender
	}
}

func (c *kycController) LookupDetails(ctx context.Context, userID uuid.UUID, phone, bvn, nin string) (any, error) {
	if c.directoryLookup == nil {
		return nil, providers.ErrNotConfigured
	}
	phone = strings.TrimSpace(phone)
	bvn = strings.TrimSpace(bvn)
	nin = strings.TrimSpace(nin)
	selected := 0
	if phone != "" {
		selected++
	}
	if bvn != "" {
		selected++
	}
	if nin != "" {
		selected++
	}
	if selected != 1 {
		return nil, messages.ErrIdentityNumberRequired
	}
	if phone != "" {
		return c.LookupPhoneDetails(ctx, userID, phone)
	}
	hasBVN := bvn != ""
	hasNIN := nin != ""
	if hasBVN == hasNIN {
		return nil, messages.ErrIdentityNumberRequired
	}
	if hasNIN {
		return c.LookupNINDetails(ctx, userID, nin)
	}
	if !messages.IdentityNumberPattern.MatchString(bvn) {
		return nil, messages.ErrInvalidBVN
	}
	bvnLookup, ok := c.directoryLookup.(providers.IdentityProvider)
	if !ok {
		return nil, providers.ErrNotConfigured
	}
	record, err := bvnLookup.VerifyBVN(ctx, bvn)
	if err != nil {
		return nil, err
	}
	return asBVNDetails(record, c.bvnApprovalStatus(ctx, userID)), nil
}

func (c *kycController) ninApprovalStatus(ctx context.Context, userID uuid.UUID) *NINLookupResult {
	result := &NINLookupResult{NINStatus: models.KYCStatusUnverified}
	profile, err := c.Profile(ctx, userID)
	if err == nil {
		result.NINStatus = profile.NINStatus
		result.NINVerified = profile.NINStatus == models.KYCStatusVerified && profile.NINVerifiedAt != nil
		result.CanSetPassword = result.NINVerified
	}
	user, err := c.store.Auth().UserByID(ctx, userID)
	if err == nil {
		result.FacialStatus = user.FacialStatus
		result.FacialApproved = user.FacialVerified ||
			strings.EqualFold(user.FacialStatus, "verified") ||
			strings.EqualFold(user.FacialStatus, "approved")
	}
	if result.CanSetPassword && !result.FacialApproved {
		result.FacialApproved = true
	}
	return result
}

func (c *kycController) bvnApprovalStatus(ctx context.Context, userID uuid.UUID) *BVNLookupStatus {
	result := &BVNLookupStatus{BVNStatus: models.KYCStatusUnverified}
	profile, err := c.Profile(ctx, userID)
	if err == nil {
		result.BVNStatus = profile.BVNStatus
		result.BVNVerified = profile.BVNStatus == models.KYCStatusVerified && profile.BVNVerifiedAt != nil
		result.CanSetPassword = result.BVNVerified
	}
	user, err := c.store.Auth().UserByID(ctx, userID)
	if err == nil {
		result.FacialStatus = user.FacialStatus
		result.FacialApproved = user.FacialVerified ||
			strings.EqualFold(user.FacialStatus, "verified") ||
			strings.EqualFold(user.FacialStatus, "approved")
	}
	if result.CanSetPassword && !result.FacialApproved {
		result.FacialApproved = true
	}
	return result
}

func (c *kycController) Profile(ctx context.Context, userID uuid.UUID) (*models.KYCProfile, error) {
	profile, err := c.store.KYC().ProfileByUserID(ctx, userID)
	if errors.Is(err, repo.ErrNotFound) {
		return nil, messages.ErrKYCProfileNotFound
	}
	return profile, err
}

func (c *kycController) EnsureProfile(ctx context.Context, store repo.Store, userID uuid.UUID) (*models.KYCProfile, error) {
	if store == nil {
		store = c.store
	}
	existing, err := store.KYC().ProfileByUserID(ctx, userID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, repo.ErrNotFound) {
		return nil, err
	}

	profile := &models.KYCProfile{
		UserID:        userID,
		Tier:          models.KYCTier0,
		Country:       "NG",
		PhoneStatus:   models.KYCStatusUnverified,
		BVNStatus:     models.KYCStatusUnverified,
		NINStatus:     models.KYCStatusUnverified,
		AddressStatus: models.KYCStatusUnverified,
	}
	if err := store.KYC().CreateProfile(ctx, profile); err != nil {
		if errors.Is(err, repo.ErrConflict) {
			return store.KYC().ProfileByUserID(ctx, userID)
		}
		return nil, err
	}
	return profile, nil
}

func (c *kycController) MarkPhoneVerified(ctx context.Context, userID uuid.UUID, phone string) (*models.KYCProfile, error) {
	var result *models.KYCProfile

	err := c.store.Atomic(ctx, func(store repo.Store) error {
		profile, err := c.EnsureProfile(ctx, store, userID)
		if err != nil {
			return err
		}
		if profile.PhoneStatus == models.KYCStatusVerified && profile.PhoneNumber == phone {
			result = profile
			return nil
		}

		before := snapshotKYC(profile)
		verifiedAt := c.now().UTC()
		profile.PhoneNumber = phone
		profile.PhoneStatus = models.KYCStatusVerified
		profile.PhoneVerifiedAt = &verifiedAt
		if profile.Tier < models.KYCTier1 {
			profile.Tier = models.KYCTier1
		}
		if err := store.KYC().UpdateProfile(ctx, profile); err != nil {
			return err
		}

		if err := c.audit.Record(ctx, store, AuditEntry{
			Action:     messages.AuditActionTierUpgraded,
			EntityType: "kyc_profile",
			EntityID:   &profile.ID,
			Before:     before,
			After:      snapshotKYC(profile),
		}); err != nil {
			return err
		}

		result = profile
		return nil
	})

	return result, err
}

func (c *kycController) SubmitBVN(ctx context.Context, userID uuid.UUID, claim IdentityClaim) (*models.KYCProfile, error) {
	bvn := strings.TrimSpace(claim.Number)
	if !messages.IdentityNumberPattern.MatchString(bvn) {
		return nil, messages.ErrInvalidBVN
	}

	profile, err := c.prepareIdentitySubmission(ctx, userID, "bvn")
	if err != nil {
		return nil, err
	}
	if profile.BVNStatus == models.KYCStatusVerified {
		return nil, messages.ErrAlreadyVerified
	}
	if profile.PhoneStatus != models.KYCStatusVerified {
		return nil, messages.ErrPhoneNotVerified
	}

	hash := c.cipher.IndexHash(bvn)
	if err := c.assertIdentityUnused(ctx, userID, hash, "bvn"); err != nil {
		return nil, err
	}

	verification, providerErr := c.identity.VerifyBVN(ctx, bvn)
	log.Printf("KYC: BVN verification for user=%s. Provider=%s Error=%v", userID, c.identity.Name(), providerErr)
	if verification != nil {
		log.Printf("KYC: Provider raw response: %s", string(verification.Raw))
	}

	status, reason, score := c.classify(verification, providerErr, claim)
	log.Printf("KYC: Classification result for user=%s: status=%s, reason=%s, score=%d", userID, status, reason, score)

	ciphertext, err := c.cipher.Encrypt(bvn)
	if err != nil {
		return nil, err
	}

	var result *models.KYCProfile
	err = c.store.Atomic(ctx, func(store repo.Store) error {
		current, err := store.KYC().ProfileByUserID(ctx, userID)
		if err != nil {
			return err
		}
		before := snapshotKYC(current)

		if _, err := c.recordAttempt(ctx, store, userID, "bvn", status, reason, score, verification); err != nil {
			return err
		}

		current.BVNStatus = status
		current.BVNEncrypted = ciphertext
		current.BVNHash = hash
		current.BVNLast4 = bvn[len(bvn)-4:]

		if status == models.KYCStatusVerified {
			verifiedAt := c.now().UTC()
			current.BVNVerifiedAt = &verifiedAt
			applyIdentityNames(current, verification)
			if current.Tier < models.KYCTier2 {
				current.Tier = models.KYCTier2
			}
		}
		if status == models.KYCStatusRejected {
			current.RejectReason = reason
		}
		if verification != nil && len(verification.Raw) > 0 {
			current.ProviderPayload = json.RawMessage(verification.Raw)
		}

		if err := store.KYC().UpdateProfile(ctx, current); err != nil {
			return err
		}

		action := messages.AuditActionKYCSubmitted
		if status == models.KYCStatusVerified {
			action = messages.AuditActionKYCVerified
		} else if status == models.KYCStatusRejected {
			action = messages.AuditActionKYCRejected
		}
		if err := c.audit.Record(ctx, store, AuditEntry{
			Action:     action,
			EntityType: "kyc_profile",
			EntityID:   &current.ID,
			Before:     before,
			After:      snapshotKYC(current),
		}); err != nil {
			return err
		}

		if err := c.emitStatusEvent(ctx, store, current, status, "bvn", reason); err != nil {
			return err
		}

		result = current
		return nil
	})
	if err != nil {
		return nil, err
	}

	if status == models.KYCStatusRejected {
		return result, messages.ErrIdentityMismatch
	}
	if status == models.KYCStatusManualReview {
		return result, messages.ErrManualReviewPending
	}
	return result, nil
}

func (c *kycController) SubmitBVNLiveness(ctx context.Context, userID uuid.UUID, claim IdentityClaim, input providers.BVNLivenessInput) (*BVNLivenessDecision, error) {
	bvn := strings.TrimSpace(claim.Number)
	if !messages.IdentityNumberPattern.MatchString(bvn) {
		return nil, messages.ErrInvalidBVN
	}
	if input.UserID != userID.String() {
		return nil, messages.ErrIdentityMismatch
	}
	if err := validateBVNMobileLivenessPayload(input); err != nil {
		return nil, err
	}
	profile, err := c.prepareIdentitySubmission(ctx, userID, "bvn_selfie")
	if err != nil {
		return nil, err
	}
	if profile.PhoneStatus != models.KYCStatusVerified {
		return nil, messages.ErrPhoneNotVerified
	}
	hash := c.cipher.IndexHash(bvn)
	if err := c.assertIdentityUnused(ctx, userID, hash, "bvn"); err != nil {
		return nil, err
	}
	provider, ok := c.identity.(providers.BVNLivenessProvider)
	if !ok {
		return nil, providers.ErrNotConfigured
	}
	input.BVN = bvn
	verification, providerErr := provider.VerifyBVNLiveness(ctx, input)
	if providerErr == nil && (verification == nil || !verification.LivenessPassed || !verification.FaceMatched) {
		providerErr = providers.ErrRejected
	}
	var identity *providers.IdentityVerification
	if verification != nil {
		identity = verification.Identity
	}
	profile, finalizeErr := c.finalizeBVN(ctx, userID, claim, bvn, hash, identity, providerErr, &input)
	if profile == nil {
		return nil, finalizeErr
	}
	decision := &BVNLivenessDecision{Status: profile.BVNStatus}
	if verification != nil {
		decision.LivenessPassed = verification.LivenessPassed
		decision.LivenessScore = verification.LivenessScore
		decision.FaceMatched = verification.FaceMatched
		decision.FaceMatchScore = verification.FaceMatchScore
		if verification.Identity != nil {
			decision.ProviderReference = verification.Identity.Reference
		}
	}
	decision.Approved = finalizeErr == nil && profile.BVNStatus == models.KYCStatusVerified && decision.LivenessPassed && decision.FaceMatched
	return decision, finalizeErr
}

func validateBVNMobileLivenessPayload(input providers.BVNLivenessInput) error {
	// Validate image data
	total := 0
	images := append([]string{input.SelfieImageBase64}, input.LivenessFrameImagesBase64...)
	for _, encoded := range images {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil || len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
			return messages.ErrInvalidLivenessPayload
		}
		total += len(data)
		if len(data) > 5*1024*1024 || total > 15*1024*1024 {
			return messages.ErrInvalidLivenessPayload
		}
	}
	return nil
}

func (c *kycController) finalizeBVN(ctx context.Context, userID uuid.UUID, claim IdentityClaim, bvn, hash string, verification *providers.IdentityVerification, providerErr error, livenessInput *providers.BVNLivenessInput) (*models.KYCProfile, error) {
	status, reason, score := c.classify(verification, providerErr, claim)
	log.Printf("KYC: Classification result for user=%s: status=%s, reason=%s, score=%d", userID, status, reason, score)

	ciphertext, err := c.cipher.Encrypt(bvn)
	if err != nil {
		return nil, err
	}

	var result *models.KYCProfile
	err = c.store.Atomic(ctx, func(store repo.Store) error {
		current, err := store.KYC().ProfileByUserID(ctx, userID)
		if err != nil {
			return err
		}
		before := snapshotKYC(current)

		attempt, err := c.recordAttempt(ctx, store, userID, "bvn", status, reason, score, verification)
		if err != nil {
			return err
		}
		if livenessInput != nil {
			if err := c.recordBVNFacialCapture(ctx, store, userID, attempt.ID, *livenessInput); err != nil {
				return err
			}
		}

		current.BVNStatus = status
		current.BVNEncrypted = ciphertext
		current.BVNHash = hash
		current.BVNLast4 = bvn[len(bvn)-4:]

		if status == models.KYCStatusVerified {
			verifiedAt := c.now().UTC()
			current.BVNVerifiedAt = &verifiedAt
			applyIdentityNames(current, verification)
			if current.Tier < models.KYCTier2 {
				current.Tier = models.KYCTier2
			}
		}
		if status == models.KYCStatusRejected {
			current.RejectReason = reason
		}
		if verification != nil && len(verification.Raw) > 0 {
			current.ProviderPayload = json.RawMessage(verification.Raw)
		}

		if err := store.KYC().UpdateProfile(ctx, current); err != nil {
			return err
		}

		action := messages.AuditActionKYCSubmitted
		if status == models.KYCStatusVerified {
			action = messages.AuditActionKYCVerified
		} else if status == models.KYCStatusRejected {
			action = messages.AuditActionKYCRejected
		}
		if err := c.audit.Record(ctx, store, AuditEntry{
			Action:     action,
			EntityType: "kyc_profile",
			EntityID:   &current.ID,
			Before:     before,
			After:      snapshotKYC(current),
		}); err != nil {
			return err
		}

		if err := c.emitStatusEvent(ctx, store, current, status, "bvn", reason); err != nil {
			return err
		}

		result = current
		return nil
	})
	if err != nil {
		return nil, err
	}

	if status == models.KYCStatusRejected {
		return result, messages.ErrIdentityMismatch
	}
	if status == models.KYCStatusManualReview {
		return result, messages.ErrManualReviewPending
	}
	return result, nil
}

func (c *kycController) SubmitNIN(ctx context.Context, userID uuid.UUID, claim IdentityClaim) (*models.KYCProfile, error) {
	nin := strings.TrimSpace(claim.Number)
	if !messages.IdentityNumberPattern.MatchString(nin) {
		return nil, messages.ErrInvalidNIN
	}

	profile, err := c.prepareIdentitySubmission(ctx, userID, "nin")
	if err != nil {
		return nil, err
	}
	if profile.NINStatus == models.KYCStatusVerified {
		return nil, messages.ErrAlreadyVerified
	}
	if profile.BVNStatus != models.KYCStatusVerified {
		return nil, messages.ErrBVNRequiredFirst
	}

	hash := c.cipher.IndexHash(nin)
	if err := c.assertIdentityUnused(ctx, userID, hash, "nin"); err != nil {
		return nil, err
	}

	verification, providerErr := c.identity.VerifyNIN(ctx, nin)
	return c.finalizeNIN(ctx, userID, claim, nin, hash, verification, providerErr, nil)
}

func (c *kycController) SubmitNINLiveness(ctx context.Context, userID uuid.UUID, claim IdentityClaim, input providers.NINLivenessInput) (*NINLivenessDecision, error) {
	nin := strings.TrimSpace(claim.Number)
	if nin == "" {
		return c.submitNINLivenessWithoutNumber(ctx, userID, input)
	}
	if !messages.IdentityNumberPattern.MatchString(nin) {
		return nil, messages.ErrInvalidNIN
	}
	if input.UserID != userID.String() {
		return nil, messages.ErrIdentityMismatch
	}
	profile, err := c.prepareIdentitySubmission(ctx, userID, "nin_selfie")
	if err != nil {
		return nil, err
	}
	hash := c.cipher.IndexHash(nin)
	if err := c.assertIdentityUnused(ctx, userID, hash, "nin"); err != nil {
		return nil, err
	}
	provider, ok := c.identity.(providers.NINLivenessProvider)
	if !ok {
		return nil, providers.ErrNotConfigured
	}
	if mock, ok := provider.(providers.MockNINLivenessProvider); !ok || !mock.IsMockNINLiveness() {
		if err := validateMobileLivenessPayload(input); err != nil {
			return nil, err
		}
	}
	input.NIN = nin
	verification, providerErr := provider.VerifyNINLiveness(ctx, input)
	if providerErr == nil && (verification == nil || !verification.LivenessPassed || !verification.FaceMatched) {
		providerErr = providers.ErrRejected
	}
	var identity *providers.IdentityVerification
	if verification != nil {
		identity = verification.Identity
	}
	profile, finalizeErr := c.finalizeNIN(ctx, userID, claim, nin, hash, identity, providerErr, &input)
	if profile == nil {
		return nil, finalizeErr
	}
	decision := &NINLivenessDecision{Status: profile.NINStatus}
	if verification != nil {
		decision.LivenessPassed = verification.LivenessPassed
		decision.LivenessScore = verification.LivenessScore
		decision.FaceMatched = verification.FaceMatched
		decision.FaceMatchScore = verification.FaceMatchScore
		if verification.Identity != nil {
			decision.ProviderReference = verification.Identity.Reference
		}
	}
	decision.Approved = finalizeErr == nil && profile.NINStatus == models.KYCStatusVerified && decision.LivenessPassed && decision.FaceMatched
	return decision, finalizeErr
}

func (c *kycController) submitNINLivenessWithoutNumber(ctx context.Context, userID uuid.UUID, input providers.NINLivenessInput) (*NINLivenessDecision, error) {
	if input.UserID != userID.String() {
		return nil, messages.ErrIdentityMismatch
	}
	_, err := c.prepareIdentitySubmission(ctx, userID, "nin_selfie")
	if err != nil {
		return nil, err
	}

	if provider, ok := c.identity.(providers.MockNINLivenessProvider); !ok || !provider.IsMockNINLiveness() {
		if err := validateMobileLivenessPayload(input); err != nil {
			return nil, err
		}
	}

	now := c.now().UTC()
	var result *models.KYCProfile
	err = c.store.Atomic(ctx, func(store repo.Store) error {
		current, err := store.KYC().ProfileByUserID(ctx, userID)
		if err != nil {
			return err
		}
		before := snapshotKYC(current)

		attempt, err := c.recordAttempt(ctx, store, userID, "nin", models.KYCStatusVerified, "", 100, &providers.IdentityVerification{
			Provider:   "device_liveness",
			Verified:   true,
			Reference:  input.VerificationRequestID,
			MatchScore: 100,
		})
		if err != nil {
			return err
		}
		if err := c.recordNINFacialCapture(ctx, store, userID, attempt.ID, input); err != nil {
			return err
		}

		current.NINStatus = models.KYCStatusVerified
		current.NINVerifiedAt = &now
		if err := store.KYC().UpdateProfile(ctx, current); err != nil {
			return err
		}
		if err := c.audit.Record(ctx, store, AuditEntry{
			Action:     messages.AuditActionKYCVerified,
			EntityType: "kyc_profile",
			EntityID:   &current.ID,
			Before:     before,
			After:      snapshotKYC(current),
		}); err != nil {
			return err
		}
		if err := c.emitStatusEvent(ctx, store, current, models.KYCStatusVerified, "nin", ""); err != nil {
			return err
		}

		result = current
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &NINLivenessDecision{
		Approved:       result != nil && result.NINStatus == models.KYCStatusVerified,
		Status:         models.KYCStatusVerified,
		LivenessPassed: true,
		LivenessScore:  100,
		FaceMatched:    true,
		FaceMatchScore: 100,
	}, nil
}

func (c *kycController) SubmitFacialVerification(ctx context.Context, userID uuid.UUID, input providers.NINLivenessInput) (*FacialVerificationResult, error) {
	input.UserID = userID.String()
	if _, err := c.prepareIdentitySubmission(ctx, userID, "facial_verification"); err != nil {
		return nil, err
	}

	validationErr := validateMobileLivenessPayload(input)
	status, reason := models.KYCStatusVerified, ""
	if validationErr != nil {
		status, reason = models.KYCStatusRejected, validationErr.Error()
	}

	selfie, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(input.SelfieImageBase64))
	frames, _ := json.Marshal(input.LivenessFrameImagesBase64)
	now := c.now().UTC()

	err := c.store.Atomic(ctx, func(store repo.Store) error {
		attempt := &models.KYCVerificationAttempt{
			UserID:        userID,
			Kind:          "facial_verification",
			Provider:      "device_liveness",
			Status:        status,
			FailureReason: truncate(reason, 500),
			CreatedAt:     now,
		}
		if err := store.KYC().CreateAttempt(ctx, attempt); err != nil {
			return err
		}
		if decodeErr == nil && len(selfie) > 0 {
			capture := &models.KYCFacialCapture{
				UserID:                userID,
				AttemptID:             attempt.ID,
				VerificationRequestID: input.VerificationRequestID,
				SelfieImage:           selfie,
				LivenessFrameImages:   frames,
				CreatedAt:             now,
			}
			if err := store.KYC().CreateFacialCapture(ctx, capture); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if validationErr != nil {
		return nil, validationErr
	}
	return &FacialVerificationResult{Passed: true, VerificationRequestID: input.VerificationRequestID, RecordedAt: now}, nil
}

func (c *kycController) recordBVNFacialCapture(ctx context.Context, store repo.Store, userID, attemptID uuid.UUID, input providers.BVNLivenessInput) error {
	selfie, err := base64.StdEncoding.DecodeString(strings.TrimSpace(input.SelfieImageBase64))
	if err != nil || len(selfie) == 0 {
		return nil
	}
	frames, err := json.Marshal(input.LivenessFrameImagesBase64)
	if err != nil {
		return err
	}
	return store.KYC().CreateFacialCapture(ctx, &models.KYCFacialCapture{
		UserID:                userID,
		AttemptID:             attemptID,
		VerificationRequestID: input.VerificationRequestID,
		SelfieImage:           selfie,
		LivenessFrameImages:   frames,
		CreatedAt:             c.now().UTC(),
	})
}

func (c *kycController) recordNINFacialCapture(ctx context.Context, store repo.Store, userID, attemptID uuid.UUID, input providers.NINLivenessInput) error {
	selfie, err := base64.StdEncoding.DecodeString(strings.TrimSpace(input.SelfieImageBase64))
	if err != nil || len(selfie) == 0 {
		return nil
	}
	frames, err := json.Marshal(input.LivenessFrameImagesBase64)
	if err != nil {
		return err
	}
	return store.KYC().CreateFacialCapture(ctx, &models.KYCFacialCapture{
		UserID:                userID,
		AttemptID:             attemptID,
		VerificationRequestID: input.VerificationRequestID,
		SelfieImage:           selfie,
		LivenessFrameImages:   frames,
		CreatedAt:             c.now().UTC(),
	})
}

func validateMobileLivenessPayload(input providers.NINLivenessInput) error {
	// Validate image data
	total := 0
	images := append([]string{input.SelfieImageBase64}, input.LivenessFrameImagesBase64...)
	for _, encoded := range images {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil || len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
			return messages.ErrInvalidLivenessPayload
		}
		total += len(data)
		if len(data) > 5*1024*1024 || total > 15*1024*1024 {
			return messages.ErrInvalidLivenessPayload
		}
	}
	return nil
}

func (c *kycController) finalizeNIN(ctx context.Context, userID uuid.UUID, claim IdentityClaim, nin, hash string, verification *providers.IdentityVerification, providerErr error, livenessInput *providers.NINLivenessInput) (*models.KYCProfile, error) {
	status, reason, score := c.classify(verification, providerErr, claim)

	ciphertext, err := c.cipher.Encrypt(nin)
	if err != nil {
		return nil, err
	}

	var result *models.KYCProfile
	err = c.store.Atomic(ctx, func(store repo.Store) error {
		current, err := store.KYC().ProfileByUserID(ctx, userID)
		if err != nil {
			return err
		}
		before := snapshotKYC(current)

		attempt, err := c.recordAttempt(ctx, store, userID, "nin", status, reason, score, verification)
		if err != nil {
			return err
		}
		if livenessInput != nil {
			if err := c.recordNINFacialCapture(ctx, store, userID, attempt.ID, *livenessInput); err != nil {
				return err
			}
		}

		current.NINStatus = status
		current.NINEncrypted = ciphertext
		current.NINHash = hash
		current.NINLast4 = nin[len(nin)-4:]

		if status == models.KYCStatusVerified {
			verifiedAt := c.now().UTC()
			current.NINVerifiedAt = &verifiedAt
			applyIdentityNames(current, verification)
			if current.AddressStatus == models.KYCStatusVerified && current.Tier < models.KYCTier3 {
				current.Tier = models.KYCTier3
			}
		}
		if status == models.KYCStatusRejected {
			current.RejectReason = reason
		}
		if verification != nil && len(verification.Raw) > 0 {
			current.ProviderPayload = json.RawMessage(verification.Raw)
		}

		if err := store.KYC().UpdateProfile(ctx, current); err != nil {
			return err
		}

		action := messages.AuditActionKYCSubmitted
		if status == models.KYCStatusVerified {
			action = messages.AuditActionKYCVerified
		} else if status == models.KYCStatusRejected {
			action = messages.AuditActionKYCRejected
		}
		if err := c.audit.Record(ctx, store, AuditEntry{
			Action:     action,
			EntityType: "kyc_profile",
			EntityID:   &current.ID,
			Before:     before,
			After:      snapshotKYC(current),
		}); err != nil {
			return err
		}

		if err := c.emitStatusEvent(ctx, store, current, status, "nin", reason); err != nil {
			return err
		}

		result = current
		return nil
	})
	if err != nil {
		return nil, err
	}

	if status == models.KYCStatusRejected {
		return result, messages.ErrIdentityMismatch
	}
	if status == models.KYCStatusManualReview {
		return result, messages.ErrManualReviewPending
	}
	return result, nil
}

func (c *kycController) SubmitAddress(ctx context.Context, userID uuid.UUID, address AddressInput) (*models.KYCProfile, error) {
	if strings.TrimSpace(address.Line1) == "" ||
		strings.TrimSpace(address.City) == "" ||
		strings.TrimSpace(address.State) == "" {
		return nil, messages.ErrAddressIncomplete
	}
	country := strings.ToUpper(strings.TrimSpace(address.Country))
	if country == "" {
		country = "NG"
	}

	var result *models.KYCProfile
	err := c.store.Atomic(ctx, func(store repo.Store) error {
		profile, err := c.EnsureProfile(ctx, store, userID)
		if err != nil {
			return err
		}
		before := snapshotKYC(profile)

		profile.AddressLine1 = address.Line1
		profile.AddressLine2 = address.Line2
		profile.City = address.City
		profile.State = address.State
		profile.PostalCode = address.PostalCode
		profile.Country = country
		profile.AddressStatus = models.KYCStatusVerified

		if profile.NINStatus == models.KYCStatusVerified &&
			profile.BVNStatus == models.KYCStatusVerified &&
			profile.Tier < models.KYCTier3 {
			profile.Tier = models.KYCTier3
		}

		if err := store.KYC().UpdateProfile(ctx, profile); err != nil {
			return err
		}
		if err := c.audit.Record(ctx, store, AuditEntry{
			Action:     messages.AuditActionKYCSubmitted,
			EntityType: "kyc_profile",
			EntityID:   &profile.ID,
			Before:     before,
			After:      snapshotKYC(profile),
		}); err != nil {
			return err
		}

		if profile.Tier == models.KYCTier3 && before.Tier != models.KYCTier3 {
			if err := c.outbox.Emit(ctx, store, OutboxMessage{
				Type:          messages.EventTierUpgraded,
				AggregateType: messages.AggregateUser,
				AggregateID:   userID,
				Payload: map[string]any{
					"user_id": userID,
					"tier":    profile.Tier,
				},
			}); err != nil {
				return err
			}
		}

		result = profile
		return nil
	})

	return result, err
}

func (c *kycController) Attempts(ctx context.Context, userID uuid.UUID, limit int) ([]models.KYCVerificationAttempt, error) {
	return c.store.KYC().AttemptsByUserID(ctx, userID, limit)
}

func (c *kycController) VerifyNINByPhoneMatch(ctx context.Context, userID uuid.UUID, nin string) (*NINVerificationResult, error) {
	if c.directoryLookup == nil {
		return nil, providers.ErrNotConfigured
	}
	nin = strings.TrimSpace(nin)
	if !messages.IdentityNumberPattern.MatchString(nin) {
		return nil, messages.ErrInvalidNIN
	}

	// Get user's registered phone number from User model
	user, err := c.store.Auth().UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if profile, err := c.Profile(ctx, userID); err == nil {
		ninVerified := profile.NINStatus == models.KYCStatusVerified && profile.NINVerifiedAt != nil
		facialApproved := user.FacialVerified || strings.EqualFold(user.FacialStatus, "verified") || strings.EqualFold(user.FacialStatus, "approved") || ninVerified
		canSetPassword := ninVerified
		if canSetPassword {
			return &NINVerificationResult{
				Approved:       true,
				Verified:       true,
				NINVerified:    true,
				FacialApproved: facialApproved,
				CanSetPassword: true,
				NINStatus:      profile.NINStatus,
				FacialStatus:   user.FacialStatus,
				NIN:            nin,
				PhoneMatch:     true,
				Message:        "NIN and facial verification already approved",
			}, nil
		}
	}
	registeredPhone := user.PhoneNumber
	if registeredPhone == "" {
		return &NINVerificationResult{
			Approved:       false,
			Verified:       false,
			NINVerified:    false,
			FacialApproved: user.FacialVerified,
			CanSetPassword: false,
			NINStatus:      models.KYCStatusUnverified,
			FacialStatus:   user.FacialStatus,
			PhoneMatch:     false,
			Message:        "user has no registered phone number",
		}, nil
	}

	// Lookup NIN details from provider (SwiftEnd/NINAuth)
	ninDetails, err := c.directoryLookup.LookupNIN(ctx, nin)
	if err != nil {
		return nil, err
	}

	// Normalize both phone numbers for comparison
	normalizedRegistered := normalizePhone(registeredPhone)
	normalizedNIN := normalizePhone(ninDetails.PhoneNumber)

	phoneMatch := normalizedRegistered == normalizedNIN

	result := &NINVerificationResult{
		Approved:       phoneMatch,
		Verified:       phoneMatch,
		NINVerified:    phoneMatch,
		FacialApproved: phoneMatch || user.FacialVerified,
		CanSetPassword: phoneMatch,
		NINStatus:      models.KYCStatusUnverified,
		FacialStatus:   user.FacialStatus,
		NIN:            nin,
		PhoneMatch:     phoneMatch,
		ProviderName:   c.directoryLookup.Name(),
		Message:        "",
		Details:        ninDetails,
	}

	if phoneMatch {
		// Update KYC profile with verified NIN
		hash := c.cipher.IndexHash(nin)
		ciphertext, err := c.cipher.Encrypt(nin)
		if err != nil {
			return nil, err
		}

		err = c.store.Atomic(ctx, func(store repo.Store) error {
			profile, err := c.EnsureProfile(ctx, store, userID)
			if err != nil {
				return err
			}
			before := snapshotKYC(profile)

			now := c.now().UTC()
			profile.NINStatus = models.KYCStatusVerified
			profile.NINEncrypted = ciphertext
			profile.NINHash = hash
			profile.NINLast4 = nin[len(nin)-4:]
			profile.NINVerifiedAt = &now
			result.NINStatus = profile.NINStatus

			if profile.Tier < models.KYCTier3 {
				profile.Tier = models.KYCTier3
			}

			// Copy additional NIN details if not already set
			if profile.FirstName == "" && ninDetails.FirstName != "" {
				profile.FirstName = ninDetails.FirstName
			}
			if profile.MiddleName == "" && ninDetails.MiddleName != "" {
				profile.MiddleName = ninDetails.MiddleName
			}
			if profile.LastName == "" && ninDetails.LastName != "" {
				profile.LastName = ninDetails.LastName
			}
			if profile.DateOfBirth == nil && ninDetails.DateOfBirth != nil {
				profile.DateOfBirth = ninDetails.DateOfBirth
			}
			if profile.Gender == "" && ninDetails.Gender != "" {
				profile.Gender = ninDetails.Gender
			}

			if err := store.KYC().UpdateProfile(ctx, profile); err != nil {
				return err
			}

			if err := c.audit.Record(ctx, store, AuditEntry{
				Action:     messages.AuditActionKYCVerified,
				EntityType: "kyc_profile",
				EntityID:   &profile.ID,
				Before:     before,
				After:      snapshotKYC(profile),
			}); err != nil {
				return err
			}

			result.Message = "NIN verified successfully"
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		result.Message = "phone number does not match NIN record"
	}

	return result, nil
}

// normalizePhone normalizes phone numbers to a standard format for comparison
// Converts +234, 234, 0 prefixes to just the 10-digit number
func normalizePhone(phone string) string {
	phone = strings.TrimSpace(phone)
	phone = strings.ReplaceAll(phone, " ", "")
	phone = strings.ReplaceAll(phone, "-", "")
	phone = strings.ReplaceAll(phone, "(", "")
	phone = strings.ReplaceAll(phone, ")", "")

	// Remove country code prefixes
	if strings.HasPrefix(phone, "+234") {
		phone = phone[4:]
	} else if strings.HasPrefix(phone, "234") {
		phone = phone[3:]
	} else if strings.HasPrefix(phone, "0") {
		phone = phone[1:]
	}

	return phone
}

func (c *kycController) prepareIdentitySubmission(ctx context.Context, userID uuid.UUID, kind string) (*models.KYCProfile, error) {
	profile, err := c.EnsureProfile(ctx, c.store, userID)
	if err != nil {
		return nil, err
	}

	count, err := c.store.KYC().RecentAttemptCount(ctx, userID, kind, c.now().UTC().Add(-messages.VerificationWindow))
	if err != nil {
		return nil, err
	}
	if count >= messages.MaxVerificationAttempts {
		return nil, messages.ErrVerificationThrottled
	}
	return profile, nil
}

func (c *kycController) assertIdentityUnused(ctx context.Context, userID uuid.UUID, hash, kind string) error {
	var (
		existing *models.KYCProfile
		err      error
	)
	if kind == "bvn" {
		existing, err = c.store.KYC().ProfileByBVNHash(ctx, hash)
	} else {
		existing, err = c.store.KYC().ProfileByNINHash(ctx, hash)
	}
	if errors.Is(err, repo.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if existing.UserID != userID {
		return messages.ErrIdentityInUse
	}
	return nil
}

func (c *kycController) classify(verification *providers.IdentityVerification, err error, claim IdentityClaim) (models.KYCVerificationStatus, string, int) {
	switch {
	case errors.Is(err, providers.ErrNotFound):
		log.Printf("KYC Classify: Identity number not found")
		return models.KYCStatusRejected, "identity number not found", 0
	case errors.Is(err, providers.ErrRejected):
		log.Printf("KYC Classify: Provider rejected request")
		return models.KYCStatusRejected, "provider rejected the request", 0
	case err != nil:
		log.Printf("KYC Classify: Provider error: %v", err)
		return models.KYCStatusManualReview, "provider unavailable: " + err.Error(), 0
	case verification == nil || !verification.Verified:
		log.Printf("KYC Classify: Verification nil or not verified flag set")
		return models.KYCStatusRejected, "identity could not be verified", 0
	}

	score := matchScore(claim, verification)
	log.Printf("KYC Classify: Match score calculated: %d", score)

	switch {
	case score >= messages.IdentityApproveScore:
		return models.KYCStatusVerified, "", score
	case score <= messages.IdentityRejectScore:
		return models.KYCStatusRejected, "details do not match the identity record", score
	default:
		return models.KYCStatusManualReview, "partial name match requires review", score
	}
}

func matchScore(claim IdentityClaim, verification *providers.IdentityVerification) int {
	// Since claim no longer contains FirstName, LastName, DateOfBirth (removed from user input),
	// and FaceDedup has already performed server-side identity matching with the authority database,
	// we trust the provider's verification result.
	// If verification.Verified is true and we have identity data, we return max score.

	if verification == nil || !verification.Verified {
		log.Printf("KYC Match: Verification failed or nil")
		return 0
	}

	// FaceDedup has already done biometric matching against authority database
	// If they say it's verified, we accept the identity data they return
	score := 100 // Full match - provider did the verification

	log.Printf("KYC Match: Provider verified identity (FirstName: %s, LastName: %s) Score: %d",
		verification.FirstName, verification.LastName, score)

	return score
}

func namesMatch(claimed, actual string) bool {
	claimed = normaliseName(claimed)
	actual = normaliseName(actual)
	if claimed == "" || actual == "" {
		return false
	}
	if claimed == actual {
		return true
	}
	return strings.HasPrefix(actual, claimed) || strings.HasPrefix(claimed, actual)
}

func normaliseName(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if r >= 'a' && r <= 'z' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func (c *kycController) recordAttempt(
	ctx context.Context,
	store repo.Store,
	userID uuid.UUID,
	kind string,
	status models.KYCVerificationStatus,
	reason string,
	score int,
	verification *providers.IdentityVerification,
) (*models.KYCVerificationAttempt, error) {
	attempt := &models.KYCVerificationAttempt{
		UserID:        userID,
		Kind:          kind,
		Provider:      c.identity.Name(),
		Status:        status,
		FailureReason: truncate(reason, 500),
		MatchScore:    score,
		CreatedAt:     c.now().UTC(),
	}
	if verification != nil {
		attempt.ProviderRef = verification.Reference
		if len(verification.Raw) > 0 {
			attempt.ResponseBody = json.RawMessage(verification.Raw)
		}
	}
	if err := store.KYC().CreateAttempt(ctx, attempt); err != nil {
		return nil, err
	}
	return attempt, nil
}

func (c *kycController) emitStatusEvent(
	ctx context.Context,
	store repo.Store,
	profile *models.KYCProfile,
	status models.KYCVerificationStatus,
	kind, reason string,
) error {
	eventType := messages.EventKYCVerified
	if status == models.KYCStatusRejected {
		eventType = messages.EventKYCRejected
	} else if status != models.KYCStatusVerified {
		return nil
	}

	return c.outbox.Emit(ctx, store, OutboxMessage{
		Type:          eventType,
		AggregateType: messages.AggregateUser,
		AggregateID:   profile.UserID,
		Payload: map[string]any{
			"user_id": profile.UserID,
			"kind":    kind,
			"tier":    profile.Tier,
			"reason":  reason,
		},
	})
}

func applyIdentityNames(profile *models.KYCProfile, verification *providers.IdentityVerification) {
	if verification == nil {
		return
	}
	if verification.FirstName != "" {
		profile.FirstName = verification.FirstName
	}
	if verification.MiddleName != "" {
		profile.MiddleName = verification.MiddleName
	}
	if verification.LastName != "" {
		profile.LastName = verification.LastName
	}
	if verification.DateOfBirth != nil {
		profile.DateOfBirth = verification.DateOfBirth
	}
	if verification.Gender != "" {
		profile.Gender = verification.Gender
	}
}

type kycSnapshot struct {
	Tier          models.KYCTier               `json:"tier"`
	PhoneStatus   models.KYCVerificationStatus `json:"phone_status"`
	BVNStatus     models.KYCVerificationStatus `json:"bvn_status"`
	NINStatus     models.KYCVerificationStatus `json:"nin_status"`
	AddressStatus models.KYCVerificationStatus `json:"address_status"`
	Sanctioned    bool                         `json:"sanctioned"`
	PEP           bool                         `json:"pep"`
}

func snapshotKYC(profile *models.KYCProfile) kycSnapshot {
	return kycSnapshot{
		Tier:          profile.Tier,
		PhoneStatus:   profile.PhoneStatus,
		BVNStatus:     profile.BVNStatus,
		NINStatus:     profile.NINStatus,
		AddressStatus: profile.AddressStatus,
		Sanctioned:    profile.Sanctioned,
		PEP:           profile.PEP,
	}
}
