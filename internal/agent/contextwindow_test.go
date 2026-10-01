package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"src.solsynth.dev/sosys/persona/internal/config"
)

func contextWindowExecutor(t *testing.T, provider config.ProviderConfig) *Executor {
	t.Helper()
	executor, err := NewExecutor(&config.Config{Providers: []config.ProviderConfig{provider}})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	return executor
}

func TestContextWindowPrefersConfiguredValuesOverPresets(t *testing.T) {
	executor := contextWindowExecutor(t, config.ProviderConfig{
		ID: "openai", Type: "openai-compatible", APIKey: "test",
		ContextWindow: 32_000,
		Models: []config.ModelConfig{
			{Name: "gpt-4o", ContextWindow: 16_000},
			{Name: "gpt-4o-mini"},
		},
	})

	// A per-model value beats both the provider default and the preset.
	if got := executor.ContextWindow(context.Background(), "openai/gpt-4o"); got != 16_000 {
		t.Fatalf("model context window = %d, want 16000", got)
	}
	// Without a model value the provider default wins over the preset.
	if got := executor.ContextWindow(context.Background(), "openai/gpt-4o-mini"); got != 32_000 {
		t.Fatalf("provider fallback = %d, want 32000", got)
	}

	presets := contextWindowExecutor(t, config.ProviderConfig{
		ID: "openai", Type: "openai-compatible", APIKey: "test",
	})
	if got := presets.ContextWindow(context.Background(), "openai/gpt-4o"); got != 128_000 {
		t.Fatalf("preset context window = %d, want 128000", got)
	}
	// An unlisted, unpreset model with discovery off stays unknown rather than
	// guessing a denominator.
	if got := presets.ContextWindow(context.Background(), "openai/unknown-model"); got != 0 {
		t.Fatalf("unknown model window = %d, want 0", got)
	}
}

func TestPresetContextWindowMatchesLongestPrefixFirst(t *testing.T) {
	cases := map[string]int{
		"gpt-4.1-mini":     1_047_576,
		"gpt-4-turbo":      128_000,
		"gpt-4o":           128_000,
		"claude-sonnet-4":  200_000,
		"gemini-2.0-flash": 1_048_576,
		"unknown":          0,
	}
	for model, want := range cases {
		if got := presetContextWindow(model); got != want {
			t.Fatalf("presetContextWindow(%q) = %d, want %d", model, got, want)
		}
	}
}

func TestContextWindowDiscoveryIsOptInAndCached(t *testing.T) {
	var hits atomic.Int64
	var sawAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected discovery path %q", r.URL.Path)
		}
		sawAuth = r.Header.Get("Authorization")
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []any{
				map[string]any{"id": "other-model", "max_model_len": 8192},
				map[string]any{"id": "self-hosted", "max_model_len": 40_960},
			},
		})
	}))
	defer server.Close()

	off := contextWindowExecutor(t, config.ProviderConfig{
		ID: "self", Type: "openai-compatible", APIKey: "test", BaseURL: server.URL + "/v1",
	})
	if got := off.ContextWindow(context.Background(), "self/self-hosted"); got != 0 {
		t.Fatalf("discovery must be opt-in, got %d", got)
	}
	if hits.Load() != 0 {
		t.Fatalf("discovery ran while disabled: %d hits", hits.Load())
	}

	on := contextWindowExecutor(t, config.ProviderConfig{
		ID: "self", Type: "openai-compatible", APIKey: "test", BaseURL: server.URL + "/v1",
		DiscoverContextWindow: true,
	})
	if got := on.ContextWindow(context.Background(), "self/self-hosted"); got != 40_960 {
		t.Fatalf("discovered window = %d, want 40960", got)
	}
	if sawAuth != "Bearer test" {
		t.Fatalf("discovery authorization = %q", sawAuth)
	}
	// A second lookup must be answered from the cache, including the miss for
	// a model the provider did not describe.
	if got := on.ContextWindow(context.Background(), "self/self-hosted"); got != 40_960 {
		t.Fatalf("cached window = %d, want 40960", got)
	}
	if got := on.ContextWindow(context.Background(), "self/missing"); got != 0 {
		t.Fatalf("missing model window = %d, want 0", got)
	}
	if hits.Load() != 2 {
		t.Fatalf("discovery hits = %d, want one per provider/model", hits.Load())
	}
}

func TestContextWindowDiscoveryToleratesUnhelpfulReplies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A plain OpenAI /models reply: ids only, no context length.
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o","object":"model","created":1,"owned_by":"openai"}]}`))
	}))
	defer server.Close()

	executor := contextWindowExecutor(t, config.ProviderConfig{
		ID: "openai", Type: "openai-compatible", APIKey: "test", BaseURL: server.URL + "/v1",
		DiscoverContextWindow: true,
	})
	if got := executor.ContextWindow(context.Background(), "openai/unlisted-model"); got != 0 {
		t.Fatalf("window = %d, want 0 when the provider states none", got)
	}
}

func TestContextWindowDiscoverySurvivesAnUnreachableProvider(t *testing.T) {
	executor := contextWindowExecutor(t, config.ProviderConfig{
		ID: "down", Type: "openai-compatible", APIKey: "test", BaseURL: "http://127.0.0.1:1/v1",
		Timeout: time.Millisecond, DiscoverContextWindow: true,
	})
	if got := executor.ContextWindow(context.Background(), "down/unlisted-model"); got != 0 {
		t.Fatalf("window = %d, want 0 when discovery fails", got)
	}
}
