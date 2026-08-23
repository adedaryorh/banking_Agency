package helpers

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

type CurrencyMeta struct {
	Code         string
	Name         string
	StarterDigit string
}

var Currencies = map[string]CurrencyMeta{
	"NGN": {Code: "NGN", Name: "Nigerian Naira", StarterDigit: "1"},
	"USD": {Code: "USD", Name: "United States Dollar", StarterDigit: "2"},
	"GBP": {Code: "GBP", Name: "British Pound Sterling", StarterDigit: "3"},
}

func IsValidCurrency(currency string) bool {
	_, ok := Currencies[currency]
	return ok
}

func ValidateAmountMinor(amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("amount must be greater than zero")
	}
	return nil
}

func GenerateAccountNumber(currency string) (string, error) {
	meta, ok := Currencies[currency]
	if !ok {
		return "", fmt.Errorf("unsupported currency: %s", currency)
	}

	digits := make([]byte, 9)
	max := big.NewInt(10)
	for i := range digits {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate account number: %w", err)
		}
		digits[i] = byte('0' + n.Int64())
	}

	return meta.StarterDigit + string(digits), nil
}
