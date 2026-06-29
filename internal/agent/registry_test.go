package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestExecutor_SupportsVisionDefaultsConservativelyForCompatibleBackends(t *testing.T) {
	executor, err := NewExecutor(&config.Config{
		Providers: []config.ProviderConfig{
			{ID: "openai", Type: "openai", APIKey: "test", Timeout: time.Second},
			{ID: "azure", Type: "openai", APIKey: "test", ByAzure: true, BaseURL: "https://example.openai.azure.com", Timeout: time.Second},
			{ID: "deepseek", Type: "openai", APIKey: "test", BaseURL: "https://api.deepseek.com", Timeout: time.Second},
		},
	})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

	if !executor.SupportsVision(Definition{Model: "openai/gpt-4.1-mini"}) {
		t.Fatal("expected official openai provider to default to vision-capable")
	}
	if !executor.SupportsVision(Definition{Model: "azure/gpt-4.1"}) {
		t.Fatal("expected azure provider to default to vision-capable")
	}
	if executor.SupportsVision(Definition{Model: "deepseek/deepseek-v4-flash"}) {
		t.Fatal("expected custom openai-compatible backend to default to text-only")
	}
}

func TestExecutor_ResolveEmbeddingModelUsesDefaultAndRejectsCompletionModel(t *testing.T) {
	executor, err := NewExecutor(&config.Config{
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Type:    "openai",
			APIKey:  "test",
			Timeout: time.Second,
			Models: []config.ModelConfig{
				{Name: "gpt-4.1-mini"},
				{Name: "text-embedding-3-small", Type: "embedding"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

	modelRef, err := executor.ResolveEmbeddingModel("", "openai/text-embedding-3-small")
	if err != nil {
		t.Fatalf("ResolveEmbeddingModel() error = %v", err)
	}
	if modelRef != "openai/text-embedding-3-small" {
		t.Fatalf("expected default embedding model, got %q", modelRef)
	}

	if _, err := executor.ResolveEmbeddingModel("openai/gpt-4.1-mini", ""); err == nil {
		t.Fatal("expected completion model to be rejected for embeddings")
	}
}

func TestExecutor_GenerateEmbeddings(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		var body struct {
			Input      []string `json:"input"`
			Model      string   `json:"model"`
			Dimensions int      `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Model != "text-embedding-3-small" {
			t.Fatalf("unexpected model %q", body.Model)
		}
		if body.Dimensions != 256 {
			t.Fatalf("unexpected dimensions %d", body.Dimensions)
		}
		if len(body.Input) != 2 || body.Input[0] != "hello" || body.Input[1] != "world" {
			t.Fatalf("unexpected input %#v", body.Input)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0},{"object":"embedding","embedding":[0.3,0.4],"index":1}],"model":"text-embedding-3-small","usage":{"prompt_tokens":2,"total_tokens":2}}`))
	}))
	defer ts.Close()

	executor, err := NewExecutor(&config.Config{
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Type:    "openai",
			APIKey:  "test",
			BaseURL: ts.URL + "/v1",
			Timeout: time.Second,
			Models:  []config.ModelConfig{{Name: "text-embedding-3-small", Type: "embedding"}},
		}},
	})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

	vectors, err := executor.GenerateEmbeddings(context.Background(), "openai/text-embedding-3-small", []string{"hello", "world"}, 256)
	if err != nil {
		t.Fatalf("GenerateEmbeddings() error = %v", err)
	}
	if len(vectors) != 2 || len(vectors[0]) != 2 || len(vectors[1]) != 2 {
		t.Fatalf("unexpected embeddings %#v", vectors)
	}
}
