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

	reasoning, err := parseOpenAIMessages([]openAIMessage{
		{Role: "assistant", Content: json.RawMessage(`"hi"`), ReasoningContent: "thought it through"},
	})
	if err != nil {
		t.Fatalf("parseOpenAIMessages() with reasoning error = %v", err)
	}
	if reasoning[0].ReasoningContent != "thought it through" {
		t.Fatalf("reasoning content was dropped: %#v", reasoning[0])
	}

	payload, err := json.Marshal(newOpenAIResponse("deepseek/reasoner", reasoning[0]))
	if err != nil {
		t.Fatalf("marshal response error = %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("unmarshal response error = %v", err)
	}
	message := response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if message["reasoning_content"] != "thought it through" {
		t.Fatalf("response reasoning_content missing: %#v", message)
	}

	chunkPayload, err := json.Marshal(newOpenAIChunk("deepseek/reasoner", reasoning[0]))
	if err != nil {
		t.Fatalf("marshal chunk error = %v", err)
	}
	var chunk map[string]any
	if err := json.Unmarshal(chunkPayload, &chunk); err != nil {
		t.Fatalf("unmarshal chunk error = %v", err)
	}
	delta := chunk["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if delta["reasoning_content"] != "thought it through" {
		t.Fatalf("chunk reasoning_content missing: %#v", delta)
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
