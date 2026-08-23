package id

import "github.com/google/uuid"

// New generates a new UUID v4
func New() uuid.UUID {
	return uuid.New()
}

// Parse parses a UUID string
func Parse(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}
