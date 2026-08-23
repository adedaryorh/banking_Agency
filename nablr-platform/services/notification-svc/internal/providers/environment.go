package providers

import "strings"

func isDevelopment(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "local", "development", "test":
		return true
	}
	return false
}
