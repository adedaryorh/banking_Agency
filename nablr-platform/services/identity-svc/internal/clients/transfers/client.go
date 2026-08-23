// Package transfers is a thin gRPC client identity-svc uses to ask
// transfers-svc about a customer's wallet balance. It mirrors the client
// pattern transfers-svc itself uses to call identity-svc
// (internal/clients/identity in that service): a shared "x-service-auth"
// token, a short per-call timeout, and gRPC status codes translated into
// sentinel errors the caller can compare with errors.Is.
package transfers

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"nabla/identity-svc/internal/models"
	pb "nabla/identity-svc/proto/transfers"
)

// callTimeout bounds every transfers call so a slow/unavailable dependency
// can't stall an account-closure request indefinitely.
const callTimeout = 5 * time.Second
const provisionTimeout = 35 * time.Second

var ErrUnavailable = errors.New("transfers service unavailable")

type Client struct {
	conn  *grpc.ClientConn
	rpc   pb.TransfersServiceClient
	token string // sent as x-service-auth; transfers-svc checks it against API_SECRET
}

func Dial(url, token string) (*Client, error) {
	conn, err := grpc.NewClient(url,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, rpc: pb.NewTransfersServiceClient(conn), token: token}, nil
}

func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) callCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx = metadata.AppendToOutgoingContext(ctx, "x-service-auth", c.token)
	if requestID := models.MetadataFromContext(ctx).RequestID; requestID != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-request-id", requestID)
	}
	return context.WithTimeout(ctx, callTimeout)
}

// Wallet is the balance snapshot identity-svc needs to gate account closure.
type Wallet struct {
	AvailableMinor int64
	ReservedMinor  int64
	Status         string
}

// Zero reports whether the wallet has nothing sitting in it, available or
// reserved. A wallet that doesn't exist yet for this currency counts as
// zero: there is nothing to withdraw first.
func (w Wallet) Zero() bool {
	return w.AvailableMinor == 0 && w.ReservedMinor == 0
}

// GetWallet fetches the user's balance in the given currency (e.g. "NGN").
// A wallet that has never been funded is reported by transfers-svc as
// "not found" rather than a zero-balance record, so that case is treated as
// a zero wallet here rather than an error.
func (c *Client) GetWallet(ctx context.Context, userID uuid.UUID, currency string) (*Wallet, error) {
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.rpc.GetWallet(cctx, &pb.GetWalletRequest{UserId: userID.String(), Currency: currency})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return &Wallet{}, nil
		}
		return nil, errors.Join(ErrUnavailable, err)
	}
	return &Wallet{
		AvailableMinor: resp.GetAvailableMinor(),
		ReservedMinor:  resp.GetReservedMinor(),
		Status:         resp.GetStatus(),
	}, nil
}

// ProvisionCustomer idempotently opens the customer's wallet and permanent
// funding account after onboarding is completed.
func (c *Client) ProvisionCustomer(ctx context.Context, userID uuid.UUID, currency string) error {
	ctx = metadata.AppendToOutgoingContext(ctx, "x-service-auth", c.token)
	if requestID := models.MetadataFromContext(ctx).RequestID; requestID != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-request-id", requestID)
	}
	cctx, cancel := context.WithTimeout(ctx, provisionTimeout)
	defer cancel()
	_, err := c.rpc.ProvisionCustomer(cctx, &pb.ProvisionCustomerRequest{
		UserId: userID.String(), Currency: currency,
	})
	if err != nil {
		return errors.Join(ErrUnavailable, err)
	}
	return nil
}
