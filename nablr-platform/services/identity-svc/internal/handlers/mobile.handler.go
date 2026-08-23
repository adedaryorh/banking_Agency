package handlers

import (
	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	"nabla/identity-svc/internal/config"
	"nabla/identity-svc/internal/models"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type MobileHandler struct {
	cfg *config.Config
}

func NewMobileHandler(cfg *config.Config) *MobileHandler { return &MobileHandler{cfg: cfg} }

func (h *MobileHandler) CheckVersion(c *gin.Context) {
	var request models.MobileVersionCheckRequest
	if !bindAndValidate(c, &request) {
		return
	}
	platform := strings.ToLower(strings.TrimSpace(request.Platform))
	if platform == "" {
		helpers.Failure(c, http.StatusBadRequest, messages.InvalidFields, gin.H{"platform": "platform is required"})
		return
	}

	var latest string
	var minimum string
	var storeURL string

	switch platform {
	case "ios":
		latest = h.cfg.MobileIOSLatestVersion
		minimum = h.cfg.MobileIOSMinSupportedVersion
		storeURL = h.cfg.MobileIOSStoreURL
	case "android":
		latest = h.cfg.MobileAndroidLatestVersion
		minimum = h.cfg.MobileAndroidMinSupportedVersion
		storeURL = h.cfg.MobileAndroidStoreURL
	default:
		helpers.Failure(c, http.StatusBadRequest, messages.InvalidFields, gin.H{
			"platform": "platform must be ios or android",
		})
		return
	}
	message := h.cfg.MobileUpdateMessage

	updateRequired := minimum != "" && compareVersions(request.Version, minimum) < 0
	updateAvailable := latest != "" && compareVersions(request.Version, latest) < 0
	helpers.Success(c, http.StatusOK, messages.AppVersionChecked, gin.H{
		"platform":                  platform,
		"current_version":           request.Version,
		"current_build_number":      request.BuildNumber,
		"latest_version":            latest,
		"minimum_supported_version": minimum,
		"update_required":           updateRequired,
		"update_available":          updateAvailable,
		"store_url":                 storeURL,
		"message":                   message,
	})
}

func compareVersions(left, right string) int {
	leftParts := strings.Split(strings.TrimSpace(left), ".")
	rightParts := strings.Split(strings.TrimSpace(right), ".")
	max := len(leftParts)
	if len(rightParts) > max {
		max = len(rightParts)
	}
	for i := 0; i < max; i++ {
		var l, r int
		if i < len(leftParts) {
			l, _ = strconv.Atoi(numericPrefix(leftParts[i]))
		}
		if i < len(rightParts) {
			r, _ = strconv.Atoi(numericPrefix(rightParts[i]))
		}
		if l < r {
			return -1
		}
		if l > r {
			return 1
		}
	}
	return 0
}

func numericPrefix(value string) string {
	var b strings.Builder
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			break
		}
		b.WriteRune(ch)
	}
	if b.Len() == 0 {
		return "0"
	}
	return b.String()
}
