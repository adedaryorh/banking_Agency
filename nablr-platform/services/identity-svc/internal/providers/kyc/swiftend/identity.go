package swiftend

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nabla/identity-svc/internal/providers"
)

type Config struct {
	BaseURL   string
	ServiceID string
	Timeout   time.Duration
}

type Provider struct{ client *providers.Client }

func New(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.ServiceID) == "" {
		return nil, providers.ErrNotConfigured
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = "https://swiftend.com"
	}
	return &Provider{client: providers.NewClient(cfg.BaseURL, cfg.Timeout, map[string]string{
		"SERVICEID": cfg.ServiceID,
	})}, nil
}

func (*Provider) Name() string { return "swiftend" }

type responseInfo struct {
	ResponseCode string `json:"ResponseCode"`
	Source       string `json:"Source"`
	Message      string `json:"Message"`
}

// responseData captures all fields returned by SwiftEnd's BVN API
type responseData struct {
	BVN                string `json:"BVN"`
	FirstName          string `json:"firstName"`
	MiddleName         string `json:"middleName"`
	LastName           string `json:"lastName"`
	DateOfBirth        string `json:"dateOfBirth"`
	PhoneNumber1       string `json:"phoneNumber1"`
	PhoneNumber2       string `json:"phoneNumber2"`
	RegistrationDate   string `json:"registrationDate"`
	EnrollmentBank     string `json:"enrollmentBank"`
	EnrollmentBranch   string `json:"enrollmentBranch"`
	Email              string `json:"email"`
	Gender             string `json:"gender"`
	LevelOfAccount     string `json:"levelOfAccount"`
	LGAOfOrigin        string `json:"lgaOfOrigin"`
	LGAOfResidence     string `json:"lgaOfResidence"`
	MaritalStatus      string `json:"maritalStatus"`
	NIN                string `json:"nin"`
	NameOnCard         string `json:"nameOnCard"`
	Nationality        string `json:"nationality"`
	ResidentialAddress string `json:"residentialAddress"`
	StateOfOrigin      string `json:"stateOfOrigin"`
	StateOfResidence   string `json:"stateOfResidence"`
	Title              string `json:"title"`
	WatchListed        string `json:"watchListed"`
	ImageBase64        string `json:"ImageBase64"`
}

type response struct {
	Info responseInfo `json:"ResponseInfo"`
	Data responseData `json:"ResponseData"`
}

func (p *Provider) VerifyBVN(ctx context.Context, bvn string) (*providers.IdentityVerification, error) {
	bvn = strings.TrimSpace(bvn)
	if len(bvn) != 11 || !digitsOnly(bvn) {
		return nil, providers.ErrRejected
	}
	res, err := p.client.Do(ctx, providers.Request{
		Method: http.MethodGet,
		Path:   "/verifybvn2/",
		Query:  url.Values{"bvn": {bvn}},
	})
	if err != nil {
		return nil, err
	}
	var decoded response
	if err := providers.DecodeInto(res, &decoded); err != nil {
		return nil, err
	}
	
	// Log the raw response from SwiftEnd
	if jsonData, err := json.MarshalIndent(decoded, "", "  "); err == nil {
		log.Printf("[SwiftEnd BVN] Response from SwiftEnd for BVN verification: %s", string(jsonData))
	} else {
		log.Printf("[SwiftEnd BVN] Response from SwiftEnd (failed to format): %+v", decoded)
	}
	
	if err := classifyResponse(decoded.Info); err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(decoded.Info.Source), "BVN") {
		return nil, fmt.Errorf("%w: swiftend returned unexpected source", providers.ErrRejected)
	}
	value := decoded.Data
	if strings.TrimSpace(value.FirstName) == "" || strings.TrimSpace(value.LastName) == "" {
		return nil, fmt.Errorf("%w: swiftend success response missing identity fields", providers.ErrUnavailable)
	}
	result := &providers.IdentityVerification{
		Provider:           p.Name(),
		Verified:           true,
		BVN:                value.BVN,
		FirstName:          value.FirstName,
		MiddleName:         value.MiddleName,
		LastName:           value.LastName,
		Gender:             value.Gender,
		PhoneNumber:        value.PhoneNumber1,
		PhoneNumber2:       value.PhoneNumber2,
		Email:              value.Email,
		EnrollmentBank:     value.EnrollmentBank,
		EnrollmentBranch:   value.EnrollmentBranch,
		LevelOfAccount:     value.LevelOfAccount,
		LGAOfOrigin:        value.LGAOfOrigin,
		LGAOfResidence:     value.LGAOfResidence,
		MaritalStatus:      value.MaritalStatus,
		NIN:                value.NIN,
		NameOnCard:         value.NameOnCard,
		Nationality:        value.Nationality,
		ResidentialAddress: value.ResidentialAddress,
		StateOfOrigin:      value.StateOfOrigin,
		StateOfResidence:   value.StateOfResidence,
		Title:              value.Title,
		WatchListed:        value.WatchListed,
		PhotoBase64:        value.ImageBase64,
		MatchScore:         100,
	}
	if parsed, ok := parseDate(value.DateOfBirth); ok {
		result.DateOfBirth = &parsed
	}
	if parsed, ok := parseDate(value.RegistrationDate); ok {
		result.RegistrationDate = &parsed
	}
	return result, nil
}

func (*Provider) VerifyNIN(context.Context, string) (*providers.IdentityVerification, error) {
	return nil, providers.ErrNotConfigured
}

func digitsOnly(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func parseDate(value string) (time.Time, bool) {
	for _, layout := range []string{"02-Jan-2006", "02-01-2006", "2006-01-02", "02/01/2006"} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

var _ providers.IdentityProvider = (*Provider)(nil)
