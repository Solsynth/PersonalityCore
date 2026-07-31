package handler

import (
	"encoding/json"
	"testing"
)

func TestParseOpenAIRequestSupportsToolHistoryAndJSONSchema(t *testing.T) {
	messages, err := parseOpenAIMessages([]openAIMessage{
		{Role: "user", Content: json.RawMessage(`"weather in Taipei"`)},
		{Role: "assistant", ToolCalls: []openAIToolCall{{ID: "call_1", Type: "function", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "weather", Arguments: `{"city":"Taipei"}`}}}},
		{Role: "tool", ToolCallID: "call_1", Name: "weather", Content: json.RawMessage(`"sunny"`)},
	})
	if err != nil {
		t.Fatalf("parseOpenAIMessages() error = %v", err)
	}
	if len(messages) != 3 || messages[1].ToolCalls[0].ID != "call_1" || messages[2].ToolCallID != "call_1" {
		t.Fatalf("tool history was not preserved: %#v", messages)
	}

	tool := openAITool{Type: "function"}
	tool.Function.Name = "weather"
	tool.Function.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city": map[string]any{"type": "string", "description": "City name"},
		},
		"required": []any{"city"},
	}
	tools, err := parseOpenAITools([]openAITool{tool})
	if err != nil {
		t.Fatalf("parseOpenAITools() error = %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "weather" || tools[0].ParamsOneOf == nil {
		t.Fatalf("tool schema was not converted: %#v", tools)
	}
}
