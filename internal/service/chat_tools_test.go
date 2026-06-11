package service

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
)

type stubSolarBridge struct {
	roomID     string
	messageID  string
	messageIDs []string
	lastAgent  string
	lastRoom   string
	lastTarget string
	lastBody   string
	sendCount  int
}

func (s *stubSolarBridge) SendBotMessage(_ context.Context, agentID, roomID, targetAccountName, content string) (string, string, error) {
	s.lastAgent = agentID
	s.lastRoom = roomID
	s.lastTarget = targetAccountName
	s.lastBody = content
	s.sendCount++
	if len(s.messageIDs) >= s.sendCount {
		return s.roomID, s.messageIDs[s.sendCount-1], nil
	}
	return s.roomID, s.messageID, nil
}

func (s *stubSolarBridge) TrackRoom(agentID, roomID string) {
	s.lastAgent = agentID
	s.lastRoom = roomID
}

func TestExecuteChatToolCallRequiresDestination(t *testing.T) {
	svc := &ConversationService{solar: &stubSolarBridge{}}
	_, err := svc.executeChatToolCall(context.Background(), "support-bot", schema.ToolCall{
		Function: schema.FunctionCall{
			Name:      sendChatToolName,
			Arguments: `{"message":"hello"}`,
		},
	})
	if err == nil {
		t.Fatal("expected destination validation error")
	}
}

func TestExecuteChatToolCallSendsMessage(t *testing.T) {
	bridge := &stubSolarBridge{roomID: "room-1", messageID: "msg-1"}
	svc := &ConversationService{solar: bridge}

	result, err := svc.executeChatToolCall(context.Background(), "support-bot", schema.ToolCall{
		Function: schema.FunctionCall{
			Name:      sendChatToolName,
			Arguments: `{"room_id":"room-1","message":"hello from bot"}`,
		},
	})
	if err != nil {
		t.Fatalf("executeChatToolCall() error = %v", err)
	}
	if bridge.lastAgent != "support-bot" {
		t.Fatalf("expected agent support-bot, got %q", bridge.lastAgent)
	}
	if bridge.lastRoom != "room-1" {
		t.Fatalf("expected room room-1, got %q", bridge.lastRoom)
	}
	if bridge.lastBody != "hello from bot" {
		t.Fatalf("expected message body to be forwarded, got %q", bridge.lastBody)
	}
	if result == nil || result.Content == "" {
		t.Fatal("expected tool result payload")
	}
}

func TestExecuteChatBatchToolCallSendsMessages(t *testing.T) {
	bridge := &stubSolarBridge{
		roomID:     "room-1",
		messageIDs: []string{"msg-1", "msg-2"},
	}
	svc := &ConversationService{solar: bridge}

	result, err := svc.executeChatToolCall(context.Background(), "support-bot", schema.ToolCall{
		ID: "call-1",
		Function: schema.FunctionCall{
			Name:      sendChatBatchToolName,
			Arguments: `{"room_id":"room-1","messages":["hello","world"]}`,
		},
	})
	if err != nil {
		t.Fatalf("executeChatToolCall() error = %v", err)
	}
	if bridge.sendCount != 2 {
		t.Fatalf("expected 2 sends, got %d", bridge.sendCount)
	}
	if bridge.lastBody != "world" {
		t.Fatalf("expected final message body to be forwarded, got %q", bridge.lastBody)
	}
	if result == nil || result.ToolName != sendChatBatchToolName {
		t.Fatal("expected batch tool result payload")
	}
}
