package swiftend

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nabla/identity-svc/internal/providers"
)

type phoneResponse struct {
	Info responseInfo `json:"ResponseInfo"`
	Data struct {
		DateOfBirth string `json:"DATE_OF_BIRTH"`
		FirstName   string `json:"FIRST_NAME"`
		Gender      string `json:"GENDER"`
		LastName    string `json:"LAST_NAME"`
		MSISDN      string `json:"MSISDN"`
		Age         string `json:"AGE"`
		Occupation  string `json:"OCCUPATION"`
		State       string `json:"RESID_STATE"`
		LGA         string `json:"RESID_LGA"`
		// RESID_ADDR is intentionally omitted.
	} `json:"ResponseData"`
}

func (p *Provider) LookupPhone(ctx context.Context, phone string) (*providers.PhoneDetails, error) {
	phone = strings.TrimSpace(phone)
	if len(phone) != 11 || phone[0] != '0' || !digitsOnly(phone) {
		return nil, providers.ErrRejected
	}
	res, err := p.client.Do(ctx, providers.Request{
		Method: http.MethodGet,
		Path:   "/verifyphone2/",
		Query:  url.Values{"phone": {phone}, "searchtype": {"basic"}},
	})
	if err != nil {
		return nil, err
	}
	var decoded phoneResponse
	if err := providers.DecodeInto(res, &decoded); err != nil {
		return nil, err
	}
	if err := classifyResponse(decoded.Info); err != nil {
		return nil, err
	}
	if decoded.Data.MSISDN == "" || decoded.Data.FirstName == "" || decoded.Data.LastName == "" {
		return nil, fmt.Errorf("%w: swiftend success response missing phone details", providers.ErrUnavailable)
	}
	result := &providers.PhoneDetails{FirstName: decoded.Data.FirstName, LastName: decoded.Data.LastName, PhoneNumber: decoded.Data.MSISDN, Gender: decoded.Data.Gender, Age: decoded.Data.Age, Occupation: decoded.Data.Occupation, State: decoded.Data.State, LGA: decoded.Data.LGA}
	for _, layout := range []string{"2-1-2006", "02-01-2006", "02-Jan-2006", "2006-01-02"} {
		if parsed, parseErr := time.Parse(layout, strings.TrimSpace(decoded.Data.DateOfBirth)); parseErr == nil {
			result.DateOfBirth = &parsed
			break
		}
	}
	return result, nil
}

var _ providers.PhoneLookupProvider = (*Provider)(nil)
