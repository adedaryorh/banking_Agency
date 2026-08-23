package swiftend

import (
	"fmt"
	"strings"

	"nabla/identity-svc/internal/providers"
)

// ResponseError retains Swiftend's code for server-side diagnosis while
// Unwrap exposes the stable provider category used by controllers/failover.
type ResponseError struct {
	Code    string
	Message string
	Kind    error
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("swiftend response code %s: %s", e.Code, strings.TrimSpace(e.Message))
}

func (e *ResponseError) Unwrap() error { return e.Kind }

func classifyResponse(info responseInfo) error {
	code := strings.TrimSpace(info.ResponseCode)
	if code == "00" {
		return nil
	}
	kind := providers.ErrRejected
	switch code {
	case "43":
		kind = providers.ErrInsufficientProviderFunds
	case "87", "95":
		// Invalid SERVICEID/hash is a configuration/authentication problem.
		// Do not fail over and hide a broken production configuration.
		kind = providers.ErrRejected
	case "88", "96", "97":
		// Network, unavailable and system errors are temporary and may fail over.
		kind = providers.ErrUnavailable
	case "99":
		kind = providers.ErrNotFound
	}
	return &ResponseError{Code: code, Message: info.Message, Kind: kind}
}
