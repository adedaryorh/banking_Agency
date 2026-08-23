package service

import (
	"testing"

	"nabla/transfers-svc/internal/providers"
)

// refundable is the complete allowlist: codes that PROVE the payout instruction
// never executed, so the hold may be released without asking the rail.
func TestRefundableOnRejection_Allowlist(t *testing.T) {
	refundable := []providers.ErrorCode{
		providers.ErrInvalidRequest,
		providers.ErrRejected,
		providers.ErrInsufficientFunds,
		providers.ErrUnauthenticated,
		providers.ErrNotSupported,
	}
	for _, code := range refundable {
		if !refundableOnRejection(code) {
			t.Errorf("code %q must be refundable: it proves the rail never acted, so holding the customer's money is wrong", code)
		}
	}
}

func TestRefundableOnRejection_UnknownIsNeverRefundable(t *testing.T) {
	neverRefundable := []providers.ErrorCode{
		providers.ErrDuplicateRequest, // rail already has it — refund would double-pay
		providers.ErrTimeout,          // outcome unknown
		providers.ErrUnknown,          // outcome unknown, by name
		providers.ErrUnavailable,      // request may or may not have landed
		providers.ErrCircuitOpen,      // we stopped ourselves; rail state unknown
		providers.ErrNotFound,         // handled by the rail query, not a blind refund
		providers.ErrRateLimited,      // request may have been accepted before the 429
	}
	for _, code := range neverRefundable {
		if refundableOnRejection(code) {
			t.Errorf("code %q must NOT be refundable: it does not prove the instruction failed, so an automatic refund risks a double payment", code)
		}
	}
}

func TestRefundableOnRejection_UnrecognisedDefaultsToInvestigate(t *testing.T) {
	if refundableOnRejection(providers.ErrorCode("some_new_code_a_rail_invented")) {
		t.Fatal("an unrecognised code defaulted to refundable; the allowlist must default to investigate")
	}
	if refundableOnRejection(providers.ErrorCode("")) {
		t.Fatal("the empty code defaulted to refundable; the allowlist must default to investigate")
	}
}
