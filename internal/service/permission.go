package service

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	sharedauth "src.solsynth.dev/sosys/go/pkg/auth"
	gen "src.solsynth.dev/sosys/go/proto"
	"src.solsynth.dev/sosys/personality/internal/config"
)

var ErrPermissionDenied = errors.New("permission denied")

type PermissionChecker interface {
	HasPermission(context.Context, string, string) (bool, error)
}
type grpcPermissionChecker struct{ client gen.DyPermissionServiceClient }

func (c grpcPermissionChecker) HasPermission(ctx context.Context, accountID, key string) (bool, error) {
	response, err := c.client.HasPermission(ctx, &gen.DyHasPermissionRequest{Actor: accountID, Key: key})
	if err != nil {
		return false, err
	}
	return response.GetHasPermission(), nil
}
func (s *ConversationService) SetPermissionClient(client gen.DyPermissionServiceClient) {
	if s.billing != nil && client != nil {
		s.billingPermission = grpcPermissionChecker{client: client}
	}
}
func (s *ConversationService) RequireAccountPermission(ctx context.Context, accountID, key string) error {
	if s.billingPermission == nil {
		return nil
	}
	allowed, err := s.billingPermission.HasPermission(ctx, strings.TrimSpace(accountID), key)
	if err != nil {
		return fmt.Errorf("check permission %q: %w", key, err)
	}
	if !allowed {
		return fmt.Errorf("%w: %s is required", ErrPermissionDenied, key)
	}
	return nil
}

func NewPermissionClient(cfg config.AuthConfig) (gen.DyPermissionServiceClient, *grpc.ClientConn, error) {
	target, useTLS := sharedauth.NormalizeAuthGRPCTarget(cfg.Target, cfg.UseTLS)
	if strings.TrimSpace(target) == "" {
		return nil, nil, errors.New("permission gRPC target is empty")
	}
	var creds credentials.TransportCredentials
	if useTLS {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.TLSSkipVerify})
	} else {
		creds = insecure.NewCredentials()
	}
	conn, err := grpc.Dial(target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, nil, fmt.Errorf("dial permission service: %w", err)
	}
	return gen.NewDyPermissionServiceClient(conn), conn, nil
}
