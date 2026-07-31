package service

import (
	"testing"

	"github.com/cloudwego/eino/schema"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/config"
)

func TestFloorCurrencyAmountRetainsSubCentRemainder(t *testing.T) {
	tests := []struct{ input, want string }{
		{input: "0.021", want: "0.02"},
		{input: "0.009", want: "0.00"},
		{input: "12.999", want: "12.99"},
	}
	for _, test := range tests {
		r, err := decimal(test.input)
		if err != nil {
			t.Fatalf("decimal(%q): %v", test.input, err)
		}
		if got := floorCurrencyAmount(r).FloatString(2); got != test.want {
			t.Errorf("floorCurrencyAmount(%s) = %s, want %s", test.input, got, test.want)
		}
	}
}

func TestPriceAddsConfiguredGlobalServiceFee(t *testing.T) {
	inputPrice := "10"
	svc := NewBillingService(nil, &config.Config{
		Billing: config.BillingConfig{Currency: "points", ServiceFeePercentage: "5"},
		Providers: []config.ProviderConfig{{
			ID: "openai",
			Models: []config.ModelConfig{{
				Name:    "model",
				Pricing: &config.ModelPricingConfig{Input: &inputPrice, Currency: "points"},
			}},
		}},
	})
	price, err := svc.price(agent.Definition{Model: "openai/model"}, &schema.TokenUsage{PromptTokens: 1_000_000})
	if err != nil {
		t.Fatalf("price() error: %v", err)
	}
	if price.amount != "10.50000000" || price.currency != "points" {
		t.Fatalf("price = %+v, want 10.50000000 points", price)
	}
}
