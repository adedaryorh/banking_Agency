package server

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"nabla/transfers-svc/internal/service"
	pb "nabla/transfers-svc/proto/transfers"
)

type Server struct {
	pb.UnimplementedTransfersServiceServer
	service   *service.Service
	wallets   *service.WalletService
	funding   *service.FundingService
	apiSecret string
}

func New(s *service.Service, wallets *service.WalletService, funding *service.FundingService, secret string) *Server {
	return &Server{service: s, wallets: wallets, funding: funding, apiSecret: secret}
}
func (s *Server) authorize(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || len(md.Get("x-service-auth")) == 0 || md.Get("x-service-auth")[0] != s.apiSecret {
		return status.Error(codes.Unauthenticated, "invalid service token")
	}
	return nil
}
func parse(v string) (uuid.UUID, error) {
	id, e := uuid.Parse(v)
	if e != nil {
		return uuid.Nil, status.Error(codes.InvalidArgument, "invalid UUID")
	}
	return id, nil
}
func (s *Server) GetWallet(ctx context.Context, r *pb.GetWalletRequest) (*pb.WalletResponse, error) {
	if e := s.authorize(ctx); e != nil {
		return nil, e
	}
	u, e := parse(r.GetUserId())
	if e != nil {
		return nil, e
	}
	w, e := s.service.Wallet(ctx, u, r.GetCurrency())
	if e != nil {
		return nil, mapErr(e)
	}
	return &pb.WalletResponse{WalletId: w.ID.String(), UserId: w.UserID.String(), Currency: w.Currency, AvailableMinor: w.AvailableMinor, ReservedMinor: w.ReservedMinor, Status: w.Status}, nil
}
func (s *Server) GetTransfer(ctx context.Context, r *pb.GetTransferRequest) (*pb.TransferResponse, error) {
	if e := s.authorize(ctx); e != nil {
		return nil, e
	}
	u, e := parse(r.GetUserId())
	if e != nil {
		return nil, e
	}
	id, e := parse(r.GetTransferId())
	if e != nil {
		return nil, e
	}
	t, e := s.service.TransferByID(ctx, u, id)
	if e != nil {
		return nil, mapErr(e)
	}
	return &pb.TransferResponse{TransferId: t.ID.String(), Reference: t.Reference, Status: t.Status, AmountMinor: t.SendAmountMinor, Currency: t.SendCurrency}, nil
}

func (s *Server) ProvisionCustomer(ctx context.Context, r *pb.ProvisionCustomerRequest) (*pb.ProvisionCustomerResponse, error) {
	if e := s.authorize(ctx); e != nil {
		return nil, e
	}
	if s.wallets == nil || s.funding == nil {
		return nil, status.Error(codes.FailedPrecondition, "customer funding is not configured")
	}
	u, e := parse(r.GetUserId())
	if e != nil {
		return nil, e
	}
	currency := r.GetCurrency()
	if currency == "" {
		currency = "NGN"
	}
	wallet, e := s.wallets.OpenWallet(ctx, u, currency, "Main wallet")
	if e != nil {
		return nil, mapErr(e)
	}
	account, e := s.funding.EnsureAccount(ctx, u)
	if e != nil {
		return nil, mapErr(e)
	}
	return &pb.ProvisionCustomerResponse{
		WalletId:      wallet.ID.String(),
		AccountNumber: account.AccountNumber,
		AccountName:   account.AccountName,
		BankName:      account.BankName,
		BankCode:      account.BankCode,
		Currency:      account.Currency,
	}, nil
}
func mapErr(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return status.Error(codes.NotFound, "record not found")
	}
	return status.Error(codes.Internal, "transfer operation failed")
}
