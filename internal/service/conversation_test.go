package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
	"src.solsynth.dev/sosys/personality/internal/solar"
)

func TestRunInputUserMessagePayloadSupportsImageParts(t *testing.T) {
	input := RunInput{
		Message: "What is in this image?",
		InputParts: []userMessageInputPart{
			{
				Type:     "image_url",
				ImageURL: "https://example.com/cat.jpg",
				Detail:   "high",
			},
		},
		RequestMetadata: map[string]any{
			"source": "api",
		},
	}

	content, metadata, err := input.userMessagePayload(input.RequestMetadata)
	if err != nil {
		t.Fatalf("userMessagePayload() error = %v", err)
	}
	if content != "What is in this image?" {
		t.Fatalf("content = %q", content)
	}
	if metadata["source"] != "api" {
		t.Fatalf("expected source metadata to be preserved, got %#v", metadata["source"])
	}
	rawParts, ok := metadata["input_parts"].([]userMessageInputPart)
	if !ok {
		t.Fatalf("expected typed input_parts metadata, got %T", metadata["input_parts"])
	}
	if len(rawParts) != 1 || rawParts[0].ImageURL != "https://example.com/cat.jpg" {
		t.Fatalf("unexpected input_parts metadata: %#v", rawParts)
	}

	parts, err := buildSchemaMessageInputParts(input.InputParts, input.Message)
	if err != nil {
		t.Fatalf("buildSchemaMessageInputParts() error = %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("expected text+image parts, got %d", len(parts))
	}
	if parts[0].Type != schema.ChatMessagePartTypeText || parts[0].Text != "What is in this image?" {
		t.Fatalf("unexpected text part: %#v", parts[0])
	}
	if parts[1].Type != schema.ChatMessagePartTypeImageURL || parts[1].Image == nil {
		t.Fatalf("unexpected image part: %#v", parts[1])
	}
	if parts[1].Image.URL == nil || *parts[1].Image.URL != "https://example.com/cat.jpg" {
		t.Fatalf("unexpected image url: %#v", parts[1].Image)
	}
	if parts[1].Image.Detail != schema.ImageURLDetailHigh {
		t.Fatalf("unexpected image detail: %q", parts[1].Image.Detail)
	}
}

func TestRunInputUserMessagePayloadRejectsInvalidBase64ImagePart(t *testing.T) {
	input := RunInput{
		InputParts: []userMessageInputPart{{
			Type:        "image_url",
			ImageBase64: "Zm9v",
		}},
	}

	_, _, err := input.userMessagePayload(nil)
	if err == nil {
		t.Fatal("expected validation error for missing mime_type")
	}
	if !strings.Contains(err.Error(), "mime_type is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildSolarInboundInputPartsUsesDriveFileURL(t *testing.T) {
	svc := &ConversationService{
		cfg: &config.Config{
			SolarNetwork: config.SolarNetworkConfig{
				BaseURL: "https://solar.example",
			},
		},
	}

	parts := svc.buildSolarInboundInputParts([]solar.ChatAttachment{
		{ID: "img-1", MIMEType: "image/png"},
		{ID: "doc-1", MIMEType: "application/pdf"},
	})

	if len(parts) != 1 {
		t.Fatalf("expected 1 supported image part, got %d", len(parts))
	}
	if parts[0].Type != "image_url" {
		t.Fatalf("unexpected part type: %q", parts[0].Type)
	}
	if parts[0].ImageURL != "https://solar.example/drive/files/img-1" {
		t.Fatalf("unexpected image url: %q", parts[0].ImageURL)
	}
	if parts[0].MIMEType != "image/png" {
		t.Fatalf("unexpected mime type: %q", parts[0].MIMEType)
	}
}

func TestResolveImpressionAccountIDFromSolarMetadataUsesSenderAccountID(t *testing.T) {
	db := openTestDB(t)
	svc := NewConversationService(db, &config.Config{}, nil, nil)

	thread := &database.ConversationThread{
		ID:        "thread-1",
		AccountID: "solar:support-bot:room-1",
		AgentID:   "support-bot",
		Title:     "Solar room",
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	message, err := svc.createMessageWithMetadata(context.Background(), thread, nil, "user", "hello", nil, map[string]any{
		"source":            "solar",
		"sender_account_id": "acct-user-1",
	})
	if err != nil {
		t.Fatalf("create message: %v", err)
	}

	got := svc.resolveImpressionAccountIDFromRecord(thread.AccountID, message)
	if got != "acct-user-1" {
		t.Fatalf("expected acct-user-1, got %q", got)
	}
}

func TestSolarSenderIdentityPromptPrefersUsername(t *testing.T) {
	got := solarSenderIdentityPrompt(&solarInboundRequestMetadata{
		SenderAccountID:   "acct-user-1",
		SenderAccountName: "alice",
		SenderNick:        "Alice Chen",
	}, &database.ExternalChatBinding{
		RemoteAccountID: "acct-user-1",
		RemoteAccount:   "alice",
	})

	if !strings.Contains(got, `username="alice"`) {
		t.Fatalf("expected username in prompt, got %q", got)
	}
	if !strings.Contains(got, `display_name="Alice Chen"`) {
		t.Fatalf("expected display name in prompt, got %q", got)
	}
	if !strings.Contains(got, "canonical identity") {
		t.Fatalf("expected canonical identity hint, got %q", got)
	}
}

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

func TestBuildModelMessagesRehydratesUserVisionHistory(t *testing.T) {
	db := openTestDB(t)
	registry, err := agent.NewRegistry([]config.AgentConfig{{
		ID:           "vision-bot",
		Name:         "Vision Bot",
		Model:        "openai/gpt-4.1-mini",
		Abilities:    []string{"chat"},
		Enabled:      true,
		SystemPrompt: "You can inspect images.",
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	svc := NewConversationService(db, &config.Config{
		Personality: config.PersonalityConfig{MaxHistoryMessages: 24},
	}, registry, nil)

	thread := &database.ConversationThread{
		ID:        "thread-vision-1",
		AccountID: "acct-1",
		AgentID:   "vision-bot",
		Title:     "Vision chat",
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if _, err := svc.createMessageWithMetadata(context.Background(), thread, nil, "user", "Describe this image", nil, map[string]any{
		"input_parts": []userMessageInputPart{{
			Type:     "image_url",
			ImageURL: "https://example.com/cat.jpg",
			Detail:   "low",
		}},
	}); err != nil {
		t.Fatalf("create multimodal user message: %v", err)
	}

	messages, _, err := svc.BuildModelMessages(context.Background(), thread.AccountID, thread.ID)
	if err != nil {
		t.Fatalf("BuildModelMessages() error = %v", err)
	}

	var userMsg *schema.Message
	for _, msg := range messages {
		if msg.Role == schema.User {
			userMsg = msg
			break
		}
	}
	if userMsg == nil {
		t.Fatal("expected user message to be present")
	}
	if userMsg.Content != "" {
		t.Fatalf("expected user content to be moved into multimodal parts, got %q", userMsg.Content)
	}
	if len(userMsg.UserInputMultiContent) != 2 {
		t.Fatalf("expected text+image multimodal parts, got %d", len(userMsg.UserInputMultiContent))
	}
	if userMsg.UserInputMultiContent[0].Type != schema.ChatMessagePartTypeText {
		t.Fatalf("unexpected first part: %#v", userMsg.UserInputMultiContent[0])
	}
	if userMsg.UserInputMultiContent[1].Image == nil || userMsg.UserInputMultiContent[1].Image.URL == nil {
		t.Fatalf("unexpected second part: %#v", userMsg.UserInputMultiContent[1])
	}
	if *userMsg.UserInputMultiContent[1].Image.URL != "https://example.com/cat.jpg" {
		t.Fatalf("unexpected image url: %#v", userMsg.UserInputMultiContent[1].Image)
	}
	if userMsg.UserInputMultiContent[1].Image.Detail != schema.ImageURLDetailLow {
		t.Fatalf("unexpected image detail: %q", userMsg.UserInputMultiContent[1].Image.Detail)
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
		"tool_name": sendChatToolName,
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

func TestBuildModelMessagesSkipsOrphanedToolMessageWithUnknownToolCallID(t *testing.T) {
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
		"tool_call_id": "fallback:run-1",
		"tool_name":    "fallback_send_chat_message",
	}); err != nil {
		t.Fatalf("create orphaned tool message: %v", err)
	}

	messages, _, err := svc.BuildModelMessages(context.Background(), thread.AccountID, thread.ID)
	if err != nil {
		t.Fatalf("BuildModelMessages() error = %v", err)
	}
	for _, msg := range messages {
		if msg.Role == schema.Tool {
			t.Fatalf("expected orphaned tool message to be skipped")
		}
	}
}

func TestBuildModelMessagesSkipsEmptyAssistantMessage(t *testing.T) {
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
	if _, err := svc.createMessage(context.Background(), thread, nil, "assistant", "", stringPtr("openai/test")); err != nil {
		t.Fatalf("create empty assistant message: %v", err)
	}

	messages, _, err := svc.BuildModelMessages(context.Background(), thread.AccountID, thread.ID)
	if err != nil {
		t.Fatalf("BuildModelMessages() error = %v", err)
	}
	for _, msg := range messages {
		if msg.Role == schema.Assistant && strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 {
			t.Fatal("expected empty assistant message to be skipped")
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
	if bindings[0].EngagementState != solarRoomEngagementStateActive {
		t.Fatalf("expected active engagement state after outbound binding update, got %q", bindings[0].EngagementState)
	}
	if bindings[0].EngagedUntil == nil {
		t.Fatal("expected engaged_until to be set after outbound binding update")
	}
}

func TestAllowSolarRoomReplyForGroupRequiresMentionOrReply(t *testing.T) {
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
	if _, err := svc.createMessageWithMetadata(context.Background(), thread, nil, "user", "hello", nil, map[string]any{
		"source":        "solar",
		"room_type":     0,
		"mentioned_bot": false,
	}); err != nil {
		t.Fatalf("create user message: %v", err)
	}

	allow, err := svc.allowSolarRoomReply(context.Background(), thread, &database.ExternalChatBinding{
		ThreadID:       thread.ID,
		AccountID:      thread.AccountID,
		RemoteRoomType: roomTypePtr(0),
	})
	if err != nil {
		t.Fatalf("allowSolarRoomReply() error = %v", err)
	}
	if allow {
		t.Fatal("expected group reply to be suppressed without mention or reply")
	}
}

func TestAllowSolarRoomReplyForGroupAllowsMention(t *testing.T) {
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
	if _, err := svc.createMessageWithMetadata(context.Background(), thread, nil, "user", "hello @michan", nil, map[string]any{
		"source":        "solar",
		"room_type":     0,
		"mentioned_bot": true,
	}); err != nil {
		t.Fatalf("create user message: %v", err)
	}

	allow, err := svc.allowSolarRoomReply(context.Background(), thread, &database.ExternalChatBinding{
		ThreadID:       thread.ID,
		AccountID:      thread.AccountID,
		RemoteRoomType: roomTypePtr(0),
	})
	if err != nil {
		t.Fatalf("allowSolarRoomReply() error = %v", err)
	}
	if !allow {
		t.Fatal("expected group reply to be allowed after mention")
	}
}

func TestAllowSolarRoomReplyForGroupAllowsActiveWindow(t *testing.T) {
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
	if _, err := svc.createMessageWithMetadata(context.Background(), thread, nil, "user", "follow up", nil, map[string]any{
		"source":        "solar",
		"room_type":     0,
		"mentioned_bot": false,
	}); err != nil {
		t.Fatalf("create user message: %v", err)
	}

	until := time.Now().Add(3 * time.Minute)
	allow, err := svc.allowSolarRoomReply(context.Background(), thread, &database.ExternalChatBinding{
		ThreadID:        thread.ID,
		AccountID:       thread.AccountID,
		RemoteRoomType:  roomTypePtr(0),
		EngagementState: solarRoomEngagementStateActive,
		EngagedUntil:    &until,
	})
	if err != nil {
		t.Fatalf("allowSolarRoomReply() error = %v", err)
	}
	if !allow {
		t.Fatal("expected group reply to be allowed during active window")
	}
}

func TestAllowSolarRoomReplyForGroupSuppressesExpiredActiveWindow(t *testing.T) {
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
	if _, err := svc.createMessageWithMetadata(context.Background(), thread, nil, "user", "random chatter", nil, map[string]any{
		"source":        "solar",
		"room_type":     0,
		"mentioned_bot": false,
	}); err != nil {
		t.Fatalf("create user message: %v", err)
	}

	until := time.Now().Add(-time.Minute)
	allow, err := svc.allowSolarRoomReply(context.Background(), thread, &database.ExternalChatBinding{
		ThreadID:        thread.ID,
		AccountID:       thread.AccountID,
		RemoteRoomType:  roomTypePtr(0),
		EngagementState: solarRoomEngagementStateActive,
		EngagedUntil:    &until,
	})
	if err != nil {
		t.Fatalf("allowSolarRoomReply() error = %v", err)
	}
	if allow {
		t.Fatal("expected group reply to be suppressed after active window expired")
	}
}

func TestEnsureSolarRoomBindingRefreshesActiveWindowOnOutboundReply(t *testing.T) {
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

	initialUntil := time.Now().Add(time.Minute).UTC().Truncate(time.Millisecond)
	if err := db.Create(&database.ExternalChatBinding{
		ID:              "binding-1",
		AgentID:         "support-bot",
		RemoteRoomID:    "room-1",
		RemoteRoomType:  roomTypePtr(0),
		EngagementState: solarRoomEngagementStateActive,
		EngagedUntil:    &initialUntil,
		ThreadID:        thread.ID,
		AccountID:       thread.AccountID,
		RemoteAccount:   "alice",
		LastMessageAt:   &initialUntil,
	}).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}

	sentAt := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Millisecond)
	if err := svc.ensureSolarRoomBinding(context.Background(), thread, "support-bot", "room-1", "alice", sentAt); err != nil {
		t.Fatalf("ensureSolarRoomBinding() error = %v", err)
	}

	var binding database.ExternalChatBinding
	if err := db.Where("agent_id = ? AND remote_room_id = ?", "support-bot", "room-1").First(&binding).Error; err != nil {
		t.Fatalf("reload binding: %v", err)
	}
	if binding.EngagedUntil == nil {
		t.Fatal("expected engaged_until to be set")
	}
	if !binding.EngagedUntil.After(initialUntil) {
		t.Fatalf("expected engaged_until to extend beyond %v, got %v", initialUntil, *binding.EngagedUntil)
	}
	if binding.EngagementState != solarRoomEngagementStateActive {
		t.Fatalf("expected active engagement state, got %q", binding.EngagementState)
	}
}

func TestSolarInboundBatcherCoalescesMessages(t *testing.T) {
	var flushed [][]ExternalInboundMessage
	batcher := newSolarInboundBatcher(20*time.Millisecond, func(_ context.Context, _ string, items []ExternalInboundMessage) error {
		flushed = append(flushed, append([]ExternalInboundMessage(nil), items...))
		return nil
	})

	now := time.Now().UTC()
	if err := batcher.Enqueue(context.Background(), "support-bot", ExternalInboundMessage{
		RoomID:      "room-1",
		MessageID:   "msg-1",
		MessageType: "text",
		Content:     "hello",
		SenderName:  "alice",
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("enqueue first message: %v", err)
	}
	if err := batcher.Enqueue(context.Background(), "support-bot", ExternalInboundMessage{
		RoomID:      "room-1",
		MessageID:   "msg-2",
		MessageType: "text",
		Content:     "world",
		SenderName:  "bob",
		CreatedAt:   now.Add(5 * time.Millisecond),
	}); err != nil {
		t.Fatalf("enqueue second message: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

	if len(flushed) != 1 {
		t.Fatalf("expected 1 flushed batch, got %d", len(flushed))
	}
	if len(flushed[0]) != 2 {
		t.Fatalf("expected 2 batched messages, got %d", len(flushed[0]))
	}
	if flushed[0][0].Content != "hello" || flushed[0][1].Content != "world" {
		t.Fatalf("expected batched messages to preserve order, got %#v", flushed[0])
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
