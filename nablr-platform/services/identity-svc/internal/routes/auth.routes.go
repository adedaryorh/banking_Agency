package routes

import (
	"github.com/gin-gonic/gin"

	"nabla/identity-svc/internal/handlers"
)

func RegisterAuthRoutes(v1 *gin.RouterGroup, handler *handlers.AuthHandler) {
	auth := v1.Group("/auth")
	auth.POST("/register", handler.Register)
	auth.POST("/login", handler.Login)
	auth.POST("/google", handler.Google)
	auth.POST("/refresh", handler.Refresh)
	auth.POST("/logout", handler.Logout)
	auth.POST("/verify-email", handler.VerifyEmail)
	auth.POST("/resend-verification", handler.ResendVerification)
	auth.POST("/forgot-password", handler.ForgotPassword)
	auth.POST("/reset-password", handler.ResetPassword)
	auth.POST("/password/phone/request", handler.RequestPhonePasswordReset)
	auth.POST("/password/phone/confirm", handler.ConfirmPhonePasswordReset)
	auth.POST("/magic-link/request", handler.RequestMagicLink)
	auth.POST("/magic-link/confirm", handler.ConfirmMagicLink)
	// Unauthenticated: redeems the restore token Login issues when a
	// deactivated account's password check succeeds.
	auth.POST("/restore", handler.RestoreAccount)
}

func RegisterAuthProtectedRoutes(protected *gin.RouterGroup, handler *handlers.AuthHandler) {
	protected.POST("/auth/reactivate", handler.ReactivateAccount)
	// Account closure: reason -> facial re-verification -> confirmation code.
	protected.POST("/account/closure/request", handler.RequestClosure)
	protected.POST("/account/closure/verify", handler.VerifyClosureFace)
	protected.POST("/account/closure/confirm", handler.ConfirmClosure)
}
