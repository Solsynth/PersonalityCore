package agent

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// emptyContentPlaceholder is the single space used to keep the `content` field
// on the wire for a message whose text is legitimately empty. A space adds no
// words to the conversation, unlike any other filler text.
const emptyContentPlaceholder = " "

// normalizeMessageContentForWire returns messages that are safe to send to the
// model provider.
//
// The provider requires a `content` field on every message. The OpenAI client
// library serializes a plain message with `content,omitempty`, so an empty text
// body is dropped from the JSON entirely instead of being sent as an empty
// string. Two legitimate turns end up with empty text and no multi-content
// parts: an assistant turn that only called tools, and a replayed
// attachment-only turn whose files no longer resolve. Such a message is copied
// with its Content set to a single space so the provider still sees the field.
//
// The original messages are never mutated: each fixed message is a shallow
// copy. When nothing needs fixing the input slice is returned as-is, with no
// allocation.
func normalizeMessageContentForWire(messages []*schema.Message) []*schema.Message {
	var patched []*schema.Message
	for i, msg := range messages {
		if msg == nil || msg.Content != "" {
			continue
		}
		if len(msg.MultiContent) > 0 || len(msg.UserInputMultiContent) > 0 || len(msg.AssistantGenMultiContent) > 0 {
			continue
		}
		if patched == nil {
			patched = make([]*schema.Message, len(messages))
			copy(patched, messages)
		}
		clone := *msg
		clone.Content = emptyContentPlaceholder
		patched[i] = &clone
	}
	if patched == nil {
		return messages
	}
	return patched
}

// contentNormalizingToolModel wraps a tool-bound chat model and applies
// normalizeMessageContentForWire to every outgoing request. The service layer
// drives the tool loop itself and calls the model returned by
// Executor.NewToolCallingModel directly, so this decorator is what keeps those
// requests well-formed.
type contentNormalizingToolModel struct {
	inner model.ToolCallingChatModel
}

var (
	_ model.BaseChatModel        = (*contentNormalizingToolModel)(nil)
	_ model.ToolCallingChatModel = (*contentNormalizingToolModel)(nil)
)

func (m *contentNormalizingToolModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.inner.Generate(ctx, normalizeMessageContentForWire(input), opts...)
}

func (m *contentNormalizingToolModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.inner.Stream(ctx, normalizeMessageContentForWire(input), opts...)
}

func (m *contentNormalizingToolModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	inner, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &contentNormalizingToolModel{inner: inner}, nil
}
