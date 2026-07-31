package service

import (
	"context"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
)

type billingTestPaymentClient struct{ calls int }

func (c *billingTestPaymentClient) CreateTransactionWithAccount(_ context.Context, _, _, _, _, _ string) (string, error) {
	c.calls++
	return "wallet-tx", nil
}

func newBillingTestService(t *testing.T) (*BillingService, *billingTestPaymentClient) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&database.BillingAccountPolicy{}, &database.BillingUsage{}, &database.BillingPayment{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	svc := NewBillingService(&database.DB{DB: db}, &config.Config{Billing: config.BillingConfig{Enabled: true, PayeeAccountID: "personality", Currency: "golds"}})
	payment := &billingTestPaymentClient{}
	svc.SetPaymentClient(payment)
	return svc, payment
}

func TestSettleAccountClearsBlacklistAfterSuccessfulPayment(t *testing.T) {
	svc, payment := newBillingTestService(t)
	ctx := context.Background()
	if err := svc.db.Create(&database.BillingAccountPolicy{AccountID: "account-1", Blacklisted: true, BlacklistReason: "prior payment failure"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.db.Create(&database.BillingUsage{ID: "usage-1", AccountID: "account-1", Currency: "golds", Amount: "3.5"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SettleAccount(ctx, "account-1"); err != nil {
		t.Fatalf("SettleAccount() error = %v", err)
	}
	if payment.calls != 1 {
		t.Fatalf("payment calls = %d, want 1", payment.calls)
	}
	policy, err := svc.AccountPolicy(ctx, "account-1")
	if err != nil || policy.Blacklisted {
		t.Fatalf("policy after settlement = %#v, err = %v", policy, err)
	}
	var usage database.BillingUsage
	if err := svc.db.First(&usage, "id = ?", "usage-1").Error; err != nil || usage.PaymentID == nil {
		t.Fatalf("usage was not settled: %#v, err = %v", usage, err)
	}
}

func TestSettleCompletedDaysSkipsBlacklistedAccounts(t *testing.T) {
	svc, payment := newBillingTestService(t)
	if err := svc.db.Create(&database.BillingAccountPolicy{AccountID: "account-1", Blacklisted: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.db.Create(&database.BillingUsage{ID: "usage-1", AccountID: "account-1", Currency: "golds", Amount: "3", CreatedAt: time.Now().UTC().Add(-24 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.SettleCompletedDays(context.Background()); err != nil {
		t.Fatalf("SettleCompletedDays() error = %v", err)
	}
	if payment.calls != 0 {
		t.Fatalf("payment calls = %d, want 0", payment.calls)
	}
}
