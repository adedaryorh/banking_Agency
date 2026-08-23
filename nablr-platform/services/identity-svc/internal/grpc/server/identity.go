package server

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"nabla/identity-svc/internal/controllers"
	"nabla/identity-svc/internal/models"
	repo "nabla/identity-svc/internal/repository"
	pb "nabla/identity-svc/proto/identity"
)

type IdentityServer struct {
	pb.UnimplementedIdentityServiceServer
	store     repo.Store
	security  controllers.SecurityController
	apiSecret string
}

func NewIdentityServer(store repo.Store, security controllers.SecurityController, apiSecret string) pb.IdentityServiceServer {
	return &IdentityServer{store: store, security: security, apiSecret: apiSecret}
}

func (s *IdentityServer) validateServiceAuth(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "metadata missing")
	}
	tokens := md.Get("x-service-auth")
	if len(tokens) == 0 || tokens[0] != s.apiSecret {
		return status.Error(codes.Unauthenticated, "invalid service token")
	}
	return nil
}

func parseUserID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, status.Error(codes.InvalidArgument, "invalid user_id")
	}
	return id, nil
}

func (s *IdentityServer) GetUser(ctx context.Context, request *pb.GetUserRequest) (*pb.GetUserResponse, error) {
	if err := s.validateServiceAuth(ctx); err != nil {
		return nil, err
	}
	id, err := parseUserID(request.GetUserId())
	if err != nil {
		return nil, err
	}
	user, err := s.store.Auth().UserByID(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	return userResponse(user)
}

// userResponse builds the shared wire shape for a user. nablr_username and
// account_number travel with every read so callers that only needed one fact
// (pay/resolve matching, funding-account handle display) do not need a second
// round trip.
func userResponse(user *models.User) (*pb.GetUserResponse, error) {
	response := &pb.GetUserResponse{UserId: user.ID.String(), Email: user.Email, PhoneNumber: user.PhoneNumber, Role: string(user.Role), Status: string(user.Status), NablrUsername: user.NablrUsername, AccountNumber: user.AccountNumber, CreatedAt: timestamppb.New(user.CreatedAt), AvatarUrl: user.AvatarURL}
	if user.EmailVerifiedAt != nil {
		response.EmailVerifiedAt = timestamppb.New(*user.EmailVerifiedAt)
	}
	return response, nil
}

func (s *IdentityServer) GetUserByPhone(ctx context.Context, request *pb.GetUserByPhoneRequest) (*pb.GetUserResponse, error) {
	if err := s.validateServiceAuth(ctx); err != nil {
		return nil, err
	}
	user, err := s.store.Auth().UserByPhone(ctx, request.GetPhoneNumber())
	if err != nil {
		return nil, mapError(err)
	}
	return userResponse(user)
}

func (s *IdentityServer) GetUserByUsername(ctx context.Context, request *pb.GetUserByUsernameRequest) (*pb.GetUserResponse, error) {
	if err := s.validateServiceAuth(ctx); err != nil {
		return nil, err
	}
	username := strings.TrimPrefix(strings.TrimSpace(request.GetUsername()), "@")
	if username == "" {
		return nil, status.Error(codes.InvalidArgument, "username required")
	}
	user, err := s.store.Auth().UserByUsername(ctx, username)
	if err != nil {
		return nil, mapError(err)
	}
	return userResponse(user)
}

func (s *IdentityServer) GetUserByAccountNumber(ctx context.Context, request *pb.GetUserByAccountNumberRequest) (*pb.GetUserResponse, error) {
	if err := s.validateServiceAuth(ctx); err != nil {
		return nil, err
	}
	accountNumber := strings.TrimSpace(request.GetAccountNumber())
	if accountNumber == "" {
		return nil, status.Error(codes.InvalidArgument, "account_number required")
	}
	user, err := s.store.Auth().UserByAccountNumber(ctx, accountNumber)
	if err != nil {
		return nil, mapError(err)
	}
	return userResponse(user)
}

func (s *IdentityServer) GetKYCProfile(ctx context.Context, request *pb.GetKYCProfileRequest) (*pb.GetKYCProfileResponse, error) {
	if err := s.validateServiceAuth(ctx); err != nil {
		return nil, err
	}
	id, err := parseUserID(request.GetUserId())
	if err != nil {
		return nil, err
	}
	profile, err := s.store.KYC().ProfileByUserID(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	return &pb.GetKYCProfileResponse{UserId: profile.UserID.String(), Tier: int32(profile.Tier), FirstName: profile.FirstName, MiddleName: profile.MiddleName, LastName: profile.LastName, PhoneNumber: profile.PhoneNumber, PhoneStatus: string(profile.PhoneStatus), BvnStatus: string(profile.BVNStatus), NinStatus: string(profile.NINStatus), Sanctioned: profile.Sanctioned, Pep: profile.PEP}, nil
}

func (s *IdentityServer) ValidateKYCTier(ctx context.Context, request *pb.ValidateKYCTierRequest) (*pb.ValidateKYCTierResponse, error) {
	profile, err := s.GetKYCProfile(ctx, &pb.GetKYCProfileRequest{UserId: request.GetUserId()})
	if err != nil {
		return nil, err
	}
	valid := profile.Tier >= request.GetRequiredTier()
	message := "KYC tier requirement satisfied"
	if !valid {
		message = "KYC tier requirement not satisfied"
	}
	return &pb.ValidateKYCTierResponse{Valid: valid, CurrentTier: profile.Tier, Message: message}, nil
}

func (s *IdentityServer) VerifyPIN(ctx context.Context, request *pb.VerifyPINRequest) (*pb.VerifyPINResponse, error) {
	if err := s.validateServiceAuth(ctx); err != nil {
		return nil, err
	}
	id, err := parseUserID(request.GetUserId())
	if err != nil {
		return nil, err
	}
	if err := s.security.VerifyPIN(ctx, s.store, id, request.GetPin()); err != nil {
		return &pb.VerifyPINResponse{Valid: false, Message: err.Error()}, nil
	}
	return &pb.VerifyPINResponse{Valid: true, Message: "PIN verified"}, nil
}

func (s *IdentityServer) SetPIN(ctx context.Context, request *pb.SetPINRequest) (*pb.SetPINResponse, error) {
	if err := s.validateServiceAuth(ctx); err != nil {
		return nil, err
	}
	id, err := parseUserID(request.GetUserId())
	if err != nil {
		return nil, err
	}
	if err := s.security.SetPIN(ctx, id, request.GetPin()); err != nil {
		return &pb.SetPINResponse{Success: false, Message: err.Error()}, nil
	}
	return &pb.SetPINResponse{Success: true, Message: "PIN set"}, nil
}

func (s *IdentityServer) RegisterDevice(ctx context.Context, request *pb.RegisterDeviceRequest) (*pb.RegisterDeviceResponse, error) {
	if err := s.validateServiceAuth(ctx); err != nil {
		return nil, err
	}
	id, err := parseUserID(request.GetUserId())
	if err != nil {
		return nil, err
	}
	_, _, err = s.security.RegisterDevice(ctx, id, controllers.DeviceInput{Fingerprint: request.GetDeviceId(), Name: request.GetDeviceName(), Platform: request.GetDeviceType(), PushToken: request.GetFcmToken()})
	if err != nil {
		return &pb.RegisterDeviceResponse{Success: false, Message: err.Error()}, nil
	}
	return &pb.RegisterDeviceResponse{Success: true, Message: "device registered"}, nil
}

func (s *IdentityServer) ValidateDevice(ctx context.Context, request *pb.ValidateDeviceRequest) (*pb.ValidateDeviceResponse, error) {
	if err := s.validateServiceAuth(ctx); err != nil {
		return nil, err
	}
	userID, err := parseUserID(request.GetUserId())
	if err != nil {
		return nil, err
	}
	deviceID, err := uuid.Parse(request.GetDeviceId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid device_id")
	}
	device, err := s.store.Security().DeviceByID(ctx, deviceID)
	if err != nil {
		return nil, mapError(err)
	}
	valid := device.UserID == userID && !device.Blocked
	return &pb.ValidateDeviceResponse{Valid: valid, Trusted: valid && device.Trusted, Message: "device validation complete"}, nil
}

func mapError(err error) error {
	if errors.Is(err, repo.ErrNotFound) {
		return status.Error(codes.NotFound, "record not found")
	}
	return status.Error(codes.Internal, "identity operation failed")
}
