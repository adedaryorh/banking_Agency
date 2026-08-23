package routes

import (
	"github.com/gin-gonic/gin"

	"nabla/identity-svc/internal/handlers"
)

func RegisterSecurityRoutes(protected *gin.RouterGroup, handler *handlers.SecurityHandler) {
	security := protected.Group("/security")

	security.GET("/pin", handler.PINStatus)
	security.POST("/pin", handler.SetPIN)
	security.PATCH("/pin", handler.ChangePIN)
	security.POST("/pin/reset", handler.ResetPIN)

	security.POST("/otp/request", handler.RequestOTP)
	security.POST("/phone/verify", handler.VerifyPhone)
	security.POST("/phone/confirm", handler.ConfirmPhone)

	security.GET("/devices", handler.ListDevices)
	security.POST("/devices/trust", handler.TrustDevice)
	security.POST("/devices/:deviceId/block", handler.BlockDevice)
}
