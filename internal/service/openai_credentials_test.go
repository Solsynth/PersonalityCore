package service

import (
	"testing"

	"github.com/cloudwego/eino/schema"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
)

func TestOpenAICredentialTokenHashAndScope(t *testing.T) {
	if hashOpenAICredentialToken("sat_example") == "sat_example" || hashOpenAICredentialToken("sat_example") != hashOpenAICredentialToken("sat_example") {
		t.Fatal("credential token hashing is not one-way and deterministic")
	}
	if err := requireScope([]byte(`["openai"]`), "deepseek", "provider"); err == nil {
		t.Fatal("expected provider scope rejection")
	}
	if err := requireScope([]byte(`["openai"]`), "openai", "provider"); err != nil {
		t.Fatalf("expected provider scope acceptance: %v", err)
	}
}

func TestOpenAICredentialUsageLimit(t *testing.T) {
	raw, err := gorm.Open(sqlite.Open("file:credential-limit?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	db := &database.DB{DB: raw}
	if err := db.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	input, output := "1", "1"
	svc := NewConversationService(db, &config.Config{Billing: config.BillingConfig{Currency: "golds", ServiceFeePercentage: "0"}, Providers: []config.ProviderConfig{{ID: "openai", Models: []config.ModelConfig{{Name: "model", Pricing: &config.ModelPricingConfig{Currency: "golds", Input: &input, Output: &output}}}}}}, nil, nil)
	row := &database.OpenAIAccessCredential{ID: "credential-1", AccountID: "account-1", TokenHash: "hash", UsageLimit: "1", UsageUsed: "0", UsageCurrency: "golds", Enabled: true}
	if err := db.Create(row).Error; err != nil {
		t.Fatal(err)
	}
	def := agent.Definition{Model: "openai/model"}
	if err := svc.RecordOpenAICredentialUsage(t.Context(), row.ID, def, &schema.TokenUsage{PromptTokens: 1_000_000}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordOpenAICredentialUsage(t.Context(), row.ID, def, &schema.TokenUsage{PromptTokens: 1_000_000}); err == nil {
		t.Fatal("expected usage limit rejection")
	}
	var updated database.OpenAIAccessCredential
	if err := db.First(&updated, "id = ?", row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.UsageUsed != "2.00000000" {
		t.Fatalf("usage used = %s, want charged over-limit usage", updated.UsageUsed)
	}
}
