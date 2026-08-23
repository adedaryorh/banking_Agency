package main

import (
	"context"
	"fmt"
	"log"
	"nabla/api-gateway/internal/alerting"
	"nabla/api-gateway/internal/config"
	"nabla/api-gateway/internal/middleware"
	"nabla/api-gateway/internal/proxy"
	"nabla/api-gateway/internal/telemetry"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	_ "nabla/api-gateway/docs"
)

// @title Nabla API Gateway
// @version 1.0
// @description Public gateway for Nabla microservices.
// @BasePath /
// @schemes http https
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @securityDefinitions.apikey InternalServiceToken
// @in header
// @name X-Internal-Service-Token
func main() {
	// Load configuration
	cfg, err := config.Load()
	if cfg == nil {
		print("Failed to load configuration")
	}

	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}
	shutdownTelemetry, err := telemetry.Setup(context.Background(), "api-gateway")
	if err != nil {
		log.Printf("configure telemetry: %v", err)
		shutdownTelemetry = func(context.Context) error { return nil }
	}
	defer shutdownTelemetry(context.Background())
	alerts := alerting.New("api-gateway")

	logger, _ := zap.NewProduction()
	if cfg.Environment == "development" {
		logger, _ = zap.NewDevelopment(zap.AddStacktrace(zapcore.ErrorLevel))
	}
	defer logger.Sync()

	// Set Gin mode
	if cfg.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	// Initialize rate limiter
	rateLimiter, err := middleware.NewRateLimiter(cfg.RedisURL, cfg.RateLimitRate)
	if err != nil {
		logger.Warn("Failed to initialize Redis rate limiter, using in-memory fallback", zap.Error(err))
		rateLimiter = nil
	}

	// Create router
	router := gin.New()
	router.Use(otelgin.Middleware("api-gateway"))

	// Apply global middleware
	router.Use(gin.Recovery())
	router.Use(middleware.RequestID())
	router.Use(middleware.RequestLogger(logger, alerts))
	router.Use(middleware.SecurityHeaders())
	router.Use(middleware.CORS())

	// Apply rate limiting
	if rateLimiter != nil {
		router.Use(rateLimiter.Middleware())
	} else {
		router.Use(middleware.IPRateLimiter(100)) // 100 req/min fallback
	}

	// JSON responses for invalid routes / wrong methods, instead of Gin's
	// plain-text defaults.
	router.HandleMethodNotAllowed = true
	router.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "Not Found",
			"message": fmt.Sprintf("no route matches %s %s", c.Request.Method, c.Request.URL.Path),
		})
	})
	router.NoMethod(func(c *gin.Context) {
		c.JSON(http.StatusMethodNotAllowed, gin.H{
			"error":   "Method Not Allowed",
			"message": fmt.Sprintf("%s is not supported for %s", c.Request.Method, c.Request.URL.Path),
		})
	})

	// Health check (no auth required)
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"service": "api-gateway",
			"status":  "healthy",
			"time":    time.Now().Format(time.RFC3339),
		})
	})
	router.GET("/health/live", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"service": "api-gateway", "status": "healthy"}) })
	router.GET("/health/ready", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"service": "api-gateway", "status": "ready"}) })

	// Metrics endpoint (no auth required)
	router.GET("/metrics", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"service": "api-gateway",
			"uptime":  time.Since(time.Now()).String(),
		})
	})
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// Operator-only admin routes: raw SQL against any of the three service
	// databases, and wallet funding. Each is a pure proxy to the owning
	// service's own /admin/* endpoint (see setupAdminRoutes) — the gateway
	// never holds a database credential itself. Gated by a shared API key,
	// not the customer JWT middleware; the backend re-checks the same key.
	setupAdminRoutes(router, cfg, logger)

	// API v1 routes
	v1 := router.Group("/api/v1")
	{
		setupPublicRoutes(v1, cfg, logger)

		// Protected routes (auth required)
		authorized := v1.Group("")
		authorized.Use(middleware.JWTAuth(cfg.JWTSecret, cfg.JWTIssuer))
		setupProtectedRoutes(authorized, cfg, logger)
	}

	// Create HTTP server
	srv := &http.Server{
		Addr:           ":" + cfg.Port,
		Handler:        router,
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   30 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1MB
	}

	// Start server in goroutine
	go func() {
		logger.Info("🚀 API Gateway started",
			zap.String("port", cfg.Port),
			zap.String("environment", cfg.Environment))

		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("Failed to start server", zap.Error(err))
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down API Gateway...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Fatal("Server forced to shutdown", zap.Error(err))
	}

	logger.Info("API Gateway stopped")
}

// setupAdminRoutes mounts the operator-only surface at the top level (not
// under /api/v1, matching the path each backend service itself uses:
// POST /admin/db/query and POST /admin/wallet/fund). If ADMIN_API_KEY is
// unset the routes still exist but AdminAuth refuses every request — see
// its own doc comment for why that is deliberate rather than unmounting them.
//
// NOTE: /admin/db/query/notification proxies to notification-svc's own
// /admin/db/query, which does not exist yet — that service still needs the
// same raw-query endpoint identity-svc and transfers-svc now have before
// this route is live end-to-end.
func setupAdminRoutes(router *gin.Engine, cfg *config.Config, logger *zap.Logger) {
	identityProxy := proxy.NewServiceProxy(cfg.Services.Identity.Timeout, logger)
	notificationProxy := proxy.NewServiceProxy(cfg.Services.Notification.Timeout, logger)
	transfersProxy := proxy.NewServiceProxy(cfg.Services.Transfers.Timeout, logger)

	admin := router.Group("/admin")
	admin.Use(middleware.AdminAuth(cfg.AdminAPIKey))
	{
		admin.POST("/db/query/identity", func(c *gin.Context) {
			identityProxy.ProxyRequestWithPath(c, cfg.Services.Identity.HTTPBaseURL, "/admin/db/query")
		})
		admin.POST("/db/query/notification", func(c *gin.Context) {
			notificationProxy.ProxyRequestWithPath(c, cfg.Services.Notification.HTTPBaseURL, "/admin/db/query")
		})
		admin.POST("/db/query/transfers", func(c *gin.Context) {
			transfersProxy.ProxyRequestWithPath(c, cfg.Services.Transfers.HTTPBaseURL, "/admin/db/query")
		})
		admin.POST("/wallet/fund", func(c *gin.Context) {
			transfersProxy.ProxyRequestWithPath(c, cfg.Services.Transfers.HTTPBaseURL, "/admin/wallet/fund")
		})
	}
}

func setupPublicRoutes(router *gin.RouterGroup, cfg *config.Config, logger *zap.Logger) {
	identityProxy := proxy.NewServiceProxy(cfg.Services.Identity.Timeout, logger)
	notificationProxy := proxy.NewServiceProxy(cfg.Services.Notification.Timeout, logger)
	transfersProxy := proxy.NewServiceProxy(cfg.Services.Transfers.Timeout, logger)
	router.POST("/webhooks/kyc", func(c *gin.Context) {
		identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL)
	})
	router.GET("/identity/health", func(c *gin.Context) {
		identityProxy.ProxyRequestWithPath(c, cfg.Services.Identity.HTTPBaseURL, "/health")
	})
	router.GET("/notification/health", func(c *gin.Context) {
		notificationProxy.ProxyRequestWithPath(c, cfg.Services.Notification.HTTPBaseURL, "/health")
	})
	router.GET("/transfers/health", func(c *gin.Context) {
		transfersProxy.ProxyRequestWithPath(c, cfg.Services.Transfers.HTTPBaseURL, "/health")
	})

	// Authentication endpoints (public)
	auth := router.Group("/auth")
	{
		auth.POST("/register", func(c *gin.Context) {
			identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL)
		})
		auth.POST("/login", func(c *gin.Context) {
			identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL)
		})
		auth.POST("/google", func(c *gin.Context) {
			identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL)
		})
		auth.POST("/refresh", func(c *gin.Context) {
			identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL)
		})
		auth.POST("/logout", func(c *gin.Context) {
			identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL)
		})
		auth.POST("/verify-email", func(c *gin.Context) {
			identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL)
		})
		auth.POST("/resend-verification", func(c *gin.Context) {
			identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL)
		})
		auth.POST("/forgot-password", func(c *gin.Context) {
			identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL)
		})
		auth.POST("/reset-password", func(c *gin.Context) {
			identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL)
		})
		auth.POST("/password/phone/request", func(c *gin.Context) {
			identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL)
		})
		auth.POST("/password/phone/confirm", func(c *gin.Context) {
			identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL)
		})
		auth.POST("/magic-link/request", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		auth.POST("/magic-link/confirm", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		// Redeems the restore token Login issues when it rejects a deactivated
		// account (no access token needed — that's the point).
		auth.POST("/restore", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
	}

	onboarding := router.Group("/onboarding")
	{
		onboarding.POST("/register", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		onboarding.POST("/otp/resend", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		onboarding.POST("/session/refresh", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		onboarding.POST("/phone/confirm", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		// Authenticated BVN verification remains available at POST /api/v1/kyc/bvn.
		// onboarding.POST("/bvn", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		onboarding.POST("/nin", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		onboarding.POST("/details", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		onboarding.POST("/facial-verification", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		onboarding.POST("/device", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		onboarding.POST("/password", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		onboarding.GET("/status", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
	}

	mobile := router.Group("/mobile")
	{
		mobile.POST("/version/check", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
	}

	waitlist := router.Group("/waitlist")
	{
		waitlist.GET("/username/availability", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		waitlist.POST("/username/reserve", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		waitlist.POST("/join", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
	}

	internal := router.Group("/internal/v1")
	{
		internal.POST("/sms", func(c *gin.Context) {
			notificationProxy.ProxyRequestWithPath(c, cfg.Services.Notification.HTTPBaseURL, "/internal/v1/sms")
		})
		internal.POST("/email", func(c *gin.Context) {
			notificationProxy.ProxyRequestWithPath(c, cfg.Services.Notification.HTTPBaseURL, "/internal/v1/email")
		})
		internal.POST("/push", func(c *gin.Context) {
			notificationProxy.ProxyRequestWithPath(c, cfg.Services.Notification.HTTPBaseURL, "/internal/v1/push")
		})
	}

	if cfg.Services.Transfers.HTTPBaseURL != "" {
		transfersProxy := proxy.NewServiceProxy(cfg.Services.Transfers.Timeout, logger)

		// Transfer/Payout provider webhooks — verified by HMAC signature, not JWT
		webhooks := router.Group("/webhooks/provider")
		{
			webhooks.POST("/payout", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
			webhooks.POST("/status", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		}

		// Collections (deposit) callback — reference is verified against the provider
		router.POST("/webhooks/collections", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		// Canonical Novac callback for both collections (inflow) and payouts (outflow).
		router.POST("/webhooks/novac", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })

		// Card issuer callbacks — secret in the path
		cardWebhooks := router.Group("/webhooks/cards")
		{
			cardWebhooks.POST("/authorization/:secret", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
			cardWebhooks.POST("/events/:secret", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		}
	}
}

func setupProtectedRoutes(router *gin.RouterGroup, cfg *config.Config, logger *zap.Logger) {
	// Create proxies for each service
	identityProxy := proxy.NewServiceProxy(cfg.Services.Identity.Timeout, logger)
	transfersProxy := proxy.NewServiceProxy(cfg.Services.Transfers.Timeout, logger)
	vasProxy := proxy.NewServiceProxy(cfg.Services.VAS.Timeout, logger)
	notificationProxy := proxy.NewServiceProxy(cfg.Services.Notification.Timeout, logger)

	// Identity Service Routes
	identity := router.Group("/identity")
	{
		identity.GET("/profile", func(c *gin.Context) {
			identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL)
		})
		identity.PUT("/profile", func(c *gin.Context) {
			identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL)
		})
	}

	// Auth Routes (self-serve account state changes, still logged in)
	auth := router.Group("/auth")
	{
		auth.POST("/reactivate", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
	}

	// Account Closure Routes: reason -> facial re-verification -> confirmation code.
	accountClosure := router.Group("/account/closure")
	{
		accountClosure.POST("/request", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		accountClosure.POST("/verify", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		accountClosure.POST("/confirm", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
	}

	// KYC Routes
	kyc := router.Group("/kyc")
	{
		kyc.GET("/profile", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		kyc.GET("/attempts", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		kyc.GET("/limits", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		kyc.POST("/bvn", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		kyc.POST("/nin", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		kyc.POST("/address", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		kyc.POST("/details", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		kyc.POST("/convert-photo", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		kyc.POST("/tier3/documents", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		kyc.GET("/tier3/documents/:document_id/download", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
	}

	// Security Routes (PIN, Device)
	security := router.Group("/security")
	{
		security.GET("/pin", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		security.POST("/pin", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		security.PATCH("/pin", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		security.POST("/pin/reset", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		security.POST("/otp/request", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		security.POST("/phone/verify", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		security.POST("/phone/confirm", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		security.GET("/devices", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		security.POST("/devices/trust", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
		security.POST("/devices/:deviceId/block", func(c *gin.Context) { identityProxy.ProxyRequest(c, cfg.Services.Identity.HTTPBaseURL) })
	}

	// Wallet Routes
	wallet := router.Group("/wallet")
	{
		wallet.POST("/create", func(c *gin.Context) {
			transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL)
		})
		wallet.GET("/balance", func(c *gin.Context) {
			transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL)
		})
		wallet.GET("/transactions", func(c *gin.Context) {
			transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL)
		})
		wallet.GET("/statement", func(c *gin.Context) {
			transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL)
		})
	}

	// Transfer Routes
	transfer := router.Group("/transfer")
	{
		transfer.POST("/internal", func(c *gin.Context) {
			transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL)
		})
		transfer.POST("/external", func(c *gin.Context) {
			transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL)
		})
		transfer.GET("/banks", func(c *gin.Context) {
			transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL)
		})
		transfer.POST("/beneficiary", func(c *gin.Context) {
			transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL)
		})
		transfer.GET("/beneficiaries", func(c *gin.Context) {
			transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL)
		})
	}

	// QR Code Routes
	qrcode := router.Group("/qrcode")
	{
		qrcode.POST("/generate", func(c *gin.Context) {
			transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL)
		})
		qrcode.POST("/scan", func(c *gin.Context) {
			transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL)
		})
		qrcode.POST("/pay", func(c *gin.Context) {
			transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL)
		})
		qrcode.GET("/merchant", func(c *gin.Context) {
			transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL)
		})
	}

	// Virtual Account Routes
	virtualAccount := router.Group("/virtual-account")
	{
		virtualAccount.POST("/create", func(c *gin.Context) {
			transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL)
		})
		virtualAccount.GET("/", func(c *gin.Context) {
			transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL)
		})
	}

	// ---------------------------------------------------------------------------
	// Transfers REST API — the canonical surface exposed by transfers-svc.
	// ---------------------------------------------------------------------------

	// Wallet Routes
	wallets := router.Group("/wallets")
	{
		wallets.POST("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		wallets.GET("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		wallets.GET("/:id", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		wallets.GET("/:id/transactions", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		wallets.GET("/:id/insights", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		wallets.GET("/:id/statement.csv", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
	}

	// Beneficiaries Routes
	beneficiaries := router.Group("/beneficiaries")
	{
		beneficiaries.GET("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		beneficiaries.POST("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		beneficiaries.GET("/recent", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		beneficiaries.GET("/:id", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		beneficiaries.PATCH("/:id", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		beneficiaries.DELETE("/:id", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		beneficiaries.POST("/resolve", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
	}

	// Bank Routes
	banks := router.Group("/banks")
	{
		banks.GET("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		banks.GET("/suggest", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		banks.GET("/timing", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
	}

	// Transfers Routes
	transfers := router.Group("/transfers")
	{
		transfers.POST("/quotes", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		transfers.GET("/quotes/:id", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		transfers.GET("/quotes", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		transfers.POST("/authorize-pin", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		transfers.POST("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		transfers.GET("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		transfers.GET("/:id", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		transfers.POST("/:id/cancel", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		transfers.GET("/:id/status", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		transfers.GET("/:id/timeline", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		transfers.GET("/:id/payout-status", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		transfers.GET("/suggestions", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
	}

	// Payment Requests Routes (Request Money — one user asks another to pay).
	// Create/list/decline/cancel move no money; POST /:id/pay routes through the
	// single transfer engine on the payer's side (PIN + limits enforced there).
	paymentRequests := router.Group("/payment-requests")
	{
		paymentRequests.POST("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		paymentRequests.GET("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		paymentRequests.GET("/:id", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		paymentRequests.POST("/:id/pay", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		paymentRequests.POST("/:id/decline", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		paymentRequests.POST("/:id/cancel", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
	}

	// Scheduled Payments Routes
	scheduledPayments := router.Group("/scheduled-payments")
	{
		scheduledPayments.POST("/authorize-pin", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		scheduledPayments.POST("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		scheduledPayments.GET("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		scheduledPayments.GET("/:id", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		scheduledPayments.PATCH("/:id", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		scheduledPayments.POST("/:id/cancel", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		scheduledPayments.POST("/:id/pause", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		scheduledPayments.POST("/:id/resume", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		scheduledPayments.GET("/:id/runs", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
	}

	// Limits Routes
	limits := router.Group("/limits")
	{
		limits.GET("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		limits.GET("/usage", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
	}

	// Pay Resolve Routes
	pay := router.Group("/pay")
	{
		pay.GET("/resolve", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
	}

	// Funding Account Routes (money-in deposits)
	fundingAccount := router.Group("/funding-account")
	{
		fundingAccount.GET("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		fundingAccount.POST("/check", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		fundingAccount.POST("/simulate-deposit", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
	}

	// Card Routes (mounted in transfers-svc only when a card issuer is wired)
	cards := router.Group("/cards")
	{
		cards.GET("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		cards.POST("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		cards.GET("/:id", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		cards.POST("/:id/details", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		cards.POST("/:id/freeze", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		cards.POST("/:id/unfreeze", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		cards.DELETE("/:id", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		cards.GET("/:id/transactions", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		cards.POST("/:id/delivery", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
	}

	// Spending Controls Routes (card spend limits)
	spendingControls := router.Group("/spending-controls")
	{
		spendingControls.GET("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		spendingControls.POST("", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		spendingControls.DELETE("/:id", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
		spendingControls.POST("/:id/override", func(c *gin.Context) { transfersProxy.ProxyRequest(c, cfg.Services.Transfers.HTTPBaseURL) })
	}

	// VAS Routes (Airtime, Data, Bills)
	vas := router.Group("/vas")
	{
		vas.POST("/airtime", func(c *gin.Context) {
			vasProxy.ProxyRequest(c, cfg.Services.VAS.HTTPBaseURL)
		})
		vas.POST("/data", func(c *gin.Context) {
			vasProxy.ProxyRequest(c, cfg.Services.VAS.HTTPBaseURL)
		})
		vas.POST("/bills/electricity", func(c *gin.Context) {
			vasProxy.ProxyRequest(c, cfg.Services.VAS.HTTPBaseURL)
		})
		vas.POST("/bills/cable-tv", func(c *gin.Context) {
			vasProxy.ProxyRequest(c, cfg.Services.VAS.HTTPBaseURL)
		})
		vas.GET("/providers", func(c *gin.Context) {
			vasProxy.ProxyRequest(c, cfg.Services.VAS.HTTPBaseURL)
		})
		vas.GET("/transactions", func(c *gin.Context) {
			vasProxy.ProxyRequest(c, cfg.Services.VAS.HTTPBaseURL)
		})
	}

	// Vault/Savings Routes
	vault := router.Group("/vault")
	{
		vault.POST("/create", func(c *gin.Context) {
			vasProxy.ProxyRequest(c, cfg.Services.VAS.HTTPBaseURL)
		})
		vault.POST("/deposit", func(c *gin.Context) {
			vasProxy.ProxyRequest(c, cfg.Services.VAS.HTTPBaseURL)
		})
		vault.POST("/withdraw", func(c *gin.Context) {
			vasProxy.ProxyRequest(c, cfg.Services.VAS.HTTPBaseURL)
		})
		vault.GET("/", func(c *gin.Context) {
			vasProxy.ProxyRequest(c, cfg.Services.VAS.HTTPBaseURL)
		})
	}

	// Notification Routes
	notification := router.Group("/notifications")
	{
		notification.GET("/", func(c *gin.Context) {
			notificationProxy.ProxyRequest(c, cfg.Services.Notification.HTTPBaseURL)
		})
		notification.PUT("/preferences", func(c *gin.Context) {
			notificationProxy.ProxyRequest(c, cfg.Services.Notification.HTTPBaseURL)
		})
		notification.PUT("/:id/read", func(c *gin.Context) {
			notificationProxy.ProxyRequest(c, cfg.Services.Notification.HTTPBaseURL)
		})
	}
}
