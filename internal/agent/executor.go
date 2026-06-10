package agent

import (
	"context"
	"fmt"
	"io"
	"strings"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"

	"src.solsynth.dev/sosys/personality/internal/config"
)

type RunRequest struct {
	Agent    Definition
	Messages []*schema.Message
}

type Executor struct {
	cfg *config.Config
}

func NewExecutor(cfg *config.Config) *Executor {
	return &Executor{cfg: cfg}
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

func (e *Executor) newChatModel(ctx context.Context, agent Definition) (*einoopenai.ChatModel, error) {
	if e == nil || e.cfg == nil {
		return nil, fmt.Errorf("executor config is missing")
	}
	if strings.TrimSpace(e.cfg.LLM.APIKey) == "" && !e.cfg.LLM.ByAzure {
		return nil, fmt.Errorf("llm api key is required")
	}

	modelName := strings.TrimSpace(agent.Model)
	if modelName == "" {
		modelName = strings.TrimSpace(e.cfg.LLM.Model)
	}
	if modelName == "" {
		return nil, fmt.Errorf("llm model is required")
	}

	temperature := e.cfg.LLM.Temperature
	if agent.Temperature != nil {
		temperature = *agent.Temperature
	}
	topP := e.cfg.LLM.TopP
	if agent.TopP != nil {
		topP = *agent.TopP
	}
	maxTokens := e.cfg.LLM.MaxCompletionTokens
	if agent.MaxCompletionTokens != nil {
		maxTokens = *agent.MaxCompletionTokens
	}

	return einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		APIKey:              e.cfg.LLM.APIKey,
		BaseURL:             e.cfg.LLM.BaseURL,
		ByAzure:             e.cfg.LLM.ByAzure,
		APIVersion:          e.cfg.LLM.APIVersion,
		Model:               modelName,
		Timeout:             e.cfg.LLM.Timeout,
		MaxCompletionTokens: intPtr(maxTokens),
		Temperature:         float32Ptr(temperature),
		TopP:                float32Ptr(topP),
	})
}

func CollectStreamContent(reader *schema.StreamReader[*schema.Message]) (*schema.Message, error) {
	if reader == nil {
		return nil, fmt.Errorf("stream reader is nil")
	}
	defer reader.Close()

	var chunks []*schema.Message
	for {
		chunk, err := reader.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if chunk != nil {
			chunks = append(chunks, chunk)
		}
	}

	if len(chunks) == 0 {
		return &schema.Message{Role: schema.Assistant, Content: ""}, nil
	}
	return schema.ConcatMessages(chunks)
}

func float32Ptr(v float32) *float32 { return &v }

func intPtr(v int) *int { return &v }
