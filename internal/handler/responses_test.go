package handler

import (
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestParseResponseInputSupportsTextAndMessageHistory(t *testing.T) {
	messages, err := parseResponseInput("Be concise.", json.RawMessage(`"Hello"`))
	if err != nil {
		t.Fatalf("parseResponseInput() string error = %v", err)
	}
	if len(messages) != 2 || messages[0].Role != schema.System || messages[1].Role != schema.User || messages[1].Content != "Hello" {
		t.Fatalf("unexpected string input messages: %#v", messages)
	}

	messages, err = parseResponseInput("", json.RawMessage(`[
		{"role":"user","content":[{"type":"input_text","text":"Hi"}]},
		{"role":"assistant","content":"Hello back"}
	]`))
	if err != nil {
		t.Fatalf("parseResponseInput() history error = %v", err)
	}
	if len(messages) != 2 || messages[0].Role != schema.User || messages[0].Content != "Hi" || messages[1].Role != schema.Assistant {
		t.Fatalf("unexpected history messages: %#v", messages)
	}
}

func TestParseResponseInputRejectsEmptyInput(t *testing.T) {
	if _, err := parseResponseInput("", nil); err == nil {
		t.Fatal("parseResponseInput() accepted missing input")
	}
	if _, err := parseResponseInput("", json.RawMessage(`"  "`)); err == nil {
		t.Fatal("parseResponseInput() accepted blank input")
	}
}

func TestParseResponseToolsAndOutputs(t *testing.T) {
	tools, err := parseResponseTools([]responseTool{{
		Type:        "function",
		Name:        "lookup_weather",
		Description: "Look up weather.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
			},
			"required": []any{"city"},
		},
	}})
	if err != nil {
		t.Fatalf("parseResponseTools() error = %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "lookup_weather" || tools[0].ParamsOneOf == nil {
		t.Fatalf("unexpected parsed tool: %#v", tools)
	}

	outputs, err := parseResponseToolOutputs([]responseToolOutput{{
		CallID: "call_1",
		Name:   "lookup_weather",
		Output: json.RawMessage(`{"temperature":22}`),
	}})
	if err != nil {
		t.Fatalf("parseResponseToolOutputs() error = %v", err)
	}
	if len(outputs) != 1 || outputs[0].CallID != "call_1" || outputs[0].Output != `{"temperature":22}` {
		t.Fatalf("unexpected parsed output: %#v", outputs)
	}
}
