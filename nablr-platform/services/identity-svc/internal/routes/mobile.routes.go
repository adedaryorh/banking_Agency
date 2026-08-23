package routes

import (
	"github.com/gin-gonic/gin"

	"nabla/identity-svc/internal/handlers"
)

func RegisterMobileRoutes(v1 *gin.RouterGroup, handler *handlers.MobileHandler) {
	mobile := v1.Group("/mobile")
	mobile.POST("/version/check", handler.CheckVersion)
}
