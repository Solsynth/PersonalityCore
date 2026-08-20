package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"

	"github.com/cloudwego/eino/schema"
)

func TestResolveOpenAIAgentModel(t *testing.T) {
	tests := []struct {
		name, agentID, model, wantAgent, wantModel string
		wantErr                                    bool
	}{
		{name: "agent default model", model: "assistant", wantAgent: "assistant"},
		{name: "agent selected provider model", model: "assistant/openai/gpt-4.1-mini", wantAgent: "assistant", wantModel: "openai/gpt-4.1-mini"},
		{name: "raw model proxy", model: "raw/openai/gpt-4.1-mini", wantAgent: "raw", wantModel: "openai/gpt-4.1-mini"},
		{name: "legacy agent id", agentID: "assistant", model: "openai/gpt-4.1-mini", wantAgent: "assistant", wantModel: "openai/gpt-4.1-mini"},
		{name: "reject ambiguous model", model: "openai/gpt-4.1-mini", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agentID, model, err := resolveOpenAIAgentModel(tc.agentID, tc.model)
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolveOpenAIAgentModel() error = %v, wantErr %v", err, tc.wantErr)
			}
			if agentID != tc.wantAgent || model != tc.wantModel {
				t.Fatalf("resolveOpenAIAgentModel() = (%q, %q), want (%q, %q)", agentID, model, tc.wantAgent, tc.wantModel)
			}
		})
	}
}

type openAICompatibleTestWalletChecker struct{}

func (openAICompatibleTestWalletChecker) CheckWalletExists(context.Context, string) (bool, error) {
	return true, nil
}

type openAICompatibleTestPaymentClient struct{}

func (openAICompatibleTestPaymentClient) CreateTransactionWithAccount(context.Context, string, string, string, string, string) (string, error) {
	return "payment-1", nil
}

func TestCompleteOpenAIRecordsBillingUsage(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected completion path %q", r.URL.Path)
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode completion request: %v", err)
		}
		if request.Model != "model" {
			t.Fatalf("completion model = %q, want model", request.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer modelServer.Close()

	inputPrice, outputPrice := "10", "20"
	cfg := &config.Config{
		Billing: config.BillingConfig{Enabled: true, Currency: "golds", ServiceFeePercentage: "0", InstantBillingWall: "0"},
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Type:    "openai-compatible",
			APIKey:  "test",
			BaseURL: modelServer.URL + "/v1",
			Timeout: time.Second,
			Models: []config.ModelConfig{{
				Name:    "model",
				Pricing: &config.ModelPricingConfig{Currency: "golds", Input: &inputPrice, Output: &outputPrice},
			}},
		}},
	}
	registry, err := agent.NewRegistry([]config.AgentConfig{{ID: "assistant", Name: "Assistant", Model: "openai/model", Enabled: true}})
	if err != nil {
		t.Fatalf("NewRegistry() error: %v", err)
	}
	executor, err := agent.NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor() error: %v", err)
	}
	svc := NewConversationService(openTestDB(t), cfg, registry, executor)
	svc.Billing().SetWalletChecker(openAICompatibleTestWalletChecker{})
	svc.Billing().SetPaymentClient(openAICompatibleTestPaymentClient{})

	result, err := svc.CompleteOpenAI(t.Context(), OpenAICompletionInput{
		AccountID: "account-1",
		Model:     "assistant",
		Messages:  []*schema.Message{schema.UserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("CompleteOpenAI() error: %v", err)
	}
	if result.Message.Content != "hello" {
		t.Fatalf("completion content = %q, want hello", result.Message.Content)
	}

	var usage database.BillingUsage
	if err := svc.db.First(&usage, "account_id = ?", "account-1").Error; err != nil {
		t.Fatalf("load billing usage: %v", err)
	}
	if usage.Model != "openai/model" || usage.Currency != "golds" || usage.InputTokens != 2 || usage.OutputTokens != 3 {
		t.Fatalf("billing usage = %+v", usage)
	}
	if usage.Amount != "0.00008000" {
		t.Fatalf("billing amount = %q, want 0.00008000", usage.Amount)
	}
	if usage.RunID == nil || *usage.RunID == "" {
		t.Fatal("billing usage run ID is empty")
	}
}
