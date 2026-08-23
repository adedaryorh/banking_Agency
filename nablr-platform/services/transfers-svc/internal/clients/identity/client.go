package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"nabla/transfers-svc/internal/models"
	"nabla/transfers-svc/internal/observability"
	"nabla/transfers-svc/internal/service"
	pb "nabla/transfers-svc/proto/identity"
)

// callTimeout bounds every identity call. Identity sits on the authorization
const callTimeout = 5 * time.Second

type Client struct {
	conn  *grpc.ClientConn
	rpc   pb.IdentityServiceClient
	token string // sent as x-service-auth; identity checks it against API_SECRET
}

var _ service.IdentityClient = (*Client)(nil)

func Dial(url, token string) (*Client, error) {
	conn, err := grpc.NewClient(url,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, rpc: pb.NewIdentityServiceClient(conn), token: token}, nil
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

func (c *Client) VerifyPIN(ctx context.Context, userID uuid.UUID, pin string) (bool, error) {
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.rpc.VerifyPIN(cctx, &pb.VerifyPINRequest{UserId: userID.String(), Pin: pin})
	if err != nil {
		return false, classify(err)
	}
	if resp.GetValid() {
		return true, nil
	}
	switch msg := strings.ToLower(resp.GetMessage()); {
	case strings.Contains(msg, "lock"):
		return false, models.ErrPINLocked
	case strings.Contains(msg, "set a transaction pin"), strings.Contains(msg, "pin first"), strings.Contains(msg, "no pin"):
		return false, models.ErrPINNotSet
	default:
		return false, nil
	}
}

func (c *Client) GetUser(ctx context.Context, userID uuid.UUID) (service.UserProfile, error) {
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.rpc.GetUser(cctx, &pb.GetUserRequest{UserId: userID.String()})
	if err != nil {
		return service.UserProfile{}, classify(err)
	}
	return profileOf(resp), nil
}

func (c *Client) GetUserByUsername(ctx context.Context, username string) (service.UserProfile, error) {
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.rpc.GetUserByUsername(cctx, &pb.GetUserByUsernameRequest{Username: username})
	if err != nil {
		return service.UserProfile{}, classify(err)
	}
	return profileOf(resp), nil
}

func (c *Client) GetUserByAccountNumber(ctx context.Context, accountNumber string) (service.UserProfile, error) {
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.rpc.GetUserByAccountNumber(cctx, &pb.GetUserByAccountNumberRequest{AccountNumber: accountNumber})
	if err != nil {
		return service.UserProfile{}, classify(err)
	}
	return profileOf(resp), nil
}

func profileOf(resp *pb.GetUserResponse) service.UserProfile {
	return service.UserProfile{
		UserID:        resp.GetUserId(),
		Email:         resp.GetEmail(),
		PhoneNumber:   resp.GetPhoneNumber(),
		Role:          resp.GetRole(),
		Status:        resp.GetStatus(),
		PhoneVerified: resp.GetPhoneVerified(),
		NablrUsername: resp.GetNablrUsername(),
		AccountNumber: resp.GetAccountNumber(),
		AvatarURL:     resp.GetAvatarUrl(),
	}
}

func (c *Client) GetKYCProfile(ctx context.Context, userID uuid.UUID) (service.KYCProfile, error) {
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.rpc.GetKYCProfile(cctx, &pb.GetKYCProfileRequest{UserId: userID.String()})
	if err != nil {
		return service.KYCProfile{}, classify(err)
	}
	return service.KYCProfile{
		UserID:      resp.GetUserId(),
		Tier:        resp.GetTier(),
		FirstName:   resp.GetFirstName(),
		MiddleName:  resp.GetMiddleName(),
		LastName:    resp.GetLastName(),
		PhoneNumber: resp.GetPhoneNumber(),
		PhoneStatus: resp.GetPhoneStatus(),
		BVNStatus:   resp.GetBvnStatus(),
		NINStatus:   resp.GetNinStatus(),
		Sanctioned:  resp.GetSanctioned(),
		PEP:         resp.GetPep(),
	}, nil
}

// classify maps gRPC transport failures onto the money engine's sentinels so
// the hot path can fail closed (503) on "unreachable" while a background consumer can stop retrying on a settled "not found".
func classify(err error) error {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Unauthenticated, codes.ResourceExhausted:
		return errors.Join(service.ErrIdentityUnavailable, err)
	case codes.NotFound:
		return errors.Join(service.ErrIdentityUserNotFound, err)
	default:
		return err
	}
}
