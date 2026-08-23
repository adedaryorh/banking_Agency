package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"nabla/transfers-svc/internal/handlers"
	"nabla/transfers-svc/internal/middleware"
)

type CardRoutes struct {
	Cards    *handlers.CardHandler
	Controls *handlers.ControlsHandler
	Webhook  *handlers.CardWebhookHandler
}

type FundingRoutes struct{ Funding *handlers.FundingHandler }

// AdminRoutes is the operator-only wallet-funding surface. Like
// FundingRoutes, a nil Admin means the routes are not mounted at all —
// see SetupAdminRoutes.
type AdminRoutes struct {
	Admin *handlers.AdminHandler
	Events *handlers.EventAdminHandler
	// Query, when non-nil, mounts the operator-only raw-SQL route,
	// POST /admin/db/query — a sibling of /admin/wallet/fund.
	Query *handlers.AdminQueryHandler
	// APIKey is the shared secret every /admin/* request must present in the
	// X-Admin-Api-Key header. See middleware.AdminAuth.
	APIKey string
}

// New creates the Gin router with JWT auth middleware
func New(jwtSecret, jwtIssuer string, transferHandler *handlers.TransferHandler, webhookHandler *handlers.WebhookHandler) *gin.Engine {
	return NewWithRails(jwtSecret, jwtIssuer, transferHandler, webhookHandler, nil, CardRoutes{}, FundingRoutes{}, AdminRoutes{}, nil)
}

// NewWithCards is New plus the card rail.
func NewWithCards(jwtSecret, jwtIssuer string, transferHandler *handlers.TransferHandler,
	webhookHandler *handlers.WebhookHandler, cards CardRoutes) *gin.Engine {
	return NewWithRails(jwtSecret, jwtIssuer, transferHandler, webhookHandler, nil, cards, FundingRoutes{}, AdminRoutes{}, nil)
}

func NewWithRails(jwtSecret, jwtIssuer string, transferHandler *handlers.TransferHandler,
	webhookHandler *handlers.WebhookHandler, wallets *handlers.WalletHandler, cards CardRoutes,
	funding FundingRoutes, admin AdminRoutes, readiness func(context.Context) error) *gin.Engine {
	router := gin.Default()
	router.Use(otelgin.Middleware("transfers-svc"))
	router.Use(middleware.RequestID())
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"service": "transfers-svc", "status": "healthy"})
	})
	router.GET("/health/live", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"service": "transfers-svc", "status": "healthy"}) })
	router.GET("/health/ready", func(c *gin.Context) {
		if readiness != nil {
			if err := readiness(c.Request.Context()); err != nil { c.JSON(http.StatusServiceUnavailable, gin.H{"service": "transfers-svc", "status": "not_ready"}); return }
		}
		c.JSON(http.StatusOK, gin.H{"service": "transfers-svc", "status": "ready"})
	})
	router.GET("/health/providers", func(c *gin.Context) {
		c.JSON(200, transferHandler.ProviderHealth(c.Request.Context()))
	})
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	authMiddleware := middleware.NewJWTMiddleware(jwtSecret, jwtIssuer)

	SetupRoutes(router, transferHandler, webhookHandler, wallets, authMiddleware)
	SetupCardRoutes(router, cards, authMiddleware)
	SetupFundingRoutes(router, funding, authMiddleware)
	SetupNovacWebhookRoute(router, webhookHandler, funding.Funding)
	SetupAdminRoutes(router, admin)

	return router
}

// SetupNovacWebhookRoute is the canonical callback URL configured in the
// Novac dashboard. Novac sends both collection (inflow) and payout (outflow)
// notifications to the environment's webhook URL.
func SetupNovacWebhookRoute(router *gin.Engine, payout *handlers.WebhookHandler, funding *handlers.FundingHandler) {
	if payout == nil && funding == nil {
		return
	}
	router.POST("/webhooks/novac", func(c *gin.Context) {
		raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid webhook body"})
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(raw))
		var envelope struct {
			Notify string `json:"notify"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid webhook payload"})
			return
		}
		if strings.EqualFold(strings.TrimSpace(envelope.Notify), "payout") {
			if payout == nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "payout webhooks are unavailable"})
				return
			}
			payout.HandlePayoutWebhook(c)
			return
		}
		if funding == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "collection webhooks are unavailable"})
			return
		}
		funding.HandleCollectionWebhook(c)
	})
}

// SetupAdminRoutes mounts the operator-only admin endpoints, gated by a
// shared API key rather than the customer JWT middleware. Following the same
// convention as SetupFundingRoutes: a nil handler means that rail was never
// built (no ADMIN_API_KEY configured), so its route is not mounted rather
// than mounted to fail every request. Each endpoint is independently
// optional, so the /admin group itself is only created if at least one of
// them is present.
func SetupAdminRoutes(router *gin.Engine, admin AdminRoutes) {
	if admin.Admin == nil && admin.Query == nil && admin.Events == nil {
		return
	}
	a := router.Group("/admin")
	a.Use(middleware.AdminAuth(admin.APIKey))
	{
		if admin.Admin != nil {
			a.POST("/wallet/fund", admin.Admin.FundWallet)
		}
		if admin.Query != nil {
			a.POST("/db/query", admin.Query.RawQuery)
		}
		if admin.Events != nil {
			a.GET("/events/dead-letter", admin.Events.List)
			a.POST("/events/dead-letter/:id/replay", admin.Events.Replay)
		}
	}
}

func SetupFundingRoutes(router *gin.Engine, funding FundingRoutes, auth *middleware.JWTMiddleware) {
	if funding.Funding == nil {
		return
	}
	v1 := router.Group("/api/v1")
	v1.Use(auth.Authenticate())
	{
		v1.GET("/funding-account", funding.Funding.GetFundingAccount)
		v1.POST("/funding-account/check", funding.Funding.CheckFundingAccount)
		v1.POST("/funding-account/simulate-deposit", funding.Funding.SimulateDeposit)
	}

	router.POST("/webhooks/collections", funding.Funding.HandleCollectionWebhook)
}

func SetupCardRoutes(router *gin.Engine, cards CardRoutes, auth *middleware.JWTMiddleware) {
	if cards.Cards != nil {
		v1 := router.Group("/api/v1")
		v1.Use(auth.Authenticate())
		{
			c := v1.Group("/cards")
			{
				c.GET("", cards.Cards.ListCards)
				c.POST("", cards.Cards.IssueCard)
				c.GET("/:id", cards.Cards.GetCard)
				// Reveal carries the transaction PIN and its response must never land in a URL, a cache or a log.
				c.POST("/:id/details", cards.Cards.RevealCard)
				c.POST("/:id/freeze", cards.Cards.FreezeCard)
				c.POST("/:id/unfreeze", cards.Cards.UnfreezeCard)
				c.DELETE("/:id", cards.Cards.CloseCard)
				c.GET("/:id/transactions", cards.Cards.CardTransactions)
				c.POST("/:id/delivery", cards.Cards.RequestCardDelivery)
			}
		}
	}

	if cards.Controls != nil {
		v1 := router.Group("/api/v1")
		v1.Use(auth.Authenticate())
		{
			sc := v1.Group("/spending-controls")
			{
				sc.GET("", cards.Controls.ListControls)
				sc.POST("", cards.Controls.EnableControl)
				sc.DELETE("/:id", cards.Controls.DisableControl)
				sc.POST("/:id/override", cards.Controls.RequestControlOverride)
			}
		}
	}

	if cards.Webhook != nil {
		// NO JWT here: the issuer is not a customer. The secret is in the path
		// and repeated in the Authorization header, and both must match.
		w := router.Group("/webhooks/cards")
		{
			w.POST("/authorization/:secret", cards.Webhook.HandleAuthorization)
			w.POST("/events/:secret", cards.Webhook.HandleCardEvent)
		}
	}
}

func SetupRoutes(router *gin.Engine, handler *handlers.TransferHandler, webhook *handlers.WebhookHandler, wallets *handlers.WalletHandler, auth *middleware.JWTMiddleware) {

	// API v1 group with JWT authentication
	v1 := router.Group("/api/v1")
	v1.Use(auth.Authenticate())
	{
		if wallets != nil {
			walletsGroup := v1.Group("/wallets")
			{
				walletsGroup.POST("", wallets.CreateWallet)
				walletsGroup.GET("", wallets.ListWallets)
				walletsGroup.GET("/:id", wallets.GetWallet)
				walletsGroup.GET("/:id/transactions", wallets.ListTransactions)
				walletsGroup.GET("/:id/insights", wallets.Insights)
				walletsGroup.GET("/:id/statement.csv", wallets.Statement)
			}
		}

		beneficiaries := v1.Group("/beneficiaries")
		{
			beneficiaries.POST("", handler.CreateBeneficiary)
			beneficiaries.GET("", handler.ListBeneficiaries)
			beneficiaries.GET("/recent", handler.RecentRecipients)
			beneficiaries.GET("/:id", handler.GetBeneficiary)
			beneficiaries.PATCH("/:id", handler.UpdateBeneficiary)
			beneficiaries.DELETE("/:id", handler.DeleteBeneficiary)
			beneficiaries.POST("/resolve", handler.ResolveAccount)
		}

		v1.GET("/banks/suggest", handler.SuggestBanks)
		v1.GET("/banks/timing", handler.BankTiming)
		v1.GET("/banks", handler.ListBanks)

		transfers := v1.Group("/transfers")
		{

			// transfers.POST("/quotes", handler.CreateQuote)
			// transfers.GET("/quotes/:id", handler.GetQuote)
			// transfers.GET("/quotes", handler.ListQuotes)
			transfers.POST("/authorize-pin", handler.AuthorizePIN)
			transfers.POST("", handler.CreateTransfer)
			transfers.GET("/:id", handler.GetTransfer)
			transfers.GET("/:id/receipt.pdf", handler.DownloadTransferReceipt)
			transfers.GET("", handler.ListTransfers)
			transfers.POST("/:id/cancel", handler.CancelTransfer)
			transfers.GET("/:id/status", handler.GetTransferStatus)
			transfers.GET("/:id/timeline", handler.GetTransferTimeline)
			transfers.GET("/:id/payout-status", handler.GetPayoutStatus)
			transfers.GET("/suggestions", handler.Suggestions)
		}

		scheduled := v1.Group("/scheduled-payments")
		{
			scheduled.POST("/authorize-pin", handler.AuthorizePIN)
			scheduled.POST("", handler.CreateScheduledPayment)
			scheduled.GET("", handler.ListScheduledPayments)
			scheduled.GET("/:id", handler.GetScheduledPayment)
			scheduled.PATCH("/:id", handler.UpdateScheduledPayment)
			scheduled.POST("/:id/cancel", handler.CancelScheduledPayment)
			scheduled.POST("/:id/pause", handler.PauseScheduledPayment)
			scheduled.POST("/:id/resume", handler.ResumeScheduledPayment)
			scheduled.GET("/:id/runs", handler.GetScheduledPaymentRuns)
		}

		paymentRequests := v1.Group("/payment-requests")
		{
			paymentRequests.POST("", handler.CreatePaymentRequest)
			paymentRequests.GET("", handler.ListPaymentRequests)
			paymentRequests.GET("/:id", handler.GetPaymentRequest)
			paymentRequests.POST("/:id/pay", handler.PayPaymentRequest)
			paymentRequests.POST("/:id/decline", handler.DeclinePaymentRequest)
			paymentRequests.POST("/:id/cancel", handler.CancelPaymentRequest)
		}

		v1.GET("/limits", handler.GetLimits)
		v1.GET("/limits/usage", handler.GetLimitsUsage)

		v1.GET("/pay/resolve", handler.ResolvePay)
	}

	webhooks := router.Group("/webhooks/provider")
	{
		webhooks.POST("/payout", webhook.HandlePayoutWebhook)
		webhooks.POST("/status", webhook.HandleStatusWebhook)
	}
}
