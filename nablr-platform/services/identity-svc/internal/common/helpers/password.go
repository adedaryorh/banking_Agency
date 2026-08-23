package helpers

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

var ErrWeakPassword = errors.New("weak_password")

var (
	lower  = regexp.MustCompile(`[a-z]`)
	upper  = regexp.MustCompile(`[A-Z]`)
	digit  = regexp.MustCompile(`[0-9]`)
	symbol = regexp.MustCompile(`[^A-Za-z0-9\s]`)
)

var common = map[string]struct{}{
	"password": {}, "password1!": {}, "pass@1234": {}, "qwerty123!": {},
	"admin123!": {}, "welcome1!": {},
}

func Validate(value string) error {
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%w: password must not begin or end with whitespace", ErrWeakPassword)
	}
	if len(value) < 12 {
		return fmt.Errorf("%w: password must be at least 12 characters long", ErrWeakPassword)
	}
	if len(value) > 72 {
		return fmt.Errorf("%w: password must not exceed 72 bytes", ErrWeakPassword)
	}
	if !lower.MatchString(value) || !upper.MatchString(value) || !digit.MatchString(value) || !symbol.MatchString(value) {
		return fmt.Errorf("%w: password must contain either lowercase, uppercase, number, and symbol characters", ErrWeakPassword)
	}
	if _, found := common[strings.ToLower(value)]; found {
		return fmt.Errorf("%w: password is too common", ErrWeakPassword)
	}
	return nil
}

func Hash(value string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(value), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func Matches(hash, value string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(value)) == nil
}
