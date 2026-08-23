package helpers

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/go-playground/validator/v10"
)

type FieldError struct {
	Field   string `json:"field"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

type Errors []FieldError

func (e Errors) Error() string { return "validation failed" }

var (
	once            sync.Once
	instance        *validator.Validate
	decimalPattern  = regexp.MustCompile(`^(0|[1-9][0-9]{0,17})(\.[0-9]{1,6})?$`)
	htmlLikePattern = regexp.MustCompile(`(?i)<\s*/?\s*[a-z][^>]*>`)
	usernamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{2,29}$`)
)

func Validator() *validator.Validate {
	once.Do(func() {
		instance = validator.New(validator.WithRequiredStructEnabled())
		instance.RegisterTagNameFunc(func(field reflect.StructField) string {
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				return field.Name
			}
			return name
		})
		mustRegister("cidr", validateCIDR)
		mustRegister("safe_url", validateSafeURL)
		mustRegister("decimal", validateDecimal)
		mustRegister("json", validateJSON)
		mustRegister("no_html", validateNoHTML)
		mustRegister("password", validatePassword)
		mustRegister("nablr_username", validateNablrUsername)
	})
	return instance
}

func mustRegister(tag string, fn validator.Func) {
	if err := instance.RegisterValidation(tag, fn); err != nil {
		panic(err)
	}
}

func Struct(value any) error {
	err := Validator().Struct(value)
	if err == nil {
		return nil
	}
	var invalid *validator.InvalidValidationError
	if errors.As(err, &invalid) {
		return err
	}
	var violations validator.ValidationErrors
	if !errors.As(err, &violations) {
		return err
	}
	out := make(Errors, 0, len(violations))
	for _, violation := range violations {
		out = append(out, FieldError{
			Field:   violation.Field(),
			Rule:    violation.Tag(),
			Message: messageValidator(violation),
		})
	}
	return out
}

func messageValidator(v validator.FieldError) string {
	switch v.Tag() {
	case "required":
		return "is required"
	case "email":
		return "must be a valid email address"
	case "uuid4":
		return "must be a valid UUID"
	case "oneof":
		return "must be one of: " + v.Param()
	case "min":
		return "must be at least " + v.Param()
	case "max":
		return "must not exceed " + v.Param()
	case "len":
		return "must have length " + v.Param()
	case "cidr":
		return "must be a valid CIDR range"
	case "safe_url":
		return "must be a safe HTTPS URL"
	case "decimal":
		return "must be a non-negative decimal"
	case "json":
		return "must contain valid JSON"
	case "no_html":
		return "must not contain HTML"
	case "password":
		return "does not satisfy the password policy"
	case "nablr_username":
		return "must be 3-30 characters, start with a letter, and use only letters, numbers, and underscores"
	default:
		return "is invalid"
	}
}

func validateCIDR(fl validator.FieldLevel) bool {
	_, _, err := net.ParseCIDR(strings.TrimSpace(fl.Field().String()))
	return err == nil
}

func validateSafeURL(fl validator.FieldLevel) bool {
	value := strings.TrimSpace(fl.Field().String())
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

func validateDecimal(fl validator.FieldLevel) bool {
	return decimalPattern.MatchString(strings.TrimSpace(fl.Field().String()))
}

func validateJSON(fl validator.FieldLevel) bool {
	field := fl.Field()
	switch field.Kind() {
	case reflect.String:
		return json.Valid([]byte(field.String()))
	case reflect.Slice:
		return json.Valid(field.Bytes())
	default:
		return false
	}
}

func validateNoHTML(fl validator.FieldLevel) bool {
	return !htmlLikePattern.MatchString(fl.Field().String())
}

func validatePassword(fl validator.FieldLevel) bool {
	return Validate(fl.Field().String()) == nil
}

func validateNablrUsername(fl validator.FieldLevel) bool {
	return ValidUsernameFormat(fl.Field().String())
}

// ValidUsernameFormat reports whether value is a well-formed Nablr username:
// 3-30 characters, starts with a letter, and contains only letters, digits,
// and underscores. Shared by struct validation and the raw query-param path
// used by the waitlist username-availability check.
func ValidUsernameFormat(value string) bool {
	return usernamePattern.MatchString(strings.TrimSpace(value))
}
