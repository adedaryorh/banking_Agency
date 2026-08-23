package helpers

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

func GenerateAccountNumber(currency string) (string, error) {
	var starterDigit string
	switch currency {
	case "NGN":
		starterDigit = "1"
	case "USD":
		starterDigit = "2"
	case "GBP":
		starterDigit = "3"
	case "EUR":
		starterDigit = "4"
	default:
		starterDigit = "0"
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

	return starterDigit + string(digits), nil
}

func GenerateAccountReference() string {
	digits := make([]byte, 10)
	max := big.NewInt(10)
	for i := range digits {
		n, _ := rand.Int(rand.Reader, max)
		digits[i] = byte('0' + n.Int64())
	}
	return "ACC" + string(digits)
}
