package routes

import (
	"context"
	"nabla/identity-svc/internal/alerting"
	"nabla/identity-svc/internal/handlers"
	"nabla/identity-svc/internal/middleware"
	"net/http"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	_ "nabla/identity-svc/docs"
)

type Config struct {
	JWTSecret      string
	JWTIssuer      string
	Auth           *handlers.AuthHandler
	Onboarding     *handlers.OnboardingHandler
	Mobile         *handlers.MobileHandler
	KYC            *handlers.KYCHandler
	Security       *handlers.SecurityHandler
	Tier3          *handlers.Tier3DocumentHandler
	Waitlist       *handlers.WaitlistHandler
	Alerts         alerting.Notifier
	Avatar         *handlers.AvatarHandler
	AllowedOrigins []string
	EnableHSTS     bool

	// AdminQuery, when non-nil, mounts the operator-only raw-SQL route at
	// POST /admin/db/query, gated by AdminAPIKey rather than the customer
	// JWT middleware. A nil AdminQuery (no ADMIN_API_KEY configured) means
	// the route is not mounted at all.
	AdminQuery  *handlers.AdminQueryHandler
	AdminAPIKey string
	Readiness   func(context.Context) error
}

func NewRouter(config Config) *gin.Engine {
	router := gin.New()
	router.Use(
		otelgin.Middleware("identity-svc"),
		middleware.RequestID(),
		middleware.StructuredLogger(config.Alerts),
		middleware.Recovery(config.Alerts),
		middleware.SecurityHeaders(config.EnableHSTS),
		middleware.CORS(config.AllowedOrigins),
		middleware.RequestMetadata(),
	)

	router.GET("/health", health)
	router.GET("/health/live", health)
	router.GET("/health/ready", func(c *gin.Context) {
		if config.Readiness != nil {
			if err := config.Readiness(c.Request.Context()); err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"service": "identity-svc", "status": "not_ready"})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"service": "identity-svc", "status": "ready"})
	})
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// Operator-only raw SQL, gated by a shared API key rather than the
	// customer JWT middleware. Not mounted at all when AdminQuery is nil.
	if config.AdminQuery != nil {
		admin := router.Group("/admin")
		admin.Use(middleware.AdminAuth(config.AdminAPIKey))
		admin.POST("/db/query", config.AdminQuery.RawQuery)
	}

	v1 := router.Group("/api/v1")
	if config.Auth != nil {
		RegisterAuthRoutes(v1, config.Auth)
	}
	if config.Onboarding != nil {
		RegisterOnboardingRoutes(v1, config.Onboarding)
	}
	if config.Mobile != nil {
		RegisterMobileRoutes(v1, config.Mobile)
	}
	if config.Waitlist != nil {
		RegisterWaitlistRoutes(v1, config.Waitlist)
	}
	protected := v1.Group("")
	protected.Use(
		middleware.Authenticate(config.JWTSecret, config.JWTIssuer),
		middleware.RequestMetadata(),
	)
	if config.Auth != nil {
		RegisterAuthProtectedRoutes(protected, config.Auth)
	}
	if config.KYC != nil {
		RegisterKYCRoutes(protected, config.KYC)
	}
	if config.Security != nil {
		RegisterSecurityRoutes(protected, config.Security)
	}
	if config.Tier3 != nil {
		RegisterTier3DocumentRoutes(protected, config.Tier3)
	}
	if config.Avatar != nil {
		RegisterAvatarRoutes(protected, config.Avatar)
	}

	return router
}

func health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"service": "identity-svc", "status": "healthy"})
}
