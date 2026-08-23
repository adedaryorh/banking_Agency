package helpers

import (
	"net/http"
	"strings"
)

// BearerToken extracts the bearer token from the Authorization header.
func BearerToken(r *http.Request) string {
	base := r.Header.Get("Authorization")
	token := strings.TrimPrefix(base, "Bearer ")
	if token == base {
		token = strings.TrimPrefix(base, "bearer ")
	}
	return strings.TrimSpace(token)
}
