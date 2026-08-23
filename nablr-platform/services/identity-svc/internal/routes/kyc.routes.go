package routes

import (
	"github.com/gin-gonic/gin"

	"nabla/identity-svc/internal/handlers"
)

func RegisterKYCRoutes(protected *gin.RouterGroup, handler *handlers.KYCHandler) {
	kyc := protected.Group("/kyc")
	kyc.GET("/profile", handler.Profile)
	kyc.GET("/attempts", handler.Attempts)
	kyc.GET("/limits", handler.Limits)
	// verification api's
	kyc.POST("/bvn", handler.SubmitBVN)
	kyc.POST("/nin", handler.SubmitNIN)
	kyc.POST("/address", handler.SubmitAddress)
	// lookup api's
	kyc.POST("/details", handler.Details)
	kyc.POST("/convert-photo", handler.ConvertBase64ToImage)
}

func RegisterTier3DocumentRoutes(protected *gin.RouterGroup, handler *handlers.Tier3DocumentHandler) {
	documents := protected.Group("/kyc/tier3/documents")
	documents.POST("", handler.Upload)
	documents.GET("/:document_id/download", handler.Download)
}

func RegisterAvatarRoutes(protected *gin.RouterGroup, handler *handlers.AvatarHandler) {
	me := protected.Group("/users/me")
	me.POST("/image", handler.Upload)
	me.GET("/image", handler.Download)
}
