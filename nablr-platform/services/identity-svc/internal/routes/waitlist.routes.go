package routes

import (
	"github.com/gin-gonic/gin"

	"nabla/identity-svc/internal/handlers"
)

func RegisterWaitlistRoutes(v1 *gin.RouterGroup, handler *handlers.WaitlistHandler) {
	waitlist := v1.Group("/waitlist")
	waitlist.GET("/username/availability", handler.CheckUsername)
	waitlist.POST("/username/reserve", handler.ReserveUsername)
	waitlist.POST("/join", handler.Join)
}
