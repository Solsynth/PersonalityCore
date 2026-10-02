package agent

import (
	"testing"

	"src.solsynth.dev/sosys/persona/internal/config"
)

func visionExecutor(t *testing.T, provider config.ProviderConfig) *Executor {
	t.Helper()
	executor, err := NewExecutor(&config.Config{Providers: []config.ProviderConfig{provider}})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	return executor
}

// The deployment shape this exists for: a DeepSeek provider listing its Flash
// model with pricing and no modalities, so nothing in the config says the
// model reads images even though the endpoint serves a multimodal one.
func TestSupportsVisionFromDeepSeekFlashPreset(t *testing.T) {
	executor := visionExecutor(t, config.ProviderConfig{
		ID: "deepseek", Type: "openai-compatible", APIKey: "test",
		BaseURL: "https://api.deepseek.com",
		Models: []config.ModelConfig{
			{Name: "deepseek-v4-flash"},
			{Name: "deepseek-v4-pro"},
		},
	})

	if !executor.SupportsVision(Definition{Model: "deepseek/deepseek-v4-flash"}) {
		t.Fatal("deepseek-v4-flash should take image input")
	}
	// Only the Flash line is documented multimodal; Pro is not.
	if executor.SupportsVision(Definition{Model: "deepseek/deepseek-v4-pro"}) {
		t.Fatal("deepseek-v4-pro should not be assumed multimodal")
	}
}

// The same slug served elsewhere is a different endpoint, and its image
// support is not the preset's to claim.
func TestSupportsVisionPresetIsScopedToTheProvider(t *testing.T) {
	executor := visionExecutor(t, config.ProviderConfig{
		ID: "fireworks", Type: "openai-compatible", APIKey: "test",
		BaseURL: "https://api.fireworks.ai/inference/v1",
		Models:  []config.ModelConfig{{Name: "deepseek-v4-flash"}},
	})

	if executor.SupportsVision(Definition{Model: "fireworks/deepseek-v4-flash"}) {
		t.Fatal("a preset must not enable images on a provider that does not serve them")
	}
}

// What the deployment says outranks what a preset knows, in both directions.
func TestSupportsVisionConfigOutranksPresets(t *testing.T) {
	declared := visionExecutor(t, config.ProviderConfig{
		ID: "deepseek", Type: "openai-compatible", APIKey: "test",
		BaseURL: "https://api.deepseek.com",
		Models: []config.ModelConfig{
			{Name: "deepseek-v4-flash", Modalities: []string{"text"}},
		},
	})
	if declared.SupportsVision(Definition{Model: "deepseek/deepseek-v4-flash"}) {
		t.Fatal("a model declaring text-only modalities must not receive images")
	}

	flaggedOff := visionExecutor(t, config.ProviderConfig{
		ID: "deepseek", Type: "openai-compatible", APIKey: "test",
		BaseURL:        "https://api.deepseek.com",
		SupportsVision: new(false),
		Models:         []config.ModelConfig{{Name: "deepseek-v4-flash"}},
	})
	if flaggedOff.SupportsVision(Definition{Model: "deepseek/deepseek-v4-flash"}) {
		t.Fatal("an explicit provider opt-out must outrank a preset")
	}

	// A provider-wide opt-in now reaches a listed model that stays silent about
	// its modalities, which it could not before.
	flaggedOn := visionExecutor(t, config.ProviderConfig{
		ID: "local", Type: "openai-compatible", APIKey: "test",
		BaseURL:        "http://127.0.0.1:8080/v1",
		SupportsVision: new(true),
		Models:         []config.ModelConfig{{Name: "qwen3-vl-8b"}},
	})
	if !flaggedOn.SupportsVision(Definition{Model: "local/qwen3-vl-8b"}) {
		t.Fatal("a provider declaring vision must be believed for its own model")
	}
}

func TestSupportsVisionFallsBackToTheEndpoint(t *testing.T) {
	openAI := visionExecutor(t, config.ProviderConfig{
		ID: "openai", Type: "openai-compatible", APIKey: "test",
		BaseURL: "https://api.openai.com/v1",
	})
	if !openAI.SupportsVision(Definition{Model: "openai/gpt-4.1-mini"}) {
		t.Fatal("openai.com serves images with no other signal")
	}

	unknown := visionExecutor(t, config.ProviderConfig{
		ID: "deepseek", Type: "openai-compatible", APIKey: "test",
		BaseURL: "https://api.deepseek.com",
		Models:  []config.ModelConfig{{Name: "deepseek-chat"}},
	})
	if unknown.SupportsVision(Definition{Model: "deepseek/deepseek-chat"}) {
		t.Fatal("a text-only model on a non-OpenAI endpoint stays text-only")
	}
}
