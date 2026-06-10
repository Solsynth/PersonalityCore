package agent

import (
	"testing"
	"time"

	"src.solsynth.dev/sosys/personality/internal/config"
)

func TestNewRegistry_RejectsDuplicateIDs(t *testing.T) {
	_, err := NewRegistry([]config.AgentConfig{
		{ID: "a", Name: "A", Enabled: true},
		{ID: "a", Name: "B", Enabled: true},
	})
	if err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestRegistry_ListOnlyEnabled(t *testing.T) {
	registry, err := NewRegistry([]config.AgentConfig{
		{ID: "a", Name: "A", Enabled: true},
		{ID: "b", Name: "B", Enabled: false},
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	if got := len(registry.List()); got != 1 {
		t.Fatalf("expected 1 enabled agent, got %d", got)
	}
}

func TestExecutor_ResolveModelRequiresProviderModelFormat(t *testing.T) {
	executor, err := NewExecutor(&config.Config{
		Providers: []config.ProviderConfig{
			{ID: "openai", Type: "openai", APIKey: "test", Timeout: time.Second},
		},
	})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	if _, _, err := executor.resolveModel("gpt-4.1-mini"); err == nil {
		t.Fatal("expected provider/model format error")
	}
}

func TestExecutor_ResolveModelFindsProvider(t *testing.T) {
	executor, err := NewExecutor(&config.Config{
		Providers: []config.ProviderConfig{
			{ID: "openai", Type: "openai", APIKey: "test", Timeout: time.Second},
		},
	})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	provider, modelName, err := executor.resolveModel("openai/gpt-4.1-mini")
	if err != nil {
		t.Fatalf("resolveModel() error = %v", err)
	}
	if provider.ID != "openai" {
		t.Fatalf("expected provider openai, got %q", provider.ID)
	}
	if modelName != "gpt-4.1-mini" {
		t.Fatalf("expected model name gpt-4.1-mini, got %q", modelName)
	}
}
