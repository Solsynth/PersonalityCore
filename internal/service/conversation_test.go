package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
)

func TestBuildModelMessagesRehydratesToolHistory(t *testing.T) {
	db := openTestDB(t)
	registry, err := agent.NewRegistry([]config.AgentConfig{{
		ID:           "support-bot",
		Name:         "Support Bot",
		Model:        "openai/test",
		Abilities:    []string{"chat"},
		Enabled:      true,
		SystemPrompt: "You are helpful.",
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	svc := NewConversationService(db, &config.Config{
		Personality: config.PersonalityConfig{MaxHistoryMessages: 24},
	}, registry, nil)

	thread := &database.ConversationThread{
		ID:        "thread-1",
		AccountID: "solar:support-bot:room-1",
		AgentID:   "support-bot",
		Title:     "Solar room",
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if _, err := svc.createMessage(context.Background(), thread, nil, "user", "hello", nil); err != nil {
		t.Fatalf("create user message: %v", err)
	}
	if _, err := svc.createMessageWithMetadata(context.Background(), thread, nil, "assistant", "", stringPtr("openai/test"), map[string]any{
		"tool_calls": []schema.ToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      sendChatToolName,
				Arguments: `{"room_id":"room-1","message":"hi"}`,
			},
		}},
		"reasoning_content": "Need to reply in the room.",
	}); err != nil {
		t.Fatalf("create assistant tool message: %v", err)
	}
	if _, err := svc.createMessageWithMetadata(context.Background(), thread, nil, "tool", `{"ok":true}`, nil, map[string]any{
		"tool_call_id": "call-1",
		"tool_name":    sendChatToolName,
	}); err != nil {
		t.Fatalf("create tool result message: %v", err)
	}

	messages, def, err := svc.BuildModelMessages(context.Background(), thread.AccountID, thread.ID)
	if err != nil {
		t.Fatalf("BuildModelMessages() error = %v", err)
	}
	if def.ID != "support-bot" {
		t.Fatalf("expected support-bot definition, got %q", def.ID)
	}
	if len(messages) < 4 {
		t.Fatalf("expected at least 4 messages including system prompt and tool history, got %d", len(messages))
	}

	var assistantMsg *schema.Message
	var toolMsg *schema.Message
	for _, msg := range messages {
		if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			assistantMsg = msg
		}
		if msg.Role == schema.Tool && msg.ToolCallID != "" {
			toolMsg = msg
		}
	}

	if assistantMsg == nil {
		t.Fatal("expected assistant tool-call message to be rehydrated")
	}
	if assistantMsg.ToolCalls[0].ID != "call-1" {
		t.Fatalf("expected tool call id call-1, got %q", assistantMsg.ToolCalls[0].ID)
	}
	if assistantMsg.ReasoningContent != "Need to reply in the room." {
		t.Fatalf("expected reasoning content to be preserved, got %q", assistantMsg.ReasoningContent)
	}
	if toolMsg == nil {
		t.Fatal("expected tool result message to be rehydrated")
	}
	if toolMsg.ToolName != sendChatToolName {
		t.Fatalf("expected tool name %q, got %q", sendChatToolName, toolMsg.ToolName)
	}
}

func TestBuildModelMessagesSkipsToolMessageWithoutToolCallID(t *testing.T) {
	db := openTestDB(t)
	registry, err := agent.NewRegistry([]config.AgentConfig{{
		ID:           "support-bot",
		Name:         "Support Bot",
		Model:        "openai/test",
		Abilities:    []string{"chat"},
		Enabled:      true,
		SystemPrompt: "You are helpful.",
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	svc := NewConversationService(db, &config.Config{
		Personality: config.PersonalityConfig{MaxHistoryMessages: 24},
	}, registry, nil)

	thread := &database.ConversationThread{
		ID:        "thread-1",
		AccountID: "solar:support-bot:room-1",
		AgentID:   "support-bot",
		Title:     "Solar room",
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if _, err := svc.createMessage(context.Background(), thread, nil, "user", "hello", nil); err != nil {
		t.Fatalf("create user message: %v", err)
	}
	if _, err := svc.createMessageWithMetadata(context.Background(), thread, nil, "tool", `{"ok":true}`, nil, map[string]any{
		"tool_name": "fallback_send_chat_message",
	}); err != nil {
		t.Fatalf("create fallback tool message: %v", err)
	}

	messages, _, err := svc.BuildModelMessages(context.Background(), thread.AccountID, thread.ID)
	if err != nil {
		t.Fatalf("BuildModelMessages() error = %v", err)
	}
	for _, msg := range messages {
		if msg.Role == schema.Tool {
			t.Fatalf("expected tool message without tool_call_id to be skipped")
		}
	}
}

func TestEnsureSolarRoomBindingUpsertsByRoom(t *testing.T) {
	db := openTestDB(t)
	svc := &ConversationService{db: db}

	thread := &database.ConversationThread{
		ID:        "thread-1",
		AccountID: "solar:support-bot:room-1",
		AgentID:   "support-bot",
		Title:     "Solar room",
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}

	seenAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := svc.ensureSolarRoomBinding(context.Background(), thread, "support-bot", "room-1", "alice", seenAt); err != nil {
		t.Fatalf("ensureSolarRoomBinding() create error = %v", err)
	}
	if err := svc.ensureSolarRoomBinding(context.Background(), thread, "support-bot", "room-1", "", seenAt.Add(time.Minute)); err != nil {
		t.Fatalf("ensureSolarRoomBinding() update error = %v", err)
	}

	var bindings []database.ExternalChatBinding
	if err := db.Order("created_at ASC").Find(&bindings).Error; err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(bindings))
	}
	if bindings[0].ThreadID != thread.ID {
		t.Fatalf("expected thread %q, got %q", thread.ID, bindings[0].ThreadID)
	}
	if bindings[0].RemoteAccount != "alice" {
		t.Fatalf("expected remote account alice, got %q", bindings[0].RemoteAccount)
	}
	if bindings[0].LastMessageAt == nil || !bindings[0].LastMessageAt.Equal(seenAt.Add(time.Minute)) {
		t.Fatalf("expected last message timestamp to be updated")
	}
}

func openTestDB(t *testing.T) *database.DB {
	t.Helper()

	raw, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	db := &database.DB{DB: raw}
	if err := db.AutoMigrate(); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}
