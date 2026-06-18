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
	"src.solsynth.dev/sosys/personality/internal/humanize"
	"src.solsynth.dev/sosys/personality/internal/solar_network"
)

func TestRunInputUserMessagePayloadSupportsAttachmentIDs(t *testing.T) {
	svc := &ConversationService{cfg: &config.Config{}}
	input := RunInput{
		Message:       "What is in this image?",
		AttachmentIDs: []string{"file-123"},
		RequestMetadata: map[string]any{
			"source": "api",
		},
	}

	content, metadata, err := svc.userMessagePayload(input, input.RequestMetadata, 0)
	if err != nil {
		t.Fatalf("userMessagePayload() error = %v", err)
	}
	if content != "What is in this image?" {
		t.Fatalf("content = %q", content)
	}
	if metadata["source"] != "api" {
		t.Fatalf("expected source metadata to be preserved, got %#v", metadata["source"])
	}
	if metadata["input_parts"] != nil && metadata["attachment_ids"] == nil {
		t.Fatalf("expected attachment_ids or no metadata, got %#v", metadata)
	}
}

func TestRunInputUserMessagePayloadRejectsEmptyInput(t *testing.T) {
	svc := &ConversationService{cfg: &config.Config{}}
	input := RunInput{}

	_, _, err := svc.userMessagePayload(input, nil, 0)
	if err == nil {
		t.Fatal("expected validation error for empty input")
	}
	if !strings.Contains(err.Error(), "message, input_parts, or attachment_ids is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildSolarInboundInputPartsUsesDriveFileURL(t *testing.T) {
	svc := &ConversationService{
		cfg: &config.Config{
			SolarNetwork: config.SolarNetworkConfig{
				BaseURL: "https://solar_network.example",
			},
		},
	}

	parts := svc.buildSnInboundInputParts([]solar_network.ChatAttachment{
		{ID: "img-1", MIMEType: "image/png"},
		{ID: "doc-1", MIMEType: "application/pdf"},
	})

	if len(parts) != 1 {
		t.Fatalf("expected 1 supported image part, got %d", len(parts))
	}
	if parts[0].Type != "image" {
		t.Fatalf("unexpected part type: %q", parts[0].Type)
	}
	if parts[0].AttachmentID != "img-1" {
		t.Fatalf("unexpected attachment id: %q", parts[0].AttachmentID)
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

func TestBuildModelMessagesPrefixesSolarUserHistoryWithUsername(t *testing.T) {
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
		ID:        "thread-solar-prefix-1",
		AccountID: "solar:support-bot:room-1",
		AgentID:   "support-bot",
		Title:     "Solar room",
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if _, err := svc.createMessageWithMetadata(context.Background(), thread, nil, "user", "hello there", nil, map[string]any{
		"source":              "solar",
		"sender_account_id":   "acct-user-1",
		"sender_account_name": "alice",
		"sender_nick":         "Alice",
	}); err != nil {
		t.Fatalf("create user message: %v", err)
	}

	messages, _, err := svc.BuildModelMessages(context.Background(), thread.AccountID, thread.ID, 0)
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
	if !strings.Contains(userMsg.Content, "[@alice] [") {
		t.Fatalf("expected solar history to include [@alice] [date] prefix, got %q", userMsg.Content)
	}
	if !strings.Contains(userMsg.Content, "] hello there") {
		t.Fatalf("expected solar history content, got %q", userMsg.Content)
	}
}

func TestEffectiveChatAgentDefinitionOverridesMaxCompletionTokens(t *testing.T) {
	baseMax := 1024
	chatMax := 160
	def := agent.Definition{
		ID:                      "chatty",
		MaxCompletionTokens:     &baseMax,
		ChatMaxCompletionTokens: &chatMax,
	}

	got := effectiveChatAgentDefinition(def)
	if got.MaxCompletionTokens == nil || *got.MaxCompletionTokens != 160 {
		t.Fatalf("expected chat max completion tokens 160, got %#v", got.MaxCompletionTokens)
	}
	if def.MaxCompletionTokens == nil || *def.MaxCompletionTokens != 1024 {
		t.Fatalf("expected original definition to remain 1024, got %#v", def.MaxCompletionTokens)
	}
}

func TestNewConversationServiceUsesConfiguredChatInboundDebounce(t *testing.T) {
	db := openTestDB(t)
	svc := NewConversationService(db, &config.Config{
		Personality: config.PersonalityConfig{
			ChatInboundDebounce: 5 * time.Second,
		},
	}, nil, nil)

	if svc.snInbound == nil {
		t.Fatal("expected solar inbound batcher")
	}
	if svc.snInbound.delay != 5*time.Second {
		t.Fatalf("expected debounce delay 5s, got %v", svc.snInbound.delay)
	}
}

func TestSolarSenderIdentityPromptPrefersUsername(t *testing.T) {
	got := snSenderIdentityPrompt(&snInboundRequestMetadata{
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

	messages, def, err := svc.BuildModelMessages(context.Background(), thread.AccountID, thread.ID, 0)
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
			Type:         "image",
			AttachmentID: "file-123",
		}},
	}); err != nil {
		t.Fatalf("create multimodal user message: %v", err)
	}

	messages, _, err := svc.BuildModelMessages(context.Background(), thread.AccountID, thread.ID, 0)
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
	if userMsg.UserInputMultiContent[1].Image.URL == nil {
		t.Fatal("expected image url to be set")
	}
}

func TestBuildModelMessagesFallsBackToTextForTextOnlyProviderHistory(t *testing.T) {
	db := openTestDB(t)
	registry, err := agent.NewRegistry([]config.AgentConfig{{
		ID:           "deepseek-bot",
		Name:         "DeepSeek Bot",
		Model:        "deepseek/deepseek-v4-flash",
		Enabled:      true,
		SystemPrompt: "You can inspect context carefully.",
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	executor, err := agent.NewExecutor(&config.Config{
		Providers: []config.ProviderConfig{{
			ID:             "deepseek",
			Type:           "openai-compatible",
			APIKey:         "test-key",
			SupportsVision: boolPtr(false),
		}},
	})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

	svc := NewConversationService(db, &config.Config{
		Personality: config.PersonalityConfig{MaxHistoryMessages: 24},
	}, registry, executor)

	thread := &database.ConversationThread{
		ID:        "thread-vision-fallback-1",
		AccountID: "acct-1",
		AgentID:   "deepseek-bot",
		Title:     "Vision fallback chat",
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if _, err := svc.createMessageWithMetadata(context.Background(), thread, nil, "user", "What is in this image?", nil, map[string]any{
		"input_parts": []userMessageInputPart{{
			Type:         "image",
			AttachmentID: "file-123",
		}},
	}); err != nil {
		t.Fatalf("create multimodal user message: %v", err)
	}

	messages, _, err := svc.BuildModelMessages(context.Background(), thread.AccountID, thread.ID, 0)
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
	if len(userMsg.UserInputMultiContent) != 0 {
		t.Fatalf("expected text-only fallback, got multimodal parts %#v", userMsg.UserInputMultiContent)
	}
	if !strings.Contains(userMsg.Content, "Image attachment provided but this model only accepts text input") {
		t.Fatalf("expected text fallback note, got %q", userMsg.Content)
	}
}

func TestBuildModelMessagesIncludesAgentIdentityOverlayAndCurrentTime(t *testing.T) {
	db := openTestDB(t)
	registry, err := agent.NewRegistry([]config.AgentConfig{{
		ID:           "michan",
		Name:         "Michan",
		Model:        "openai/test",
		Enabled:      true,
		SystemPrompt: "You are Michan.",
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	svc := NewConversationService(db, &config.Config{
		Personality: config.PersonalityConfig{MaxHistoryMessages: 4},
	}, registry, nil)

	if _, err := svc.humanize.SaveAgentSelfNote(context.Background(), "michan", humanize.AgentSelfNoteInput{
		Key:      "favorite_drink",
		Category: "preference",
		Content:  "I like hojicha lattes.",
	}); err != nil {
		t.Fatalf("SaveAgentSelfNote() error = %v", err)
	}

	thread := &database.ConversationThread{
		ID:        "thread-self-1",
		AccountID: "acct-1",
		AgentID:   "michan",
		Title:     "Identity chat",
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	createdAt := time.Date(2026, 6, 13, 10, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	if err := db.Create(&database.ConversationMessage{
		ID:        "msg-1",
		ThreadID:  thread.ID,
		AccountID: thread.AccountID,
		Role:      "user",
		Content:   "What do you like to drink?",
		Sequence:  1,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}

	messages, _, err := svc.BuildModelMessages(context.Background(), thread.AccountID, thread.ID, 0)
	if err != nil {
		t.Fatalf("BuildModelMessages() error = %v", err)
	}
	if len(messages) < 4 {
		t.Fatalf("expected system, identity, user, and current-time messages, got %d", len(messages))
	}
	foundIdentity := false
	for _, msg := range messages {
		if msg.Role == schema.System && strings.Contains(msg.Content, "favorite_drink") {
			foundIdentity = true
			break
		}
	}
	if !foundIdentity {
		t.Fatal("expected identity overlay in system messages")
	}
	var userMessage *schema.Message
	for _, msg := range messages {
		if msg.Role == schema.User {
			userMessage = msg
			break
		}
	}
	if userMessage == nil || !strings.Contains(userMessage.Content, "Sent at: 2026-06-13 10:30:00 +08:00") {
		t.Fatalf("expected timestamped user content, got %#v", userMessage)
	}
	last := messages[len(messages)-1]
	if last.Role != schema.System || !strings.Contains(last.Content, "Current date and time:") {
		t.Fatalf("expected final current time system message, got %#v", last)
	}
}

func TestBuildModelMessagesCompactsOlderThreadContext(t *testing.T) {
	db := openTestDB(t)
	registry, err := agent.NewRegistry([]config.AgentConfig{{
		ID:           "michan",
		Name:         "Michan",
		Model:        "openai/test",
		Enabled:      true,
		SystemPrompt: "You are Michan.",
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	svc := NewConversationService(db, &config.Config{
		Personality: config.PersonalityConfig{MaxHistoryMessages: 3},
	}, registry, nil)

	thread := &database.ConversationThread{
		ID:        "thread-compact-1",
		AccountID: "acct-1",
		AgentID:   "michan",
		Title:     "Long chat",
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	baseTime := time.Date(2026, 6, 13, 9, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	for i := 0; i < 6; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		at := baseTime.Add(time.Duration(i) * time.Minute)
		if err := db.Create(&database.ConversationMessage{
			ID:        fmt.Sprintf("msg-%d", i+1),
			ThreadID:  thread.ID,
			AccountID: thread.AccountID,
			Role:      role,
			Content:   fmt.Sprintf("message %d", i+1),
			Sequence:  int64(i + 1),
			CreatedAt: at,
			UpdatedAt: at,
		}).Error; err != nil {
			t.Fatalf("create message %d: %v", i+1, err)
		}
	}

	messages, _, err := svc.BuildModelMessages(context.Background(), thread.AccountID, thread.ID, 0)
	if err != nil {
		t.Fatalf("BuildModelMessages() error = %v", err)
	}
	var refreshed database.ConversationThread
	if err := db.Where("id = ?", thread.ID).First(&refreshed).Error; err != nil {
		t.Fatalf("reload thread: %v", err)
	}
	if refreshed.SummarySeq == 0 || strings.TrimSpace(refreshed.ContextSummary) == "" {
		t.Fatalf("expected compacted thread summary, got seq=%d summary=%q", refreshed.SummarySeq, refreshed.ContextSummary)
	}
	foundSummary := false
	for _, msg := range messages {
		if msg.Role == schema.System && strings.Contains(msg.Content, "Earlier compacted thread context:") {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Fatal("expected compacted thread context system message")
	}
}

func TestBuildSolarSystemOverlayMakesGroupMultiUserContextExplicit(t *testing.T) {
	db := openTestDB(t)
	svc := &ConversationService{db: db}
	thread := &database.ConversationThread{
		ID:        "thread-group-1",
		AccountID: "solar:michan:room-1",
		AgentID:   "michan",
		Title:     "Group room",
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := db.Create(&database.ExternalChatBinding{
		ID:             "binding-group-1",
		AgentID:        "michan",
		RemoteRoomID:   "room-1",
		RemoteRoomType: roomTypePtr(0),
		ThreadID:       thread.ID,
		AccountID:      thread.AccountID,
		RemoteAccount:  "alice",
	}).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}

	overlay, err := svc.buildSnSystemOverlay(context.Background(), "michan", thread.ID, nil)
	if err != nil {
		t.Fatalf("buildSnSystemOverlay() error = %v", err)
	}
	if !strings.Contains(overlay, "This is not a DM") {
		t.Fatalf("expected non-DM hint, got %q", overlay)
	}
	if !strings.Contains(overlay, "Multiple different users may be speaking") {
		t.Fatalf("expected explicit multi-user hint, got %q", overlay)
	}
	if !strings.Contains(overlay, "pay extra attention to which participant sent each message") {
		t.Fatalf("expected participant-tracking hint, got %q", overlay)
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

	messages, _, err := svc.BuildModelMessages(context.Background(), thread.AccountID, thread.ID, 0)
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

	messages, _, err := svc.BuildModelMessages(context.Background(), thread.AccountID, thread.ID, 0)
	if err != nil {
		t.Fatalf("BuildModelMessages() error = %v", err)
	}
	for _, msg := range messages {
		if msg.Role == schema.Tool {
			t.Fatalf("expected orphaned tool message to be skipped")
		}
	}
}

func TestBuildModelMessagesDropsAssistantToolCallsWithoutToolResponses(t *testing.T) {
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
		ID:        "thread-orphan-assistant-call-1",
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
			ID:   "call-missing",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      sendChatToolName,
				Arguments: `{"room_id":"room-1","message":"hi"}`,
			},
		}},
	}); err != nil {
		t.Fatalf("create assistant tool message: %v", err)
	}

	messages, _, err := svc.BuildModelMessages(context.Background(), thread.AccountID, thread.ID, 0)
	if err != nil {
		t.Fatalf("BuildModelMessages() error = %v", err)
	}
	for _, msg := range messages {
		if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			t.Fatalf("expected orphaned assistant tool calls to be dropped, got %#v", msg.ToolCalls)
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

	messages, _, err := svc.BuildModelMessages(context.Background(), thread.AccountID, thread.ID, 0)
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
	if bindings[0].EngagementState != snRoomEngagementStateActive {
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

	allow, err := svc.allowSnRoomReply(context.Background(), thread, &database.ExternalChatBinding{
		ThreadID:       thread.ID,
		AccountID:      thread.AccountID,
		RemoteRoomType: roomTypePtr(0),
	})
	if err != nil {
		t.Fatalf("allowSnRoomReply() error = %v", err)
	}
	if allow != snReplySuppress {
		t.Fatal("expected group reply to be suppressed without mention")
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

	allow, err := svc.allowSnRoomReply(context.Background(), thread, &database.ExternalChatBinding{
		ThreadID:       thread.ID,
		AccountID:      thread.AccountID,
		RemoteRoomType: roomTypePtr(0),
	})
	if err != nil {
		t.Fatalf("allowSnRoomReply() error = %v", err)
	}
	if allow != snReplyForceAllow {
		t.Fatal("expected group reply to be force-allowed after mention")
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
	allow, err := svc.allowSnRoomReply(context.Background(), thread, &database.ExternalChatBinding{
		ThreadID:        thread.ID,
		AccountID:       thread.AccountID,
		RemoteRoomType:  roomTypePtr(0),
		EngagementState: snRoomEngagementStateActive,
		EngagedUntil:    &until,
	})
	if err != nil {
		t.Fatalf("allowSnRoomReply() error = %v", err)
	}
	if allow != snReplyAllow {
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
	allow, err := svc.allowSnRoomReply(context.Background(), thread, &database.ExternalChatBinding{
		ThreadID:        thread.ID,
		AccountID:       thread.AccountID,
		RemoteRoomType:  roomTypePtr(0),
		EngagementState: snRoomEngagementStateActive,
		EngagedUntil:    &until,
	})
	if err != nil {
		t.Fatalf("allowSnRoomReply() error = %v", err)
	}
	if allow != snReplySuppress {
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
		EngagementState: snRoomEngagementStateActive,
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
	if binding.EngagementState != snRoomEngagementStateActive {
		t.Fatalf("expected active engagement state, got %q", binding.EngagementState)
	}
}

func TestSolarInboundBatcherCoalescesMessages(t *testing.T) {
	var flushed [][]ExternalInboundMessage
	batcher := newSnInboundBatcher(20*time.Millisecond, func(_ context.Context, _ string, items []ExternalInboundMessage) error {
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

func boolPtr(v bool) *bool {
	return &v
}
