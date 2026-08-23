package server

import (
	"context"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"nabla/notification-svc/internal/providers"
	pb "nabla/notification-svc/proto/notification"
)

type NotificationServer struct {
	pb.UnimplementedNotificationServiceServer
	token  string
	sms    providers.SMSProvider
	email  providers.EmailProvider
	push   providers.PushProvider
	events deliveryEventRecorder
}

type deliveryEventRecorder interface {
	Record(ctx context.Context, eventType, channel, provider, reference, requestID string)
}

func NewNotificationServer(token string, sms providers.SMSProvider, email providers.EmailProvider, push providers.PushProvider, recorders ...deliveryEventRecorder) pb.NotificationServiceServer {
	var recorder deliveryEventRecorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &NotificationServer{token: token, sms: sms, email: email, push: push, events: recorder}
}

func (s *NotificationServer) validateServiceAuth(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "metadata missing")
	}
	values := md.Get("x-service-auth")
	if s.token == "" || len(values) == 0 || len(values[0]) != len(s.token) || subtle.ConstantTimeCompare([]byte(values[0]), []byte(s.token)) != 1 {
		return status.Error(codes.Unauthenticated, "invalid service token")
	}
	return nil
}

func (s *NotificationServer) SendSMS(ctx context.Context, request *pb.SendSMSRequest) (*pb.DeliveryResponse, error) {
	if err := s.validateServiceAuth(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.GetTo()) == "" || strings.TrimSpace(request.GetBody()) == "" {
		return nil, status.Error(codes.InvalidArgument, "to and body are required")
	}
	result, err := s.sms.SendSMS(ctx, providers.SMSMessage{To: request.GetTo(), Body: request.GetBody(), Sender: request.GetSender()})
	if err != nil {
		if s.events != nil {
			s.events.Record(ctx, "notification.delivery.failed", "sms", s.sms.Name(), "", "")
		}
		return &pb.DeliveryResponse{Success: false, Provider: s.sms.Name(), Message: err.Error()}, nil
	}
	if s.events != nil {
		s.events.Record(ctx, "notification.delivery.accepted", "sms", result.Provider, result.Reference, "")
	}
	return &pb.DeliveryResponse{Success: true, Provider: result.Provider, Reference: result.Reference, Message: "sms accepted"}, nil
}

func (s *NotificationServer) SendEmail(ctx context.Context, request *pb.SendEmailRequest) (*pb.DeliveryResponse, error) {
	if err := s.validateServiceAuth(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.GetTo()) == "" || strings.TrimSpace(request.GetSubject()) == "" || (strings.TrimSpace(request.GetText()) == "" && strings.TrimSpace(request.GetHtml()) == "") {
		return nil, status.Error(codes.InvalidArgument, "to, subject, and text or html are required")
	}
	result, err := s.email.SendEmail(ctx, providers.EmailMessage{To: request.GetTo(), Subject: request.GetSubject(), Text: request.GetText(), HTML: request.GetHtml()})
	if err != nil {
		if s.events != nil {
			s.events.Record(ctx, "notification.delivery.failed", "email", s.email.Name(), "", "")
		}
		return &pb.DeliveryResponse{Success: false, Provider: s.email.Name(), Message: err.Error()}, nil
	}
	if s.events != nil {
		s.events.Record(ctx, "notification.delivery.accepted", "email", result.Provider, result.Reference, "")
	}
	return &pb.DeliveryResponse{Success: true, Provider: result.Provider, Reference: result.Reference, Message: "email accepted"}, nil
}

func (s *NotificationServer) SendPush(ctx context.Context, request *pb.SendPushRequest) (*pb.DeliveryResponse, error) {
	if err := s.validateServiceAuth(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.GetDeviceToken()) == "" || strings.TrimSpace(request.GetTitle()) == "" || strings.TrimSpace(request.GetBody()) == "" {
		return nil, status.Error(codes.InvalidArgument, "device_token, title, and body are required")
	}
	result, err := s.push.SendPush(ctx, providers.PushMessage{DeviceToken: request.GetDeviceToken(), Title: request.GetTitle(), Body: request.GetBody(), Data: request.GetData()})
	if err != nil {
		if s.events != nil {
			s.events.Record(ctx, "notification.delivery.failed", "push", s.push.Name(), "", "")
		}
		return &pb.DeliveryResponse{Success: false, Provider: s.push.Name(), Message: err.Error()}, nil
	}
	if s.events != nil {
		s.events.Record(ctx, "notification.delivery.accepted", "push", result.Provider, result.Reference, "")
	}
	return &pb.DeliveryResponse{Success: true, Provider: result.Provider, Reference: result.Reference, Message: "push accepted"}, nil
}
