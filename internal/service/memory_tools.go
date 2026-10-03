package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"

	"src.solsynth.dev/sosys/persona/internal/agent"
	"src.solsynth.dev/sosys/persona/internal/database"
	"src.solsynth.dev/sosys/persona/internal/humanize"
)

const memorySearchToolName = "memory_search"
const memorySaveToolName = "memory_save"
const memoryForgetToolName = "memory_forget"

func isMemoryToolName(name string) bool {
	switch name {
	case memorySearchToolName, memorySaveToolName, memoryForgetToolName:
		return true
	default:
		return false
	}
}

func memoryScopeParam() *schema.ParameterInfo {
	return &schema.ParameterInfo{
		Type: schema.String,
		Desc: `Which store to act on: "user" (default) is what you know about the person you are talking to, "agent" is your own persistent identity notes shared across all conversations.`,
	}
}

func (s *ConversationService) memorySearchToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: memorySearchToolName,
		Desc: "Search durable memories. Use this when a relevant personal detail is not already in the system context.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Type:     schema.String,
				Desc:     "A name, preference, place, project, or phrase to search for.",
				Required: true,
			},
			"scope": memoryScopeParam(),
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
		Desc: "Save one durable memory. With scope \"user\", save after the user explicitly asks you to remember something or clearly confirms it. With scope \"agent\", record a stable detail about yourself that should stay consistent across conversations.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"category": {Type: schema.String, Desc: "Memory category such as identity, preference, location, work, lore, or project.", Required: true},
			"key":      {Type: schema.String, Desc: "Stable key such as preferred_name, favorite_drink, or current_project.", Required: true},
			"content":  {Type: schema.String, Desc: "The concise fact to remember.", Required: true},
			"scope":    memoryScopeParam(),
		}),
	}
}

func (s *ConversationService) memoryForgetToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: memoryForgetToolName,
		Desc: "Forget one durable memory by its memory ID, whether it is a memory about the user or one of your own notes. Use only when asked to forget or correct it.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"memory_id": {Type: schema.String, Desc: "The memory ID returned by memory_search.", Required: true},
		}),
	}
}

// memoryScope validates the optional scope argument shared by the memory
// tools. An empty value means the memories about the talking user, which is
// what the model reaches for unless it is deliberately writing about itself.
func memoryScope(value string) (string, error) {
	switch scope := strings.TrimSpace(strings.ToLower(value)); scope {
	case "":
		return humanize.MemoryScopeUser, nil
	case humanize.MemoryScopeUser, humanize.MemoryScopeAgent:
		return scope, nil
	default:
		return "", fmt.Errorf("scope must be %q or %q", humanize.MemoryScopeUser, humanize.MemoryScopeAgent)
	}
}

func (s *ConversationService) executeMemoryToolCall(ctx context.Context, def agent.Definition, accountID string, retention humanize.MemoryRetention, call schema.ToolCall) (*executedChatToolResult, error) {
	var payload any
	switch call.Function.Name {
	case memorySearchToolName:
		var input struct {
			Query string `json:"query"`
			Scope string `json:"scope"`
			Limit int    `json:"limit"`
		}
		if err := decodeToolCallArgs(call, &input); err != nil {
			return nil, fmt.Errorf("decode %s arguments: %w", call.Function.Name, err)
		}
		if strings.TrimSpace(input.Query) == "" {
			return nil, fmt.Errorf("%s requires query", call.Function.Name)
		}
		scope, err := memoryScope(input.Scope)
		if err != nil {
			return nil, err
		}
		var memories []memoryRecord
		if scope == humanize.MemoryScopeAgent {
			notes, err := s.humanize.ListSelfNotes(ctx, def.ID, input.Query, input.Limit)
			if err != nil {
				return nil, err
			}
			memories = make([]memoryRecord, 0, len(notes))
			for _, note := range notes {
				memories = append(memories, newMemoryRecord(note))
			}
		} else {
			records, err := s.ListMemories(ctx, accountID, def.ID, input.Query, input.Limit)
			if err != nil {
				return nil, err
			}
			memories = make([]memoryRecord, 0, len(records))
			for _, record := range records {
				memories = append(memories, newMemoryRecord(record))
			}
		}
		payload = map[string]any{"scope": scope, "memories": memories}
	case memorySaveToolName:
		var input struct {
			Category string `json:"category"`
			Key      string `json:"key"`
			Content  string `json:"content"`
			Scope    string `json:"scope"`
		}
		if err := decodeToolCallArgs(call, &input); err != nil {
			return nil, fmt.Errorf("decode %s arguments: %w", call.Function.Name, err)
		}
		scope, err := memoryScope(input.Scope)
		if err != nil {
			return nil, err
		}
		// Retention is about what a grouped conversation taught about the
		// person; an agent-scope note is global identity and carries no group.
		if scope != humanize.MemoryScopeUser {
			retention = humanize.MemoryRetention{}
		}
		memory, err := s.SaveMemory(ctx, accountID, def.ID, humanize.MemoryInput{
			Scope: scope, Category: input.Category, Key: input.Key, Content: input.Content,
			Confidence: 1, Confirmed: true, Pinned: retention.Pinned, GroupID: retention.GroupID,
		})
		if err != nil {
			return nil, err
		}
		payload = map[string]any{"scope": scope, "memory": newMemoryRecord(*memory), "saved": true}
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

// memoryRecord is the tool-facing view of one memory. Both scopes share the
// store, so they share one wire shape.
type memoryRecord struct {
	ID         string  `json:"id"`
	Scope      string  `json:"scope"`
	Category   string  `json:"category"`
	Key        string  `json:"key"`
	Content    string  `json:"content"`
	Confidence float32 `json:"confidence"`
	Confirmed  bool    `json:"confirmed"`
	Pinned     bool    `json:"pinned"`
	GroupID    string  `json:"group_id"`
}

func newMemoryRecord(record database.AgentMemory) memoryRecord {
	return memoryRecord{
		ID: record.ID, Scope: record.Scope, Category: record.Category, Key: record.Key,
		Content: record.Content, Confidence: record.Confidence, Confirmed: record.Confirmed,
		Pinned: record.Pinned, GroupID: record.GroupID,
	}
}
