package helpers

import (
	"errors"
	"regexp"
	"strings"
)

var (
	ErrInvalidPhoneNumber = errors.New("invalid phone number format")
	ErrInvalidCountryCode = errors.New("only Nigerian phone numbers (+234) are supported")
)

func NormalizePhoneNumber(input string) (string, error) {
	if input == "" {
		return "", ErrInvalidPhoneNumber
	}

	cleaned := strings.ReplaceAll(input, " ", "")
	cleaned = strings.ReplaceAll(cleaned, "-", "")
	cleaned = strings.ReplaceAll(cleaned, "(", "")
	cleaned = strings.ReplaceAll(cleaned, ")", "")
	cleaned = strings.TrimSpace(cleaned)
	if strings.HasPrefix(cleaned, "+234") {
		if len(cleaned) != 14 {
			return "", ErrInvalidPhoneNumber
		}
		return cleaned, nil
	}

	if strings.HasPrefix(cleaned, "234") {
		if len(cleaned) != 13 {
			return "", ErrInvalidPhoneNumber
		}
		return "+" + cleaned, nil
	}
	if strings.HasPrefix(cleaned, "0") {
		if len(cleaned) != 11 {
			return "", ErrInvalidPhoneNumber
		}
		return "+234" + cleaned[1:], nil
	}

	if len(cleaned) == 10 {
		return "+234" + cleaned, nil
	}
	return "", ErrInvalidPhoneNumber
}
func ValidatePhoneNumber(input string) (string, error) {
	normalized, err := NormalizePhoneNumber(input)
	if err != nil {
		return "", err
	}
	digits := normalized[4:]
	if !isNumeric(digits) {
		return "", ErrInvalidPhoneNumber
	}

	firstDigit := digits[0]
	if firstDigit != '7' && firstDigit != '8' && firstDigit != '9' {
		return "", ErrInvalidPhoneNumber
	}

	return normalized, nil
}

func FormatPhoneNumberForDisplay(phoneNumber string) string {
	normalized, err := NormalizePhoneNumber(phoneNumber)
	if err != nil {
		return phoneNumber
	}

	if len(normalized) == 14 {
		return normalized[:4] + " " + normalized[4:7] + " " + normalized[7:10] + " " + normalized[10:]
	}

	return normalized
}

func MaskPhoneNumber(phoneNumber string) string {
	normalized, err := NormalizePhoneNumber(phoneNumber)
	if err != nil {
		return "***"
	}

	if len(normalized) == 14 {
		return normalized[:7] + "***" + normalized[10:]
	}

	return "***"
}

func ParsePhoneNumbers(text string) []string {
	re := regexp.MustCompile(`\+?234?[0-9]{10,11}|0[7-9][0-9]{9}|[7-9][0-9]{9}`)
	matches := re.FindAllString(text, -1)

	var normalized []string
	seen := make(map[string]bool)

	for _, match := range matches {
		if phone, err := NormalizePhoneNumber(match); err == nil {
			if !seen[phone] {
				normalized = append(normalized, phone)
				seen[phone] = true
			}
		}
	}

	return normalized
}

func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func ComparePhoneNumbers(phone1, phone2 string) bool {
	norm1, err1 := NormalizePhoneNumber(phone1)
	norm2, err2 := NormalizePhoneNumber(phone2)

	if err1 != nil || err2 != nil {
		return false
	}

	return norm1 == norm2
}
