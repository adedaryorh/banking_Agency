package service

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	db "nabla/transfers-svc/db/sqlc"
)

func TestRequesterDisplayName(t *testing.T) {
	txt := func(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

	cases := []struct {
		name string
		row  db.PaymentRequest
		want string
	}{
		{"real name wins", db.PaymentRequest{RequesterName: txt("Ada Lovelace"), RequesterUsername: txt("ada")}, "Ada Lovelace"},
		{"handle when no name", db.PaymentRequest{RequesterUsername: txt("ada")}, "@ada"},
		{"neutral fallback when nothing was captured", db.PaymentRequest{}, "Nablr user"},
		{"whitespace-only name falls through to the handle", db.PaymentRequest{RequesterName: txt("   "), RequesterUsername: txt("ada")}, "@ada"},
		{"whitespace name and no handle falls through to the fallback", db.PaymentRequest{RequesterName: txt("   ")}, "Nablr user"},
	}
	for _, c := range cases {
		if got := requesterDisplayName(c.row); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
