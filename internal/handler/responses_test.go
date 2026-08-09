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
