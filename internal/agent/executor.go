package agent

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	goopenai "github.com/meguminnnnnnnnn/go-openai"

	"src.solsynth.dev/sosys/personality/internal/config"
)

type RunRequest struct {
	Agent    Definition
	Messages []*schema.Message
}

type Executor struct {
	providers map[string]config.ProviderConfig
}

func NewExecutor(cfg *config.Config) (*Executor, error) {
	if cfg == nil {
		return nil, fmt.Errorf("executor config is missing")
	}

	providers := make(map[string]config.ProviderConfig, len(cfg.Providers))
	for _, provider := range cfg.Providers {
		id := strings.TrimSpace(provider.ID)
		if id == "" {
			return nil, fmt.Errorf("provider id is required")
		}
		if _, exists := providers[id]; exists {
			return nil, fmt.Errorf("duplicate provider id %q", id)
		}
		if strings.TrimSpace(provider.Type) == "" {
			return nil, fmt.Errorf("provider %q type is required", id)
		}
		providers[id] = provider
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("at least one provider is required")
	}

	return &Executor{providers: providers}, nil
}

func (e *Executor) Generate(ctx context.Context, req RunRequest) (*schema.Message, error) {
	model, err := e.newChatModel(ctx, req.Agent)
	if err != nil {
		return nil, err
	}
	return model.Generate(ctx, req.Messages)
}

func (e *Executor) Stream(ctx context.Context, req RunRequest) (*schema.StreamReader[*schema.Message], error) {
	model, err := e.newChatModel(ctx, req.Agent)
	if err != nil {
		return nil, err
	}
	return model.Stream(ctx, req.Messages)
}

func (e *Executor) NewToolCallingModel(ctx context.Context, agent Definition, tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	baseModel, err := e.newChatModel(ctx, agent)
	if err != nil {
		return nil, err
	}
	toolModel, ok := baseModel.(model.ToolCallingChatModel)
	if !ok {
		return nil, fmt.Errorf("provider for model %q does not support tool calling", agent.Model)
	}
	return toolModel.WithTools(tools)
}

func (e *Executor) ResolveEmbeddingModel(explicitModel, defaultModel string) (string, error) {
	modelRef := strings.TrimSpace(explicitModel)
	if modelRef == "" {
		modelRef = strings.TrimSpace(defaultModel)
	}
	if modelRef == "" {
		return "", fmt.Errorf("embedding model is required")
	}
	_, _, _, err := e.resolveModelForPurpose(modelRef, true)
	if err != nil {
		return "", err
	}
	return modelRef, nil
}

func (e *Executor) GenerateEmbeddings(ctx context.Context, modelRef string, texts []string, dimensions int) ([][]float32, error) {
	if e == nil {
		return nil, fmt.Errorf("executor config is missing")
	}
	provider, modelName, _, err := e.resolveModelForPurpose(modelRef, true)
	if err != nil {
		return nil, err
	}
	client, err := e.newOpenAIClient(provider)
	if err != nil {
		return nil, err
	}

	resp, err := client.CreateEmbeddings(ctx, goopenai.EmbeddingRequestStrings{
		Input:      texts,
		Model:      goopenai.EmbeddingModel(modelName),
		Dimensions: dimensions,
	})
	if err != nil {
		return nil, fmt.Errorf("embedding generation failed: %w", err)
	}

	vectors := make([][]float32, 0, len(resp.Data))
	for _, item := range resp.Data {
		vectors = append(vectors, append([]float32(nil), item.Embedding...))
	}
	return vectors, nil
}

func (e *Executor) newChatModel(ctx context.Context, agent Definition) (model.BaseChatModel, error) {
	if e == nil {
		return nil, fmt.Errorf("executor config is missing")
	}
	provider, modelName, _, err := e.resolveModelForPurpose(agent.Model, false)
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(strings.TrimSpace(provider.Type)) {
	case "openai", "openai-compatible":
		return e.newOpenAIChatModel(ctx, provider, modelName, agent)
	default:
		return nil, fmt.Errorf("provider %q uses unsupported type %q", provider.ID, provider.Type)
	}
}

func (e *Executor) newOpenAIChatModel(ctx context.Context, provider config.ProviderConfig, modelName string, agent Definition) (*einoopenai.ChatModel, error) {
	if strings.TrimSpace(provider.APIKey) == "" && !provider.ByAzure {
		return nil, fmt.Errorf("provider %q apiKey is required", provider.ID)
	}

	mc := provider.ResolveModel(modelName)

	temperature := provider.Temperature
	if mc != nil && mc.Temperature != 0 {
		temperature = mc.Temperature
	}
	if agent.Temperature != nil {
		temperature = *agent.Temperature
	}
	topP := provider.TopP
	if mc != nil && mc.TopP != 0 {
		topP = mc.TopP
	}
	if agent.TopP != nil {
		topP = *agent.TopP
	}
	maxTokens := provider.MaxCompletionTokens
	if mc != nil && mc.MaxCompletionTokens != 0 {
		maxTokens = mc.MaxCompletionTokens
	}
	if agent.MaxCompletionTokens != nil {
		maxTokens = *agent.MaxCompletionTokens
	}
	if agent.PerkMaxTokens != nil {
		maxTokens = *agent.PerkMaxTokens
	}

	return einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		APIKey:              provider.APIKey,
		BaseURL:             provider.BaseURL,
		ByAzure:             provider.ByAzure,
		APIVersion:          provider.APIVersion,
		Model:               modelName,
		Timeout:             provider.Timeout,
		MaxCompletionTokens: intPtr(maxTokens),
		Temperature:         float32Ptr(temperature),
		TopP:                float32Ptr(topP),
	})
}

func (e *Executor) newOpenAIClient(provider config.ProviderConfig) (*goopenai.Client, error) {
	if strings.TrimSpace(provider.APIKey) == "" && !provider.ByAzure {
		return nil, fmt.Errorf("provider %q apiKey is required", provider.ID)
	}

	var cfg goopenai.ClientConfig
	if provider.ByAzure {
		cfg = goopenai.DefaultAzureConfig(provider.APIKey, provider.BaseURL)
		if strings.TrimSpace(provider.APIVersion) != "" {
			cfg.APIVersion = provider.APIVersion
		}
	} else {
		cfg = goopenai.DefaultConfig(provider.APIKey)
		if strings.TrimSpace(provider.BaseURL) != "" {
			cfg.BaseURL = provider.BaseURL
		}
	}
	cfg.HTTPClient = &http.Client{Timeout: provider.Timeout}
	return goopenai.NewClientWithConfig(cfg), nil
}

func (e *Executor) resolveModel(raw string) (config.ProviderConfig, string, error) {
	provider, modelName, _, err := e.resolveModelForPurpose(raw, false)
	return provider, modelName, err
}

func (e *Executor) resolveModelForPurpose(raw string, wantEmbedding bool) (config.ProviderConfig, string, *config.ModelConfig, error) {
	modelRef := strings.TrimSpace(raw)
	if modelRef == "" {
		return config.ProviderConfig{}, "", nil, fmt.Errorf("agent model is required and must use provider/model format")
	}

	parts := strings.SplitN(modelRef, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return config.ProviderConfig{}, "", nil, fmt.Errorf("invalid model %q, expected provider/model", modelRef)
	}

	providerID := strings.TrimSpace(parts[0])
	modelName := strings.TrimSpace(parts[1])
	provider, ok := e.providers[providerID]
	if !ok {
		return config.ProviderConfig{}, "", nil, fmt.Errorf("unknown provider %q", providerID)
	}
	mc := provider.ResolveModel(modelName)
	if mc != nil {
		if wantEmbedding && !mc.IsEmbedding() {
			return config.ProviderConfig{}, "", nil, fmt.Errorf("model %q is not configured as an embedding model", modelRef)
		}
		if !wantEmbedding && mc.IsEmbedding() {
			return config.ProviderConfig{}, "", nil, fmt.Errorf("model %q is configured as an embedding model", modelRef)
		}
	}
	return provider, modelName, mc, nil
}

func (e *Executor) SupportsVision(agent Definition) bool {
	if e == nil {
		return true
	}
	provider, modelName, _, err := e.resolveModelForPurpose(agent.Model, false)
	if err != nil {
		return false
	}
	if mc := provider.ResolveModel(modelName); mc != nil {
		return mc.SupportsModality("image")
	}
	if provider.SupportsVision != nil {
		return *provider.SupportsVision
	}
	if provider.ByAzure {
		return true
	}
	baseURL := strings.ToLower(strings.TrimSpace(provider.BaseURL))
	if baseURL == "" {
		return strings.EqualFold(strings.TrimSpace(provider.ID), "openai")
	}
	return strings.Contains(baseURL, "openai.com")
}

func (e *Executor) SupportsModality(agent Definition, modality string) bool {
	if e == nil {
		return false
	}
	provider, modelName, _, err := e.resolveModelForPurpose(agent.Model, false)
	if err != nil {
		return false
	}
	if mc := provider.ResolveModel(modelName); mc != nil {
		return mc.SupportsModality(modality)
	}
	if modality == "image" {
		return e.SupportsVision(agent)
	}
	return false
}

func (e *Executor) GenerateWithModel(ctx context.Context, modelRef string, messages []*schema.Message) (*schema.Message, error) {
	if e == nil {
		return nil, fmt.Errorf("executor config is missing")
	}
	provider, modelName, _, err := e.resolveModelForPurpose(modelRef, false)
	if err != nil {
		return nil, err
	}
	m, err := e.newOpenAIChatModel(ctx, provider, modelName, Definition{Model: modelRef})
	if err != nil {
		return nil, err
	}
	return m.Generate(ctx, messages)
}

func float32Ptr(v float32) *float32 { return &v }

func intPtr(v int) *int { return &v }
