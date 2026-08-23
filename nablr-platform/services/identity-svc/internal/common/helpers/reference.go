package helpers

import (
	"fmt"
	"strings"
	"time"
)

var Lagos = mustLoadLagos()

func mustLoadLagos() *time.Location {
	loc, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		return time.FixedZone("WAT", 1*60*60)
	}
	return loc
}

const (
	RefPrefixInternalTransfer = "NBL-INT"
	RefPrefixExternalTransfer = "NBL-EXT"
	RefPrefixDeposit          = "NBL-DEP"
	RefPrefixVAS              = "NBL-VAS"
	RefPrefixVault            = "NBL-VLT"
	RefPrefixFee              = "NBL-FEE"
	RefPrefixCommission       = "NBL-COM"
	RefPrefixReversal         = "NBL-REV"
	RefPrefixInterest         = "NBL-INR"
	RefPrefixAdjustment       = "NBL-ADJ"
	RefPrefixQRPayment        = "NBL-QRP"
)

func NewReference(prefix string) string {
	now := time.Now().In(Lagos)
	return fmt.Sprintf("%s-%s-%s", prefix, now.Format("20060102150405"), GenerateRandomCode(8))
}

func DerivedReference(parent, suffix string) string {
	return parent + "-" + strings.ToUpper(suffix)
}

func VTpassRequestID() string {
	now := time.Now().In(Lagos)
	return now.Format("200601021504") + GenerateRandomCode(8)
}

func DayKey(t time.Time) string {
	return t.In(Lagos).Format("2006-01-02")
}

func MonthKey(t time.Time) string {
	return t.In(Lagos).Format("2006-01")
}

func BackoffDelay(attempt int, base, max time.Duration) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 16 {
		attempt = 16
	}
	delay := base << uint(attempt)
	if delay > max || delay <= 0 {
		return max
	}
	return delay
}
