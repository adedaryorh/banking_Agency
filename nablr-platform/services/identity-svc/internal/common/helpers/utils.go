package helpers

import (
	"crypto/rand"
	"encoding/base64"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
)

const alphanumeric = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func NewUUID() uuid.UUID {
	return uuid.New()
}

func ParseUUID(value string) (uuid.UUID, error) {
	return uuid.Parse(value)
}

func StringPtrOrNil(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func PointerString(s string) *string     { return &s }
func PointerTime(t time.Time) *time.Time { return &t }
func PointerInt(i int) *int              { return &i }
func PointerInt64(i int64) *int64        { return &i }

func ToSlug(value string) string {
	fields := strings.Fields(value)
	return strings.ToLower(strings.Join(fields, "-"))
}

func GenerateRandomCode(length int) string {
	if length <= 0 {
		return ""
	}
	result := make([]byte, length)
	max := big.NewInt(int64(len(alphanumeric)))
	for i := range result {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			result[i] = alphanumeric[0]
			continue
		}
		result[i] = alphanumeric[n.Int64()]
	}
	return string(result)
}

// DecodeBase64Image decodes a base64 encoded string to bytes
func DecodeBase64Image(base64Str string) ([]byte, error) {
	// Remove data URI scheme if present (e.g., "data:image/jpeg;base64,")
	if idx := strings.Index(base64Str, ","); idx != -1 {
		base64Str = base64Str[idx+1:]
	}
	
	// Decode base64 string
	return base64.StdEncoding.DecodeString(base64Str)
}
