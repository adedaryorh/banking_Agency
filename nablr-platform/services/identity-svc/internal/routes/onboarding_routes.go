package routes

import (
	"github.com/gin-gonic/gin"

	"nabla/identity-svc/internal/handlers"
)

func RegisterOnboardingRoutes(v1 *gin.RouterGroup, handler *handlers.OnboardingHandler) {
	onboarding := v1.Group("/onboarding")
	onboarding.POST("/register", handler.Register)
	onboarding.POST("/otp/resend", handler.ResendOTP)
	onboarding.POST("/session/refresh", handler.RefreshSession)
	onboarding.POST("/phone/confirm", handler.ConfirmPhone)
	// onboarding.POST("/bvn", handler.SubmitBVN)
	onboarding.POST("/nin", handler.SubmitNIN)
	onboarding.POST("/details", handler.Details)
	//deprecated
	onboarding.POST("/facial-verification", handler.SubmitFacialVerification)
	onboarding.POST("/device", handler.RegisterDevice)
	onboarding.POST("/password", handler.SetPassword)
	onboarding.GET("/status", handler.Status)
}
