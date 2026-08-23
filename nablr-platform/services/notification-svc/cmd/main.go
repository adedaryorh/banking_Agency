package main

import (
	"context"
	"log"
	"nabla/notification-svc/internal/alerting"
	"nabla/notification-svc/internal/config"
	"nabla/notification-svc/internal/events"
	"nabla/notification-svc/internal/handlers"
	"nabla/notification-svc/internal/metrics"
	"nabla/notification-svc/internal/middleware"
	"nabla/notification-svc/internal/providers"
	"nabla/notification-svc/internal/retry"
	"nabla/notification-svc/internal/telemetry"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	notificationdb "nabla/notification-svc/db/sqlc"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	_ "nabla/notification-svc/docs"

	grpcserver "nabla/notification-svc/internal/grpc/server"

	emailintegration "nabla/notification-svc/internal/integrations/email"

	pb "nabla/notification-svc/proto/notification"
)

// @title Nabla Notification Service API
// @version 1.0
// @description Internal SMS, email, and push notification APIs.
// @BasePath /
// @schemes http https
// @securityDefinitions.apikey InternalServiceToken
// @in header
// @name X-Internal-Service-Token
func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}
	shutdownTelemetry, err := telemetry.Setup(context.Background(), "notification-svc")
	if err != nil {
		log.Printf("configure telemetry: %v", err)
		shutdownTelemetry = func(context.Context) error { return nil }
	}
	defer shutdownTelemetry(context.Background())

	alerts := alerting.New("notification-svc")
	db, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("configure database: %v", err)
	}
	defer db.Close()
	if err = db.Ping(context.Background()); err != nil {
		log.Fatalf("connect database: %v", err)
	}
	appContext, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()
	if cfg.RabbitMQURL == "" {
		log.Print("notification outbox relay disabled (RABBITMQ_URL unset)")
	} else {
		relay := events.NewRelay(db, cfg.RabbitMQURL)
		defer relay.Close()
		go func() {
			log.Print("notification outbox relay started (RabbitMQ)")
			relay.Run(appContext)
			log.Print("notification outbox relay stopped")
		}()
	}

	sms, err := providers.NewSMSProvider(providers.SMSConfig{Provider: cfg.SMSProvider, BaseURL: cfg.SMSBaseURL, SenderID: cfg.SMSSenderID, TermiiAPIKey: cfg.TermiiAPIKey, DigitalPulseAPIKey: cfg.DigitalPulseAPIKey, DigitalPulseRoute: cfg.DigitalPulseRoute, TwilioSID: cfg.TwilioSID, TwilioToken: cfg.TwilioToken, TwilioFrom: cfg.TwilioFrom})
	if err != nil {
		log.Fatalf("configure SMS: %v", err)
	}
	email, err := emailintegration.New(emailintegration.Config{Providers: cfg.EmailProviders, Environment: cfg.Environment, Timeout: 30 * time.Second, FromAddress: cfg.EmailFromAddress, FromName: cfg.EmailFromName, MailgunBaseURL: cfg.MailgunBaseURL, MailgunDomain: cfg.MailgunDomain, MailgunAPIKey: cfg.MailgunAPIKey, SendGridBaseURL: cfg.SendGridBaseURL, SendGridAPIKey: cfg.SendGridAPIKey, SMTPHost: cfg.SMTPHost, SMTPPort: cfg.SMTPPort, SMTPUsername: cfg.SMTPUsername, SMTPPassword: cfg.SMTPPassword})
	if err != nil {
		log.Fatalf("configure email: %v", err)
	}
	push, err := providers.NewPushProvider(context.Background(), providers.PushConfig{Provider: cfg.PushProvider, Environment: cfg.Environment, ConfigJSON: cfg.FirebaseConfigJSON, CredentialsBase64: cfg.FirebaseCredentials, CredentialsPath: cfg.FirebaseCredentialsPath})
	if err != nil {
		log.Fatalf("configure push: %v", err)
	}

	obs := metrics.New()
	eventRecorder := events.NewRecorder(notificationdb.New(db))
	sms = retry.WrapSMS(sms, retry.DefaultConfig(), obs)
	email = retry.WrapEmail(email, retry.DefaultConfig(), obs)
	push = retry.WrapPush(push, retry.DefaultConfig(), obs)

	log.Printf("notification service configured env=%s sms_provider=%s email_providers=%s push_provider=%s", cfg.Environment, sms.Name(), email.Name(), push.Name())

	router := gin.New()
	router.Use(otelgin.Middleware("notification-svc"))
	router.Use(middleware.RequestID(), middleware.StructuredLogger(alerts), middleware.Recovery(alerts))
	router.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"service": "notification-svc", "status": "healthy"}) })
	router.GET("/health/live", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"service": "notification-svc", "status": "healthy"}) })
	router.GET("/health/ready", func(c *gin.Context) {
		if err := db.Ping(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"service": "notification-svc", "status": "not_ready"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"service": "notification-svc", "status": "ready"})
	})
	router.GET("/metrics", gin.WrapH(obs.Handler()))
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	handlers.NewDeliveryHandler(cfg.InternalServiceToken, sms, email, push, eventRecorder).Register(router)

	server := &http.Server{Addr: cfg.HTTPAddress, Handler: router, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve HTTP: %v", err)
		}
	}()

	grpcServer := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
	pb.RegisterNotificationServiceServer(grpcServer, grpcserver.NewNotificationServer(cfg.InternalServiceToken, sms, email, push, eventRecorder))
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	grpcServer.GracefulStop()
}
