package notification

import (
	"context"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"nabla/transfers-svc/internal/observability"
	"nabla/transfers-svc/internal/service"
	pb "nabla/transfers-svc/proto/notification"
)

const callTimeout = 10 * time.Second

type Client struct {
	conn  *grpc.ClientConn
	rpc   pb.NotificationServiceClient
	token string // x-service-auth; notification checks it against INTERNAL_SERVICE_TOKEN
}

var _ service.Notifier = (*Client)(nil)

func Dial(url, token string) (*Client, error) {
	conn, err := grpc.NewClient(url,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, rpc: pb.NewNotificationServiceClient(conn), token: token}, nil
}

func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) callCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx = metadata.AppendToOutgoingContext(ctx, "x-service-auth", c.token)
	if correlationID := observability.CorrelationID(ctx); correlationID != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-request-id", correlationID)
	}
	return context.WithTimeout(ctx, callTimeout)
}

func (c *Client) SendSMS(ctx context.Context, to, body string) (bool, error) {
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.rpc.SendSMS(cctx, &pb.SendSMSRequest{To: to, Body: body})
	if err != nil {
		return false, err
	}
	return resp.GetSuccess(), nil
}

func (c *Client) SendEmail(ctx context.Context, to, subject, text, html string) (bool, error) {
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.rpc.SendEmail(cctx, &pb.SendEmailRequest{To: to, Subject: subject, Text: text, Html: html})
	if err != nil {
		return false, err
	}
	return resp.GetSuccess(), nil
}
