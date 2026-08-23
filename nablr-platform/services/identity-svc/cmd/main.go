package main

import (
	"context"
	"log"
	"nabla/identity-svc/internal/alerting"
	"nabla/identity-svc/internal/bootstrap"
	"nabla/identity-svc/internal/clients/transfers"
	"nabla/identity-svc/internal/config"
	"nabla/identity-svc/internal/controllers"
	"nabla/identity-svc/internal/grpc/server"
	"nabla/identity-svc/internal/handlers"
	identityrabbit "nabla/identity-svc/internal/messaging/rabbitmq"
	"nabla/identity-svc/internal/routes"
	"nabla/identity-svc/internal/telemetry"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	repo "nabla/identity-svc/internal/repository"

	pb "nabla/identity-svc/proto/identity"
)

// @title Nabla Identity Service API
// @version 1.0
// @description Identity, authentication, onboarding, KYC, device, and security APIs.
// @BasePath /
// @schemes http https
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	shutdownTelemetry, err := telemetry.Setup(context.Background(), "identity-svc")
	if err != nil {
		log.Printf("configure telemetry: %v", err)
		shutdownTelemetry = func(context.Context) error { return nil }
	}
	defer shutdownTelemetry(context.Background())

	alerts := alerting.New("identity-svc")
	db, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("configure database: %v", err)
	}
	defer db.Close()
	if err := db.Ping(context.Background()); err != nil {
		log.Fatalf("connect database: %v", err)
	}

	store := repo.NewStore(db)
	audit := controllers.NewAuditController(store)
	outbox := controllers.NewOutboxController(store)
	appContext, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()
	if cfg.RabbitMQURL == "" {
		log.Print("identity outbox relay disabled (RABBITMQ_URL unset)")
	} else {
		publisher := identityrabbit.NewPublisher(cfg.RabbitMQURL)
		defer publisher.Close()
		relay := controllers.NewOutboxRelay(store, publisher, controllers.OutboxRelayConfig{})
		go func() {
			log.Print("identity outbox relay started (RabbitMQ)")
			relay.Run(appContext)
			log.Print("identity outbox relay stopped")
		}()
	}

	providerSet, err := bootstrap.BuildProviders(context.Background(), cfg)
	if err != nil {
		log.Fatal(err)
	}

	otp := controllers.NewOTPController(store, providerSet.SMS, providerSet.Email, audit)
	kyc := controllers.NewKYCController(store, providerSet.Identity, providerSet.Directory, providerSet.Cipher, audit, outbox)
	security := controllers.NewSecurityController(store, otp, audit, outbox, 0)
	limits := controllers.NewLimitController(store, audit)
	authConfig := controllers.AuthConfig{
		JWTSecret:            cfg.JWTSecret,
		JWTIssuer:            cfg.JWTIssuer,
		AccessTokenTTL:       15 * time.Minute,
		RefreshTokenTTL:      30 * 24 * time.Hour,
		VerificationTokenTTL: 24 * time.Hour,
		PasswordResetTTL:     time.Hour,
		MagicLinkTTL:         15 * time.Minute,
		OnboardingTokenTTL:   30 * time.Minute,
	}

	// Account closure gates on a zero wallet balance, checked live against
	// transfers-svc. A dial failure here doesn't stop identity-svc from
	// starting — ConfirmAccountClosure fails closed (ErrProviderUnavailable)
	// if this stays nil, so closure just isn't available until transfers-svc
	// is reachable.
	var transfersWallets controllers.TransfersWalletProvider
	transfersClient, err := transfers.Dial(cfg.TransfersGRPCURL, cfg.APISecret)
	if err != nil {
		log.Printf("dial transfers-svc at %s: %v (account closure will be unavailable)", cfg.TransfersGRPCURL, err)
	} else {
		transfersWallets = transfersClient
		defer transfersClient.Close()
	}

	auth := controllers.NewAuthController(
		repo.NewAuthRepository(db),
		otp,
		controllers.NewEmailTokenDelivery(providerSet.Email, cfg.FrontendURL),
		nil,
		authConfig,
		alerts,
		kyc,
		transfersWallets,
	)
	tier3Documents := controllers.NewTier3DocumentController(store, providerSet.Storage)
	avatarImages := controllers.NewAvatarController(store, providerSet.Storage)
	waitlist := controllers.NewWaitlistController(store, controllers.NewEmailWaitlistDelivery(providerSet.Email), alerts)

	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	var adminQuery *handlers.AdminQueryHandler
	if cfg.AdminAPIKey != "" {
		adminQuery = handlers.NewAdminQueryHandler(db)
		log.Print("admin routes mounted at /admin (ADMIN_API_KEY configured)")
	} else {
		log.Print("admin routes NOT mounted (ADMIN_API_KEY unset)")
	}

	router := routes.NewRouter(routes.Config{
		JWTSecret:      cfg.JWTSecret,
		JWTIssuer:      cfg.JWTIssuer,
		Auth:           handlers.NewAuthHandler(auth, kyc),
		Onboarding:     handlers.NewOnboardingHandler(auth, otp, kyc, security),
		Mobile:         handlers.NewMobileHandler(cfg),
		KYC:            handlers.NewKYCHandler(kyc, limits),
		Security:       handlers.NewSecurityHandler(security, otp, kyc),
		Tier3:          handlers.NewTier3DocumentHandler(tier3Documents),
		Waitlist:       handlers.NewWaitlistHandler(waitlist),
		Alerts:         alerts,
		Avatar:         handlers.NewAvatarHandler(avatarImages),
		AllowedOrigins: cfg.CORSAllowedOrigins,
		EnableHSTS:     cfg.IsProduction(),
		AdminQuery:     adminQuery,
		AdminAPIKey:    cfg.AdminAPIKey,
		Readiness:      db.Ping,
	})

	httpServer := &http.Server{Addr: cfg.HTTPAddress, Handler: router, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve HTTP: %v", err)
		}
	}()

	grpcServer := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
	pb.RegisterIdentityServiceServer(grpcServer, server.NewIdentityServer(store, security, cfg.APISecret))
	grpcListener, err := net.Listen("tcp", cfg.GRPCAddress)
	if err != nil {
		log.Fatalf("listen gRPC: %v", err)
	}
	go func() {
		if err := grpcServer.Serve(grpcListener); err != nil {
			log.Fatalf("serve gRPC: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	cancelApp()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdown)
}
