package swiftend

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"nabla/identity-svc/internal/providers"
)

type ninResponse struct {
	Info responseInfo `json:"ResponseInfo"`

	Data struct {
		NIN              string `json:"nin"`
		FirstName        string `json:"firstname"`
		MiddleName       string `json:"middlename"`
		LastName         string `json:"surname"`
		MaidenName       string `json:"maidenname"`
		PhoneNumber      string `json:"telephoneno"`
		State            string `json:"state"`
		Place            string `json:"place"`
		Profession       string `json:"profession"`
		Title            string `json:"title"`
		Height           string `json:"height"`
		Email            string `json:"email"`
		BirthDate        string `json:"birthdate"`
		BirthState       string `json:"birthstate"`
		BirthCountry     string `json:"birthcountry"`
		CentralID        string `json:"centralID"`
		DocumentNo       string `json:"documentno"`
		EducationalLevel string `json:"educationallevel"`
		EmploymentStatus string `json:"employmentstatus"`

		NOKFirstName  string `json:"nok_firstname"`
		NOKLastName   string `json:"nok_lastname"`
		NOKMiddleName string `json:"nok_middlename"`
		NOKAddress1   string `json:"nok_address1"`
		NOKAddress2   string `json:"nok_address2"`
		NOKLGA        string `json:"nok_lga"`
		NOKState      string `json:"nok_state"`
		NOKTown       string `json:"nok_town"`
		NOKPostalCode string `json:"nok_postalcode"`

		OtherName        string `json:"othername"`
		ParentFirstName  string `json:"pfirstname"`
		Photo            string `json:"photo"`
		ParentMiddleName string `json:"pmiddlename"`
		ParentSurname    string `json:"psurname"`

		NativeSpokenLanguage string `json:"nspokenlang"`
		OtherSpokenLanguage  string `json:"ospokenlang"`
		Religion             string `json:"religion"`

		ResidenceTown         string  `json:"residence_Town"`
		ResidenceLGA          string  `json:"residence_lga"`
		ResidenceState        string  `json:"residence_state"`
		ResidenceStatus       string  `json:"residencestatus"`
		ResidenceAddressLine1 *string `json:"residence_AddressLine1"`
		ResidenceAddressLine2 *string `json:"residence_AddressLine2"`

		SelfOriginLGA   string `json:"self_origin_lga"`
		SelfOriginPlace string `json:"self_origin_place"`
		SelfOriginState string `json:"self_origin_state"`

		Signature   string `json:"signature"`
		Nationality string `json:"nationality"`
		Gender      string `json:"gender"`
		TrackingID  string `json:"trackingId"`
	} `json:"ResponseData"`
}

func (p *Provider) LookupNIN(ctx context.Context, nin string) (*providers.NINDetails, error) {
	nin = strings.TrimSpace(nin)
	if len(nin) != 11 || !digitsOnly(nin) {
		return nil, providers.ErrRejected
	}
	res, err := p.client.Do(ctx, providers.Request{Method: http.MethodGet, Path: "/verifynin", Query: url.Values{"regNo": {nin}}})
	if err != nil {
		return nil, err
	}
	var decoded ninResponse
	if err := providers.DecodeInto(res, &decoded); err != nil {
		return nil, err
	}
	
	// Log the raw response from SwiftEnd
	if jsonData, err := json.MarshalIndent(decoded, "", "  "); err == nil {
		log.Printf("[SwiftEnd NIN] Response from SwiftEnd for NIN lookup: %s", string(jsonData))
	} else {
		log.Printf("[SwiftEnd NIN] Response from SwiftEnd (failed to format): %+v", decoded)
	}
	
	if err := classifyResponse(decoded.Info); err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(decoded.Info.Source), "NIMC") {
		return nil, fmt.Errorf("%w: swiftend returned unexpected NIN source", providers.ErrRejected)
	}
	if decoded.Data.FirstName == "" || decoded.Data.LastName == "" {
		return nil, fmt.Errorf("%w: swiftend success response missing NIN details", providers.ErrUnavailable)
	}
	result := &providers.NINDetails{
		NIN:              decoded.Data.NIN,
		FirstName:        decoded.Data.FirstName,
		MiddleName:       decoded.Data.MiddleName,
		LastName:         decoded.Data.LastName,
		MaidenName:       decoded.Data.MaidenName,
		PhoneNumber:      decoded.Data.PhoneNumber,
		State:            decoded.Data.State,
		Place:            decoded.Data.Place,
		Profession:       decoded.Data.Profession,
		Title:            decoded.Data.Title,
		Height:           decoded.Data.Height,
		Email:            decoded.Data.Email,
		BirthState:       decoded.Data.BirthState,
		BirthCountry:     decoded.Data.BirthCountry,
		CentralID:        decoded.Data.CentralID,
		DocumentNo:       decoded.Data.DocumentNo,
		EducationalLevel: decoded.Data.EducationalLevel,
		EmploymentStatus: decoded.Data.EmploymentStatus,

		NOKFirstName:  decoded.Data.NOKFirstName,
		NOKLastName:   decoded.Data.NOKLastName,
		NOKMiddleName: decoded.Data.NOKMiddleName,
		NOKAddress1:   decoded.Data.NOKAddress1,
		NOKAddress2:   decoded.Data.NOKAddress2,
		NOKLGA:        decoded.Data.NOKLGA,
		NOKState:      decoded.Data.NOKState,
		NOKTown:       decoded.Data.NOKTown,
		NOKPostalCode: decoded.Data.NOKPostalCode,

		OtherName:        decoded.Data.OtherName,
		ParentFirstName:  decoded.Data.ParentFirstName,
		Photo:            decoded.Data.Photo,
		ParentMiddleName: decoded.Data.ParentMiddleName,
		ParentSurname:    decoded.Data.ParentSurname,

		NativeSpokenLanguage: decoded.Data.NativeSpokenLanguage,
		OtherSpokenLanguage:  decoded.Data.OtherSpokenLanguage,
		Religion:             decoded.Data.Religion,

		ResidenceTown:         decoded.Data.ResidenceTown,
		ResidenceLGA:          decoded.Data.ResidenceLGA,
		ResidenceState:        decoded.Data.ResidenceState,
		ResidenceStatus:       decoded.Data.ResidenceStatus,
		ResidenceAddressLine1: decoded.Data.ResidenceAddressLine1,
		ResidenceAddressLine2: decoded.Data.ResidenceAddressLine2,

		SelfOriginLGA:   decoded.Data.SelfOriginLGA,
		SelfOriginPlace: decoded.Data.SelfOriginPlace,
		SelfOriginState: decoded.Data.SelfOriginState,

		Signature:   decoded.Data.Signature,
		Nationality: decoded.Data.Nationality,
		Gender:      decoded.Data.Gender,
		TrackingID:  decoded.Data.TrackingID,
	}
	if parsed, ok := parseDate(decoded.Data.BirthDate); ok {
		result.DateOfBirth = &parsed
	}
	return result, nil
}

var _ providers.NINLookupProvider = (*Provider)(nil)
