package handlers

import "testing"

func TestParseMinor_Exact(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0.29", 29},   // the float-killer
		{"0.01", 1},    // one minor unit
		{"0.10", 10},   // trailing zero kept
		{"1", 100},     // no decimal point
		{"1.5", 150},   // one decimal place padded to two
		{"100", 10000}, // whole number
		{"1234.56", 123456},
		{"  12.34  ", 1234}, // surrounding space trimmed
		{"-5.00", -500},     // explicit negative
		{"+5", 500},         // explicit positive sign
		{"0", 0},
	}
	for _, c := range cases {
		got, err := parseMinor(c.in)
		if err != nil {
			t.Errorf("parseMinor(%q) errored: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseMinor(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseMinor_Rejects(t *testing.T) {
	bad := []string{
		"",       // required
		"1.005",  // more than two decimals
		"1.2345", // more than two decimals
		"abc",    // not a number
		"1.2a",   // trailing junk in fraction
		"1,000",  // thousands separator is not a decimal
		"0x10",   // not base-10
	}
	for _, in := range bad {
		if _, err := parseMinor(in); err == nil {
			t.Errorf("parseMinor(%q) should have been rejected but was accepted", in)
		}
	}
}

func TestParseMinor_OverflowRejected(t *testing.T) {
	if _, err := parseMinor("99999999999999999999"); err == nil {
		t.Fatal("an amount large enough to overflow int64 was accepted; it must be rejected")
	}
}

func TestMoneyDTO_FormatsWithoutFloat(t *testing.T) {
	cases := []struct {
		minor    int64
		currency string
		want     string
	}{
		{29, "NGN", "NGN 0.29"},
		{5, "NGN", "NGN 0.05"},
		{10000, "NGN", "NGN 100.00"},
		{123456, "NGN", "NGN 1234.56"},
		{-500, "NGN", "NGN -5.00"},
		{29, "", "0.29"},
	}
	for _, c := range cases {
		got := moneyDTO(c.minor, c.currency)
		if got.Formatted != c.want {
			t.Errorf("moneyDTO(%d, %q).Formatted = %q, want %q", c.minor, c.currency, got.Formatted, c.want)
		}
		if got.Amount != c.minor {
			t.Errorf("moneyDTO(%d, %q).Amount = %d, want %d (the raw minor value must be preserved)", c.minor, c.currency, got.Amount, c.minor)
		}
	}
}

func TestMoney_RoundTrip(t *testing.T) {
	values := []int64{0, 1, 5, 29, 99, 100, 150, 10000, 123456, 999999999}
	for _, minor := range values {
		formatted := moneyDTO(minor, "").Formatted
		got, err := parseMinor(formatted)
		if err != nil {
			t.Errorf("round trip of %d: parseMinor(%q) errored: %v", minor, formatted, err)
			continue
		}
		if got != minor {
			t.Errorf("round trip of %d: rendered %q, parsed back %d", minor, formatted, got)
		}
	}
}
