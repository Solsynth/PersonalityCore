package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"src.solsynth.dev/sosys/persona/internal/agent"
	"src.solsynth.dev/sosys/persona/internal/config"
	"src.solsynth.dev/sosys/persona/internal/database"
)

// fakeWalletChecker lets priced models pass the payment-wallet requirement.
type fakeWalletChecker struct{ exists bool }

func (f fakeWalletChecker) CheckWalletExists(context.Context, string) (bool, error) {
	return f.exists, nil
}

// newCompleteTestService wires a real registry, executor, and billing service
// against a fake OpenAI-compatible endpoint that reports token usage.
func newCompleteTestService(t *testing.T, inputPrice, outputPrice string, billing config.BillingConfig) (*ConversationService, string) {
	t.Helper()
	code := "123456"
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected completion path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "completion-1",
			"model": "model",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "Your code is " + code},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1000, "completion_tokens": 500, "total_tokens": 1500},
		})
	}))
	t.Cleanup(modelServer.Close)

	cfg := &config.Config{
		Billing: billing,
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Type:    "openai-compatible",
			APIKey:  "test",
			BaseURL: modelServer.URL + "/v1",
			Timeout: time.Second,
			Models: []config.ModelConfig{{
				Name:    "model",
				Pricing: &config.ModelPricingConfig{Input: &inputPrice, Output: &outputPrice},
			}},
		}},
	}
	registry, err := agent.NewRegistry([]config.AgentConfig{{
		ID:      "mail-summarizer",
		Name:    "Mail Summarizer",
		Model:   "openai/model",
		Enabled: true,
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	executor, err := agent.NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	svc := NewConversationService(openTestDB(t), cfg, registry, executor)
	svc.Billing().SetWalletChecker(fakeWalletChecker{exists: true})
	return svc, code
}

func TestCompleteOnceRecordsPricedUsageForTheAccount(t *testing.T) {
	svc, code := newCompleteTestService(t, "10", "20", config.BillingConfig{Enabled: true, Currency: "golds", ServiceFeePercentage: "0"})

	result, err := svc.CompleteOnce(context.Background(), CompleteOnceInput{
		AgentID:   "mail-summarizer",
		AccountID: "acct-mail",
		Message:   "Summarize this email.",
	})
	if err != nil {
		t.Fatalf("CompleteOnce() error = %v", err)
	}
	if result.Content != "Your code is "+code {
		t.Fatalf("content = %q", result.Content)
	}

	var usages []database.BillingUsage
	if err := svc.db.Find(&usages).Error; err != nil {
		t.Fatalf("load billing usage: %v", err)
	}
	if len(usages) != 1 {
		t.Fatalf("billing usage rows = %d, want 1", len(usages))
	}
	usage := usages[0]
	// 1000 prompt tokens at 10/1M plus 500 completion tokens at 20/1M.
	if usage.Amount != "0.02000000" {
		t.Fatalf("amount = %q, want 0.02000000", usage.Amount)
	}
	if usage.AccountID != "acct-mail" || usage.Model != "openai/model" || usage.Currency != "golds" {
		t.Fatalf("usage = %+v, want the requesting account and effective model", usage)
	}
	if usage.InputTokens != 1000 || usage.OutputTokens != 500 {
		t.Fatalf("tokens = %d/%d, want 1000/500", usage.InputTokens, usage.OutputTokens)
	}
	if usage.RunID != nil {
		t.Fatalf("run_id = %v, want NULL for a one-shot completion", *usage.RunID)
	}
}

func TestCompleteOnceCountsFreeModelsAgainstUsageLimits(t *testing.T) {
	svc, _ := newCompleteTestService(t, "0", "0", config.BillingConfig{
		Enabled:              true,
		Currency:             "golds",
		ServiceFeePercentage: "0",
		DailyUsageLimits:     map[string]string{"golds": "0.5"},
	})

	if _, err := svc.CompleteOnce(context.Background(), CompleteOnceInput{
		AgentID: "mail-summarizer", AccountID: "acct-mail", Message: "One",
	}); err != nil {
		t.Fatalf("first CompleteOnce() error = %v", err)
	}

	// A free model still reserves a ledger row, so the day's usage is no longer
	// zero and the account is now over its limit.
	if err := svc.db.Model(&database.BillingUsage{}).
		Where("account_id = ?", "acct-mail").
		Update("amount", "0.6").Error; err != nil {
		t.Fatalf("age usage row: %v", err)
	}
	if err := svc.db.Model(&database.BillingUsage{}).
		Where("account_id = ?", "acct-mail").
		Update("original_amount", "0.6").Error; err != nil {
		t.Fatalf("age usage row: %v", err)
	}

	_, err := svc.CompleteOnce(context.Background(), CompleteOnceInput{
		AgentID: "mail-summarizer", AccountID: "acct-mail", Message: "Two",
	})
	if !errors.Is(err, ErrBillingQuotaExceeded) {
		t.Fatalf("CompleteOnce() error = %v, want ErrBillingQuotaExceeded", err)
	}
}

func TestCompleteOnceRejectsBlacklistedAccounts(t *testing.T) {
	svc, _ := newCompleteTestService(t, "10", "20", config.BillingConfig{Enabled: true, Currency: "golds", ServiceFeePercentage: "0"})
	if err := svc.db.Create(&database.BillingAccountPolicy{AccountID: "acct-banned", Blacklisted: true}).Error; err != nil {
		t.Fatalf("create policy: %v", err)
	}

	_, err := svc.CompleteOnce(context.Background(), CompleteOnceInput{
		AgentID: "mail-summarizer", AccountID: "acct-banned", Message: "Hello",
	})
	if !errors.Is(err, ErrBillingBlacklisted) {
		t.Fatalf("CompleteOnce() error = %v, want ErrBillingBlacklisted", err)
	}
}

func TestCompleteOnceReleasesReservationWhenGenerationFails(t *testing.T) {
	svc, _ := newCompleteTestService(t, "10", "20", config.BillingConfig{Enabled: true, Currency: "golds", ServiceFeePercentage: "0"})

	// A missing executor entry (unknown agent) is caught earlier, so force a
	// generation failure by closing the fake model endpoint.
	svc.executor = nil
	if _, err := svc.CompleteOnce(context.Background(), CompleteOnceInput{
		AgentID: "mail-summarizer", AccountID: "acct-mail", Message: "Hello",
	}); err == nil {
		t.Fatal("CompleteOnce() error = nil, want a generation failure")
	}

	var pending int64
	if err := svc.db.Model(&database.BillingUsage{}).Count(&pending).Error; err != nil {
		t.Fatalf("count usage rows: %v", err)
	}
	if pending != 0 {
		t.Fatalf("usage rows = %d, want the reservation released", pending)
	}
}
