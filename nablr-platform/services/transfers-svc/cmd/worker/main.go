package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	identityclient "nabla/transfers-svc/internal/clients/identity"
	notificationclient "nabla/transfers-svc/internal/clients/notification"
	"nabla/transfers-svc/internal/config"
	"nabla/transfers-svc/internal/platform/clock"
	"nabla/transfers-svc/internal/platform/crypto"
	platformdb "nabla/transfers-svc/internal/platform/db"
	"nabla/transfers-svc/internal/providers"
	"nabla/transfers-svc/internal/providers/novac"
	"nabla/transfers-svc/internal/service"
	"nabla/transfers-svc/internal/worker"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}
	db, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	enc, bi, err := crypto.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	payout := payoutProvider(cfg)
	log.Printf("worker payout rail: %s", payout.Info().Name)

	s := service.NewWithProviderAndCrypto(db, payout, enc, bi)

	idClient, closeID := identityClient(cfg)
	defer closeID()
	notifClient, closeNotif := notificationClient(cfg)
	defer closeNotif()
	s.WithClients(idClient, notifClient)
	var funding *service.FundingService
	if collections := collectionProvider(cfg); collections != nil {
		raw := platformdb.NewAdapter(db)
		clk := clock.RealClock{}
		wallets := service.NewWalletService(raw, clk)
		funding = service.NewFundingService(raw, collections,
			service.NewLedgerService(raw, clk), wallets,
			service.NewLimitsService(raw), clk)
		funding.WithIdentity(idClient)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.New(s, time.Second, funding).Run(ctx)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
}

func collectionProvider(cfg *config.Config) providers.CollectionProvider {
	if cfg.MockCollections {
		return providers.NewMockCollectionsProvider()
	}
	if (cfg.NovacAPIKey == "" && cfg.NovacAPISecret == "") || cfg.NovacVABankCode == "" {
		return nil
	}
	return novac.New(novac.Config{
		BaseURL: cfg.NovacAPIURL, APIKey: cfg.NovacAPIKey, APISecret: cfg.NovacAPISecret,
		VirtualAccountBankCode: cfg.NovacVABankCode, Sandbox: cfg.NovacSandbox,
		Timeout: 30 * time.Second,
	})
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
	// The mock delivers "webhook" status updates in-process so the whole
	// settle/fail path is exercised without a network rail.
	return providers.NewMockProviderWithSink(nil, 200*time.Millisecond)
}

// identityClient / notificationClient build the internal gRPC clients,
func identityClient(cfg *config.Config) (service.IdentityClient, func()) {
	if cfg.IdentityGRPCURL == "" {
		log.Print("worker: identity gRPC client disabled (IDENTITY_GRPC_URL unset)")
		return nil, func() {}
	}
	c, err := identityclient.Dial(cfg.IdentityGRPCURL, cfg.APISecret)
	if err != nil {
		log.Fatalf("worker: identity gRPC client: %v", err)
	}
	log.Printf("worker: identity gRPC client -> %s (resilient: cache + breaker)", cfg.IdentityGRPCURL)
	return identityclient.NewResilient(c), func() { _ = c.Close() }
}

func notificationClient(cfg *config.Config) (service.Notifier, func()) {
	if cfg.NotificationGRPCURL == "" {
		log.Print("worker: notification gRPC client disabled (NOTIFICATION_GRPC_URL unset)")
		return nil, func() {}
	}
	c, err := notificationclient.Dial(cfg.NotificationGRPCURL, cfg.InternalServiceToken)
	if err != nil {
		log.Fatalf("worker: notification gRPC client: %v", err)
	}
	log.Printf("worker: notification gRPC client -> %s", cfg.NotificationGRPCURL)
	return c, func() { _ = c.Close() }
}
