package agent

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestNormalizeMessageContentForWirePatchesEmptyAssistantToolCall(t *testing.T) {
	original := &schema.Message{
		Role:    schema.Assistant,
		Content: "",
		ToolCalls: []schema.ToolCall{{
			ID:       "call-1",
			Type:     "function",
			Function: schema.FunctionCall{Name: "memory_search", Arguments: `{"query":"tea"}`},
		}},
	}

	normalized := normalizeMessageContentForWire([]*schema.Message{original})

	if len(normalized) != 1 {
		t.Fatalf("got %d messages, want 1", len(normalized))
	}
	if normalized[0] == original {
		t.Fatal("expected a copy, got the original pointer")
	}
	if normalized[0].Content != " " {
		t.Fatalf("content = %q, want a single space", normalized[0].Content)
	}
	if original.Content != "" {
		t.Fatalf("original message was mutated: content = %q", original.Content)
	}
	if len(normalized[0].ToolCalls) != 1 || normalized[0].ToolCalls[0].ID != "call-1" {
		t.Fatalf("tool calls were not preserved: %#v", normalized[0].ToolCalls)
	}
}

func TestNormalizeMessageContentForWireLeavesTextMessageUntouched(t *testing.T) {
	original := &schema.Message{Role: schema.User, Content: "hello"}

	normalized := normalizeMessageContentForWire([]*schema.Message{original})

	if len(normalized) != 1 || normalized[0] != original {
		t.Fatal("expected the original slice and pointer to be returned unchanged")
	}
	if original.Content != "hello" {
		t.Fatalf("content = %q, want %q", original.Content, "hello")
	}
}

func TestNormalizeMessageContentForWireLeavesMultiContentUntouched(t *testing.T) {
	original := &schema.Message{
		Role:    schema.User,
		Content: "",
		UserInputMultiContent: []schema.MessageInputPart{{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{URL: new("https://example.test/img.png")},
			},
		}},
	}

	normalized := normalizeMessageContentForWire([]*schema.Message{original})

	if len(normalized) != 1 || normalized[0] != original {
		t.Fatal("expected the multi-content message to be returned unchanged")
	}
	if len(original.UserInputMultiContent) != 1 {
		t.Fatalf("multi-content parts were mutated: %#v", original.UserInputMultiContent)
	}
}

func TestNormalizeMessageContentForWireReturnsInputWhenNothingToFix(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.System, Content: "system"},
		{Role: schema.User, Content: "hi"},
		{Role: schema.Tool, Content: "result", ToolCallID: "call-1"},
	}

	normalized := normalizeMessageContentForWire(messages)

	if &normalized[0] != &messages[0] {
		t.Fatal("expected the input slice to be returned unchanged")
	}
}
