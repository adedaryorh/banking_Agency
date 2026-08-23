package handlers

import "testing"

func TestApplyCustomerID(t *testing.T) {
	const cust = "3f1a2b3c-4d5e-6f70-8192-a3b4c5d6e7f8"

	cases := []struct {
		name                                                   string
		customerID, beneficiaryID, recipientType, recipientRef string
		wantType, wantRef                                      string
	}{
		{
			name:          "no customer_id leaves the fields untouched",
			recipientType: "bank", recipientRef: "9020304050",
			wantType: "bank", wantRef: "9020304050",
		},
		{
			name:       "only customer_id folds into an internal reference",
			customerID: cust,
			wantType:   "internal", wantRef: cust,
		},
		{
			name:       "surrounding whitespace on customer_id is trimmed",
			customerID: "  " + cust + "  ",
			wantType:   "internal", wantRef: cust,
		},
		{
			name:       "a saved beneficiary_id wins over customer_id",
			customerID: cust, beneficiaryID: "b-123",
			wantType: "", wantRef: "",
		},
		{
			name:       "an explicit recipient_reference is left untouched",
			customerID: cust, recipientType: "internal", recipientRef: "@ada",
			wantType: "internal", wantRef: "@ada",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotType, gotRef := applyCustomerID(tc.customerID, tc.beneficiaryID, tc.recipientType, tc.recipientRef)
			if gotType != tc.wantType || gotRef != tc.wantRef {
				t.Fatalf("applyCustomerID = (%q, %q), want (%q, %q)", gotType, gotRef, tc.wantType, tc.wantRef)
			}
		})
	}
}

func descriptorFor(customerID, beneficiaryID, recipientType, recipientRef, bankCode, accountNumber string) string {
	rt, rr := applyCustomerID(customerID, beneficiaryID, recipientType, recipientRef)
	return recipientDescriptor(beneficiaryID, rt, rr, bankCode, accountNumber)
}

func TestCustomerIDDescriptorBinding(t *testing.T) {
	const cust = "3f1a2b3c-4d5e-6f70-8192-a3b4c5d6e7f8"
	authorizePIN := descriptorFor(cust, "", "", "", "", "")
	transfer := descriptorFor(cust, "", "", "", "", "")

	if authorizePIN != transfer {
		t.Fatalf("descriptor differs across endpoints: authorize-pin=%q transfer=%q", authorizePIN, transfer)
	}
	if want := "internal:" + cust; authorizePIN != want {
		t.Fatalf("customer_id descriptor = %q, want %q", authorizePIN, want)
	}
	if mixed := descriptorFor("3F1A2B3C-4D5E-6F70-8192-A3B4C5D6E7F8", "", "", "", "", ""); mixed != authorizePIN {
		t.Fatalf("mixed-case customer_id descriptor = %q, want %q", mixed, authorizePIN)
	}
}
