package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"

	"src.solsynth.dev/sosys/personality/internal/agent"
)

const memorySearchToolName = "memory_search"
const memorySaveToolName = "memory_save"
const memoryForgetToolName = "memory_forget"

func (s *ConversationService) memorySearchToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: memorySearchToolName,
		Desc: "Search the user's durable memories. Use this when a relevant personal detail is not already in the system context.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Type:     schema.String,
				Desc:     "A name, preference, place, project, or phrase to search for.",
				Required: true,
			},
			"limit": {
				Type: schema.Integer,
				Desc: "Maximum number of memories to return. Defaults to 8.",
			},
		}),
	}
}

func (s *ConversationService) memorySaveToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: memorySaveToolName,
		Desc: "Save one durable user memory after the user explicitly asks you to remember it or clearly confirms it. Keep the key stable and the content concise.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"category": {Type: schema.String, Desc: "Memory category such as identity, preference, location, or work.", Required: true},
			"key":      {Type: schema.String, Desc: "Stable key such as preferred_name or favorite_drink.", Required: true},
			"content":  {Type: schema.String, Desc: "The concise fact to remember.", Required: true},
		}),
	}
}

func (s *ConversationService) memoryForgetToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: memoryForgetToolName,
		Desc: "Forget one durable user memory by its memory ID. Use only when the user asks you to forget or correct it.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"memory_id": {Type: schema.String, Desc: "The memory ID returned by memory_search.", Required: true},
		}),
	}
}

func (s *ConversationService) executeMemoryToolCall(ctx context.Context, def agent.Definition, accountID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var payload any
	switch call.Function.Name {
	case memorySearchToolName:
		var input struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := decodeToolCallArgs(call, &input); err != nil {
			return nil, fmt.Errorf("decode %s arguments: %w", call.Function.Name, err)
		}
		if strings.TrimSpace(input.Query) == "" {
			return nil, fmt.Errorf("%s requires query", call.Function.Name)
		}
		memories, err := s.ListMemories(ctx, accountID, def.ID, input.Query, input.Limit)
		if err != nil {
			return nil, err
		}
		items := make([]map[string]any, 0, len(memories))
		for _, memory := range memories {
			items = append(items, map[string]any{
				"id": memory.ID, "category": memory.Category, "key": memory.Key,
				"content": memory.Content, "confidence": memory.Confidence,
				"confirmed": memory.Confirmed, "updated_at": memory.UpdatedAt,
			})
		}
		payload = map[string]any{"memories": items}
	case memorySaveToolName:
		var input struct {
			Category string `json:"category"`
			Key      string `json:"key"`
			Content  string `json:"content"`
		}
		if err := decodeToolCallArgs(call, &input); err != nil {
			return nil, fmt.Errorf("decode %s arguments: %w", call.Function.Name, err)
		}
		memory, err := s.SaveMemory(ctx, accountID, def.ID, MemoryInput{
			Scope: "user", Category: input.Category, Key: input.Key, Content: input.Content,
			Confidence: 1, Confirmed: true,
		})
		if err != nil {
			return nil, err
		}
		payload = map[string]any{
			"memory": map[string]any{
				"id": memory.ID, "category": memory.Category, "key": memory.Key,
				"content": memory.Content, "confirmed": memory.Confirmed,
			},
			"saved": true,
		}
	case memoryForgetToolName:
		var input struct {
			MemoryID string `json:"memory_id"`
		}
		if err := decodeToolCallArgs(call, &input); err != nil {
			return nil, fmt.Errorf("decode %s arguments: %w", call.Function.Name, err)
		}
		if err := s.ForgetMemory(ctx, accountID, def.ID, input.MemoryID); err != nil {
			return nil, err
		}
		payload = map[string]any{"forgotten": true, "memory_id": strings.TrimSpace(input.MemoryID)}
	default:
		return nil, fmt.Errorf("unsupported tool %q", call.Function.Name)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &executedChatToolResult{Content: string(encoded), ToolName: call.Function.Name, ToolCallID: call.ID}, nil
}
