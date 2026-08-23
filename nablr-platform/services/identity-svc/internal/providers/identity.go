package providers

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
)

var (
	ErrNotFound = errors.New("provider: record not found")

	ErrUnavailable = errors.New("provider: temporarily unavailable")

	ErrRejected = errors.New("provider: request rejected")

	ErrIndeterminate = errors.New("provider: outcome indeterminate")

	ErrInsufficientProviderFunds = errors.New("provider: insufficient float")
	ErrDuplicateReference        = errors.New("provider: reference already used")
	ErrNotConfigured             = errors.New("provider: not configured")
)

type IdentityVerification struct {
	Provider           string
	Verified           bool
	Reference          string
	BVN                string
	FirstName          string
	MiddleName         string
	LastName           string
	DateOfBirth        *time.Time
	Gender             string
	PhoneNumber        string
	PhoneNumber2       string
	Email              string
	RegistrationDate   *time.Time
	EnrollmentBank     string
	EnrollmentBranch   string
	LevelOfAccount     string
	LGAOfOrigin        string
	LGAOfResidence     string
	MaritalStatus      string
	NIN                string
	NameOnCard         string
	Nationality        string
	ResidentialAddress string
	StateOfOrigin      string
	StateOfResidence   string
	Title              string
	WatchListed        string
	PhotoBase64        string
	MatchScore         int
	Raw                []byte
}

type FailoverIdentityProvider struct {
	providers []IdentityProvider
	failover  bool
}

func NewFailoverIdentityProvider(failover bool, values ...IdentityProvider) (*FailoverIdentityProvider, error) {
	filtered := make([]IdentityProvider, 0, len(values))
	for _, value := range values {
		if value != nil {
			filtered = append(filtered, value)
		}
	}
	if len(filtered) == 0 {
		return nil, ErrNotConfigured
	}
	return &FailoverIdentityProvider{providers: filtered, failover: failover}, nil
}
func (p *FailoverIdentityProvider) Name() string {
	names := make([]string, 0, len(p.providers))
	for _, value := range p.providers {
		names = append(names, value.Name())
	}
	return strings.Join(names, ",")
}
func (p *FailoverIdentityProvider) VerifyBVN(ctx context.Context, bvn string) (*IdentityVerification, error) {
	return p.verify(func(provider IdentityProvider) (*IdentityVerification, error) {
		return provider.VerifyBVN(ctx, bvn)
	})
}
func (p *FailoverIdentityProvider) VerifyNIN(ctx context.Context, nin string) (*IdentityVerification, error) {
	return p.verify(func(provider IdentityProvider) (*IdentityVerification, error) {
		return provider.VerifyNIN(ctx, nin)
	})
}
func (p *FailoverIdentityProvider) verify(call func(IdentityProvider) (*IdentityVerification, error)) (*IdentityVerification, error) {
	var last error
	for index, provider := range p.providers {
		log.Printf("Identity: Attempting verification via provider=%s (failover=%v)", provider.Name(), p.failover)
		result, err := call(provider)
		if err == nil {
			log.Printf("Identity: Verification success via provider=%s", provider.Name())
			if result != nil && result.Provider == "" {
				result.Provider = provider.Name()
			}
			return result, nil
		}
		log.Printf("Identity: Provider=%s failed with error=%v", provider.Name(), err)
		last = err
		if !p.canFailover(err, index) {
			return nil, err
		}
		log.Printf("Identity: Retrying via failover from provider=%s", provider.Name())
	}
	return nil, last
}
func (p *FailoverIdentityProvider) VerifyBVNWithSelfie(ctx context.Context, bvn, selfie string) (*BVNSelfieVerification, error) {
	var last error
	capable := make([]IdentityProvider, 0, len(p.providers))
	for _, provider := range p.providers {
		if _, ok := provider.(BVNSelfieProvider); ok {
			capable = append(capable, provider)
		}
	}
	for index, provider := range capable {
		log.Printf("Identity: Attempting BVN+Selfie verification via provider=%s (failover=%v)", provider.Name(), p.failover)
		result, err := provider.(BVNSelfieProvider).VerifyBVNWithSelfie(ctx, bvn, selfie)
		if err == nil {
			log.Printf("Identity: BVN+Selfie verification success via provider=%s", provider.Name())
			if result != nil && result.Identity != nil && result.Identity.Provider == "" {
				result.Identity.Provider = provider.Name()
			}
			return result, nil
		}
		log.Printf("Identity: Provider=%s (BVN+Selfie) failed with error=%v", provider.Name(), err)
		last = err
		if !p.failover || index == len(capable)-1 || (!errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrIndeterminate) && !errors.Is(err, ErrInsufficientProviderFunds)) {
			return nil, err
		}
		log.Printf("Identity: Retrying via failover (BVN+Selfie) from provider=%s", provider.Name())
	}
	if last != nil {
		return nil, last
	}
	return nil, ErrNotConfigured
}
func (p *FailoverIdentityProvider) canFailover(err error, index int) bool {
	return p.failover && index < len(p.providers)-1 && (errors.Is(err, ErrUnavailable) || errors.Is(err, ErrIndeterminate) || errors.Is(err, ErrInsufficientProviderFunds))
}

type IdentityProvider interface {
	Name() string
	VerifyBVN(ctx context.Context, bvn string) (*IdentityVerification, error)
	VerifyNIN(ctx context.Context, nin string) (*IdentityVerification, error)
}

type PhoneDetails struct {
	FirstName   string     `json:"first_name"`
	LastName    string     `json:"last_name"`
	DateOfBirth *time.Time `json:"date_of_birth,omitempty"`
	PhoneNumber string     `json:"phone_number"`
	Gender      string     `json:"gender,omitempty"`
	Age         string     `json:"age,omitempty"`
	Occupation  string     `json:"occupation,omitempty"`
	State       string     `json:"state,omitempty"`
	LGA         string     `json:"lga,omitempty"`
}

type PhoneLookupProvider interface {
	LookupPhone(context.Context, string) (*PhoneDetails, error)
}

//	type NINDetails struct {
//		FirstName   string     `json:"first_name"`
//		MiddleName  string     `json:"middle_name,omitempty"`
//		LastName    string     `json:"last_name"`
//		DateOfBirth *time.Time `json:"date_of_birth,omitempty"`
//		PhoneNumber string     `json:"phone_number,omitempty"`
//		Gender      string     `json:"gender,omitempty"`
//		State       string     `json:"state,omitempty"`
//		Profession  string     `json:"profession,omitempty"`
//	}
type NINDetails struct {
	NIN              string     `json:"nin"`
	FirstName        string     `json:"first_name"`
	MiddleName       string     `json:"middle_name,omitempty"`
	LastName         string     `json:"last_name"`
	MaidenName       string     `json:"maiden_name,omitempty"`
	PhoneNumber      string     `json:"phone_number,omitempty"`
	State            string     `json:"state,omitempty"`
	Place            string     `json:"place,omitempty"`
	Profession       string     `json:"profession,omitempty"`
	Title            string     `json:"title,omitempty"`
	Height           string     `json:"height,omitempty"`
	Email            string     `json:"email,omitempty"`
	DateOfBirth      *time.Time `json:"date_of_birth,omitempty"`
	BirthState       string     `json:"birth_state,omitempty"`
	BirthCountry     string     `json:"birth_country,omitempty"`
	CentralID        string     `json:"central_id,omitempty"`
	DocumentNo       string     `json:"document_no,omitempty"`
	EducationalLevel string     `json:"educational_level,omitempty"`
	EmploymentStatus string     `json:"employment_status,omitempty"`

	NOKFirstName  string `json:"nok_first_name,omitempty"`
	NOKLastName   string `json:"nok_last_name,omitempty"`
	NOKMiddleName string `json:"nok_middle_name,omitempty"`
	NOKAddress1   string `json:"nok_address_1,omitempty"`
	NOKAddress2   string `json:"nok_address_2,omitempty"`
	NOKLGA        string `json:"nok_lga,omitempty"`
	NOKState      string `json:"nok_state,omitempty"`
	NOKTown       string `json:"nok_town,omitempty"`
	NOKPostalCode string `json:"nok_postal_code,omitempty"`

	OtherName        string `json:"other_name,omitempty"`
	ParentFirstName  string `json:"parent_first_name,omitempty"`
	ParentMiddleName string `json:"parent_middle_name,omitempty"`
	ParentSurname    string `json:"parent_surname,omitempty"`

	Photo string `json:"photo,omitempty"`

	NativeSpokenLanguage string `json:"native_spoken_language,omitempty"`
	OtherSpokenLanguage  string `json:"other_spoken_language,omitempty"`
	Religion             string `json:"religion,omitempty"`

	ResidenceTown         string  `json:"residence_town,omitempty"`
	ResidenceLGA          string  `json:"residence_lga,omitempty"`
	ResidenceState        string  `json:"residence_state,omitempty"`
	ResidenceStatus       string  `json:"residence_status,omitempty"`
	ResidenceAddressLine1 *string `json:"residence_address_line_1,omitempty"`
	ResidenceAddressLine2 *string `json:"residence_address_line_2,omitempty"`

	SelfOriginLGA   string `json:"self_origin_lga,omitempty"`
	SelfOriginPlace string `json:"self_origin_place,omitempty"`
	SelfOriginState string `json:"self_origin_state,omitempty"`

	Signature   string `json:"signature,omitempty"`
	Nationality string `json:"nationality,omitempty"`
	Gender      string `json:"gender,omitempty"`
	TrackingID  string `json:"tracking_id,omitempty"`
}

type NINLookupProvider interface {
	LookupNIN(context.Context, string) (*NINDetails, error)
}

type DirectoryLookupProvider interface {
	PhoneLookupProvider
	NINLookupProvider
	Name() string
}

type BVNSelfieVerification struct {
	Identity       *IdentityVerification
	LivenessPassed bool
	LivenessScore  float64
	FaceMatched    bool
	FaceMatchScore float64
}

type BVNSelfieProvider interface {
	VerifyBVNWithSelfie(ctx context.Context, bvn string, selfieBase64 string) (*BVNSelfieVerification, error)
}

type BVNLivenessInput struct {
	BVN, EncryptedLivenessPayload, SelfieImageBase64, UserID, VerificationRequestID string
	LivenessFrameImagesBase64                                                       []string
}

type BVNLivenessProvider interface {
	VerifyBVNLiveness(context.Context, BVNLivenessInput) (*BVNSelfieVerification, error)
}

type NINSelfieVerification struct {
	Identity       *IdentityVerification
	LivenessPassed bool
	LivenessScore  float64
	FaceMatched    bool
	FaceMatchScore float64
}
type NINSelfieProvider interface {
	VerifyNINWithSelfie(context.Context, string, string) (*NINSelfieVerification, error)
}

type NINLivenessInput struct {
	NIN, ResultToken, EncryptedLivenessPayload, SelfieImageBase64, UserID, VerificationRequestID string
	LivenessFrameImagesBase64                                                                    []string
}
type NINLivenessProvider interface {
	VerifyNINLiveness(context.Context, NINLivenessInput) (*NINSelfieVerification, error)
}

type MockNINLivenessProvider interface {
	IsMockNINLiveness() bool
}
