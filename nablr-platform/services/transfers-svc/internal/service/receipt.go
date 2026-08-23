package service

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// ReceiptIdentity is the small sender block a customer receipt needs. It is
// deliberately separate from authorization: a receipt should still be available
// even if identity-svc is temporarily unavailable after the transfer happened.
type ReceiptIdentity struct {
	Name          string
	AccountNumber string
	Username      string
}

func (s *Service) ReceiptIdentityFor(ctx context.Context, userID uuid.UUID) ReceiptIdentity {
	if s == nil || s.identity == nil {
		return ReceiptIdentity{}
	}
	var out ReceiptIdentity
	if kyc, err := s.identity.GetKYCProfile(ctx, userID); err == nil {
		out.Name = strings.Join(nonEmpty(kyc.FirstName, kyc.MiddleName, kyc.LastName), " ")
	}
	if u, err := s.identity.GetUser(ctx, userID); err == nil {
		out.AccountNumber = u.AccountNumber
		out.Username = u.NablrUsername
		if out.Name == "" && u.NablrUsername != "" {
			out.Name = u.NablrUsername
		}
	}
	return out
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
