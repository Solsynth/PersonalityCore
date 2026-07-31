package service

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/wrapperspb"
	sharedauth "src.solsynth.dev/sosys/go/pkg/auth"
	gen "src.solsynth.dev/sosys/go/proto"
	"src.solsynth.dev/sosys/personality/internal/config"
)

type walletPaymentClient struct{ client gen.DyPaymentServiceClient }

func (c walletPaymentClient) CreateTransactionWithAccount(ctx context.Context, payer, payee, currency, amount, remarks string) (string, error) {
	transaction, err := c.client.CreateTransactionWithAccount(ctx, &gen.DyCreateTransactionWithAccountRequest{
		PayerAccountId: wrapperspb.String(payer), PayeeAccountId: wrapperspb.String(payee), Currency: currency, Amount: amount,
		Remarks: wrapperspb.String(remarks), Type: gen.DyTransactionType_DY_TRANSACTION_TYPE_SYSTEM,
	})
	if err != nil {
		return "", err
	}
	return transaction.GetId(), nil
}

func NewWalletPaymentClient(client gen.DyPaymentServiceClient) PaymentClient {
	if client == nil {
		return nil
	}
	return walletPaymentClient{client: client}
}

func NewWalletClient(cfg config.BillingConfig) (gen.DyPaymentServiceClient, *grpc.ClientConn, error) {
	target, useTLS := sharedauth.NormalizeAuthGRPCTarget(cfg.Target, cfg.UseTLS)
	if strings.TrimSpace(target) == "" {
		return nil, nil, fmt.Errorf("wallet gRPC target is empty")
	}
	var creds credentials.TransportCredentials
	if useTLS {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.TLSSkipVerify})
	} else {
		creds = insecure.NewCredentials()
	}
	conn, err := grpc.Dial(target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, nil, fmt.Errorf("dial wallet service: %w", err)
	}
	return gen.NewDyPaymentServiceClient(conn), conn, nil
}
