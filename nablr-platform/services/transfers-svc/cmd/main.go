package main

import (
	"context"
	"log"
	"nabla/transfers-svc/internal/config"
	"nabla/transfers-svc/internal/events"
	"nabla/transfers-svc/internal/handlers"
	"nabla/transfers-svc/internal/platform/clock"
	"nabla/transfers-svc/internal/platform/crypto"
	"nabla/transfers-svc/internal/providers"
	"nabla/transfers-svc/internal/providers/novac"
	"nabla/transfers-svc/internal/providers/sudo"
	"nabla/transfers-svc/internal/routes"
	"nabla/transfers-svc/internal/service"
	"nabla/transfers-svc/internal/telemetry"
	"nabla/transfers-svc/internal/worker"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	"github.com/google/uuid"

	identityclient "nabla/transfers-svc/internal/clients/identity"
	notificationclient "nabla/transfers-svc/internal/clients/notification"

	grpcserver "nabla/transfers-svc/internal/grpc/server"

	platformdb "nabla/transfers-svc/internal/platform/db"

	pb "nabla/transfers-svc/proto/transfers"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	shutdownTelemetry, err := telemetry.Setup(context.Background(), "transfers-svc")
	if err != nil {
		log.Printf("configure telemetry: %v", err)
		shutdownTelemetry = func(context.Context) error { return nil }
	}
	defer shutdownTelemetry(context.Background())

	pool, e := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if e != nil {
		log.Fatal(e)
	}
	defer pool.Close()
	if e = pool.Ping(context.Background()); e != nil {
		log.Fatal(e)
	}

	payout := payoutProvider(cfg)
	log.Printf("payout rail: %s", payout.Info().Name)

	collections := collectionProvider(cfg)

	// Account numbers are never stored in plaintext: they feed the
	// uq_beneficiaries_account constraint via a blind index and are held as
	// AES-256-GCM ciphertext.
	enc, bi, err := crypto.FromEnv()
	if err != nil {
		log.Fatal(err)
	}

	// ONE money engine for the whole process.
	// HTTP, gRPC and the worker are transports over this single Service.
	svc := service.NewWithProviderAndCrypto(pool, payout, enc, bi)

	idClient, closeID := identityClient(cfg)
	defer closeID()
	notifClient, closeNotif := notificationClient(cfg)
	defer closeNotif()
	svc.WithClients(idClient, notifClient)

	transferHandler := handlers.NewTransferHandler(svc)
	novacWebhookGuard := handlers.NewWebhookSourceGuard(
		cfg.NovacWebhookAllowedIPs, cfg.NovacWebhookTrustProxy)
	webhookHandler := handlers.NewWebhookHandler(svc, cfg.WebhookSecret, novacWebhookGuard)

	raw := platformdb.NewAdapter(pool)
	clk := clock.RealClock{}
	wallets := service.NewWalletService(raw, clk)
	walletHandler := handlers.NewWalletHandler(wallets)
	var funding *service.FundingService
	if collections != nil {
		funding = service.NewFundingService(raw, collections,
			service.NewLedgerService(raw, clk), wallets,
			service.NewLimitsService(raw), clk)
		funding.WithIdentity(idClient)
	}
	var identityEvents *events.IdentityConsumer
	if cfg.RabbitMQURL != "" {
		identityEvents = events.NewIdentityConsumer(cfg.RabbitMQURL, pool, wallets, funding)
	}

	cardRoutes := cardRail(cfg, pool, idClient)
	fundingRoutes := fundingRail(funding, novacWebhookGuard)
	adminRoutes := adminRail(cfg, pool, identityEvents)

	ginRouter := routes.NewWithRails(cfg.JWTSecret, cfg.JWTIssuer,
		transferHandler, webhookHandler, walletHandler, cardRoutes, fundingRoutes, adminRoutes, pool.Ping)

	srv := &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           ginRouter,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		if e := srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
			log.Fatal(e)
		}
	}()
	log.Printf("http listening on %s", cfg.HTTPAddress)

	listener, e := net.Listen("tcp", cfg.GRPCAddress)
	if e != nil {
		log.Fatal(e)
	}
	grpcSrv := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
	pb.RegisterTransfersServiceServer(grpcSrv, grpcserver.New(svc, wallets, funding, cfg.APISecret))
	go func() {
		if e := grpcSrv.Serve(listener); e != nil {
			log.Fatal(e)
		}
	}()
	log.Printf("grpc listening on %s", cfg.GRPCAddress)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-stop
		log.Print("shutdown signal received")
		cancel()
	}()
	if identityEvents == nil {
		log.Print("identity event consumer disabled (RABBITMQ_URL unset)")
	} else {
		go func() {
			log.Print("identity event consumer started (RabbitMQ)")
			identityEvents.Run(ctx)
			log.Print("identity event consumer stopped")
		}()
	}

	if cfg.RunWorker {
		go func() {
			log.Print("outbox worker started in-process")
			worker.New(svc, time.Second, funding).Run(ctx)
			log.Print("outbox worker stopped")
		}()
	} else {
		log.Print("outbox worker disabled (RUN_WORKER=false) — run cmd/worker separately")
	}

	<-ctx.Done()
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	grpcSrv.GracefulStop()
	_ = srv.Shutdown(ctx)
}

// cardRail builds the card issuing surface: the issuer, the cards service and
// the three handlers over it.
//
// Everything here is optional and fails SOFT in one direction only. With no
// SUDO_API_KEY the mock issuer is used, which is right for development and
// asks for no national identity number. With no SUDO_WEBHOOK_SECRET the
// callbacks are NOT mounted at all rather than mounted open — an endpoint that
// approves card spending must not exist because a setting was missed.
func cardRail(cfg *config.Config, pool *pgxpool.Pool, identity service.IdentityClient) routes.CardRoutes {
	raw := platformdb.NewAdapter(pool)
	clk := clock.RealClock{}

	ledger := service.NewLedgerService(raw, clk)
	wallets := service.NewWalletService(raw, clk)
	controls := service.NewControlsService(raw, clk)
	limits := service.NewLimitsService(raw)

	// The default issuer. The mock declares no cardholder-KYC capability, so
	// nothing asks a development customer for a NIN.
	var issuer providers.CardIssuer = providers.NewMockCardIssuer()

	cards := service.NewCardsService(raw, issuer, ledger, wallets, controls, limits, clk)
	cards.WithIdentity(identity)

	// The real issuer, gated to the customers named in SUDO_ALLOWED_CUSTOMERS.
	//
	// A card rollout begins with somebody, not with everybody: a real card
	// issued to a customer nobody expected to give one to is much harder to
	// take back than it is to withhold. An empty list means nobody.
	if cfg.SudoAPIKey != "" {
		real, err := sudo.New(sudo.Config{
			BaseURL:         cfg.SudoAPIURL,
			APIKey:          cfg.SudoAPIKey,
			FundingSourceID: cfg.SudoFundingSourceID,
			DebitAccountID:  cfg.SudoDebitAccountID,
			Brand:           cfg.SudoBrand,
			Sandbox:         cfg.SudoSandbox,
		})
		if err != nil {
			log.Fatalf("sudo card issuer: %v", err)
		}
		allowList := cfg.SudoAllowedCustomers
		cards.SetPreferredIssuer(real, func(ctx context.Context, customerID uuid.UUID) bool {
			return handlers.AllowListMatches(allowList, customerID.String())
		})
		log.Printf("card issuer: sudo (sandbox=%t) for %q; everyone else on the mock",
			cfg.SudoSandbox, allowList)
	} else {
		log.Print("card issuer: mock (SUDO_API_KEY unset)")
	}

	out := routes.CardRoutes{
		Cards:    handlers.NewCardHandler(cards, identity),
		Controls: handlers.NewControlsHandler(controls),
	}
	if cfg.SudoWebhookSecret != "" {
		out.Webhook = handlers.NewCardWebhookHandler(cards, cfg.SudoWebhookSecret)
	} else {
		log.Print("card webhooks NOT mounted (SUDO_WEBHOOK_SECRET unset)")
	}
	return out
}

// collectionProvider is the money-in rail, or nil.
//
// Nil is a real answer and not a failure: without a virtual-account bank code
// there is nowhere to open funding accounts, and the endpoints are then not
// mounted at all rather than mounted to fail.
func collectionProvider(cfg *config.Config) providers.CollectionProvider {
	if cfg.MockCollections {
		log.Print("collections rail: mock (MOCK_COLLECTIONS=true) — simulated deposits only")
		return providers.NewMockCollectionsProvider()
	}
	if cfg.NovacAPIKey == "" && cfg.NovacAPISecret == "" {
		log.Print("collections disabled (no Novac credentials)")
		return nil
	}
	if cfg.NovacVABankCode == "" {
		log.Print("collections disabled (NOVAC_VA_BANK_CODE unset) — " +
			"list the issuers your key may use with novac.Client.VirtualAccountBanks")
		return nil
	}
	c := novac.New(novac.Config{
		BaseURL:                cfg.NovacAPIURL,
		APIKey:                 cfg.NovacAPIKey,
		APISecret:              cfg.NovacAPISecret,
		VirtualAccountBankCode: cfg.NovacVABankCode,
		Sandbox:                cfg.NovacSandbox,
		Timeout:                30 * time.Second,
	})
	log.Printf("collections rail: novac (sandbox=%t) issuing at bank %s",
		c.Info().Sandbox, cfg.NovacVABankCode)
	return c
}

// fundingRail builds the inbound money path over the same single balance the
// transfer and card rails spend from.
func fundingRail(funding *service.FundingService, guard *handlers.WebhookSourceGuard) routes.FundingRoutes {
	if funding == nil {
		return routes.FundingRoutes{}
	}
	return routes.FundingRoutes{Funding: handlers.NewFundingHandler(funding, guard)}
}

// adminRail builds the operator-only wallet-funding surface. Like
// fundingRail, absence is a real answer: with no ADMIN_API_KEY configured
// the routes are not mounted at all rather than mounted with an empty key
// that would refuse everyone but still advertise the endpoint's existence.
func adminRail(cfg *config.Config, pool *pgxpool.Pool, identityEvents *events.IdentityConsumer) routes.AdminRoutes {
	if cfg.AdminAPIKey == "" {
		log.Print("admin routes NOT mounted (ADMIN_API_KEY unset)")
		return routes.AdminRoutes{}
	}
	raw := platformdb.NewAdapter(pool)
	clk := clock.RealClock{}
	admin := service.NewAdminService(raw,
		service.NewLedgerService(raw, clk),
		service.NewWalletService(raw, clk), clk)

	log.Print("admin routes mounted at /admin (ADMIN_API_KEY configured)")
	return routes.AdminRoutes{
		Admin:  handlers.NewAdminHandler(admin),
		Query:  handlers.NewAdminQueryHandler(pool),
		Events: handlers.NewEventAdminHandler(identityEvents),
		APIKey: cfg.AdminAPIKey,
	}
}

func payoutProvider(cfg *config.Config) providers.PayoutProvider {
	if cfg.NovacAPIKey != "" || cfg.NovacAPISecret != "" {
		return novac.New(novac.Config{
			BaseURL:   cfg.NovacAPIURL,
			APIKey:    cfg.NovacAPIKey,
			APISecret: cfg.NovacAPISecret,
			Timeout:   30 * time.Second,
		})
	}
	return providers.NewMockProviderWithSink(nil, 200*time.Millisecond)
}

func identityClient(cfg *config.Config) (service.IdentityClient, func()) {
	if cfg.IdentityGRPCURL == "" {
		log.Print("identity gRPC client disabled (IDENTITY_GRPC_URL unset)")
		return nil, func() {}
	}
	log.Printf("identity gRPC config url=%s api_secret_configured=%t", cfg.IdentityGRPCURL, cfg.APISecret != "" && cfg.APISecret != "secret")
	c, err := identityclient.Dial(cfg.IdentityGRPCURL, cfg.APISecret)
	if err != nil {
		log.Fatalf("identity gRPC client: %v", err)
	}
	log.Printf("identity gRPC client -> %s (resilient: cache + breaker)", cfg.IdentityGRPCURL)
	return identityclient.NewResilient(c), func() { _ = c.Close() }
}

func notificationClient(cfg *config.Config) (service.Notifier, func()) {
	if cfg.NotificationGRPCURL == "" {
		log.Print("notification gRPC client disabled (NOTIFICATION_GRPC_URL unset)")
		return nil, func() {}
	}
	c, err := notificationclient.Dial(cfg.NotificationGRPCURL, cfg.InternalServiceToken)
	if err != nil {
		log.Fatalf("notification gRPC client: %v", err)
	}
	log.Printf("notification gRPC client -> %s", cfg.NotificationGRPCURL)
	return c, func() { _ = c.Close() }
}
