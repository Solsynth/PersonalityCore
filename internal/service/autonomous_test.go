package service

import (
	"context"
	"testing"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
)

func TestCreateRunWithRequestSupportsAutonomousSystemMessages(t *testing.T) {
	db := openTestDB(t)
	registry, err := agent.NewRegistry([]config.AgentConfig{{
		ID:        "auto-bot",
		Name:      "Auto Bot",
		Model:     "openai/test",
		Abilities: []string{"autonomous"},
		Enabled:   true,
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	svc := NewConversationService(db, &config.Config{}, registry, nil)
	thread, err := svc.CreateConversation(context.Background(), "acct-1", CreateConversationInput{
		AgentID: "auto-bot",
		Title:   "Autonomous thread",
	})
	if err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}

	_, run, requestMessage, err := svc.createRunWithRequest(
		context.Background(),
		thread,
		thread.AccountID,
		"system",
		"Autonomous wake-up triggered.",
		false,
		map[string]any{
			"source":  "autonomous",
			"trigger": "periodic",
		},
	)
	if err != nil {
		t.Fatalf("createRunWithRequest() error = %v", err)
	}

	if requestMessage.Role != "system" {
		t.Fatalf("expected system request message, got %q", requestMessage.Role)
	}
	if string(requestMessage.Metadata) == "" || string(requestMessage.Metadata) == "{}" {
		t.Fatalf("expected autonomous metadata, got %s", requestMessage.Metadata)
	}
	if run.RequestMessageID != requestMessage.ID {
		t.Fatalf("expected run request message id %q, got %q", requestMessage.ID, run.RequestMessageID)
	}
}

func TestResolveAutonomousThreadCreatesTargetConversation(t *testing.T) {
	db := openTestDB(t)
	registry, err := agent.NewRegistry([]config.AgentConfig{{
		ID:        "auto-bot",
		Name:      "Auto Bot",
		Model:     "openai/test",
		Abilities: []string{"autonomous", "chat"},
		Enabled:   true,
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	svc := NewConversationService(db, &config.Config{}, registry, nil)
	def, ok := registry.Get("auto-bot")
	if !ok {
		t.Fatal("expected agent definition")
	}

	thread, err := svc.resolveAutonomousThread(context.Background(), def, AutonomousRunInput{
		TargetAccountID:   "acct-alice-1",
		TargetAccountName: "alice",
	})
	if err != nil {
		t.Fatalf("resolveAutonomousThread() error = %v", err)
	}
	if thread.AccountID != "solar:auto-bot:dm:acct-alice-1" {
		t.Fatalf("unexpected account id %q", thread.AccountID)
	}
	if thread.Title != "alice" {
		t.Fatalf("unexpected title %q", thread.Title)
	}
}

func TestAllowAutonomousOldMessagePickupAllowsDM(t *testing.T) {
	db := openTestDB(t)
	svc := NewConversationService(db, &config.Config{}, nil, nil)
	thread := &database.ConversationThread{
		ID:        "thread-dm-1",
		AccountID: "solar:auto-bot:room-dm-1",
		AgentID:   "auto-bot",
		Title:     "DM",
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}

	dmRoomType := 1
	allow, err := svc.allowAutonomousOldMessagePickup(context.Background(), thread, &database.ExternalChatBinding{
		ThreadID:       thread.ID,
		AccountID:      thread.AccountID,
		RemoteRoomType: &dmRoomType,
	})
	if err != nil {
		t.Fatalf("allowAutonomousOldMessagePickup() error = %v", err)
	}
	if !allow {
		t.Fatal("expected DM pickup to be allowed")
	}
}

func TestAllowAutonomousOldMessagePickupSuppressesOldGroupMessageWithoutMention(t *testing.T) {
	db := openTestDB(t)
	svc := NewConversationService(db, &config.Config{}, nil, nil)
	thread := &database.ConversationThread{
		ID:        "thread-group-1",
		AccountID: "solar:auto-bot:room-group-1",
		AgentID:   "auto-bot",
		Title:     "Group",
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if _, err := svc.createMessageWithMetadata(context.Background(), thread, nil, "user", "old chatter", nil, map[string]any{
		"source":        "solar",
		"room_type":     0,
		"mentioned_bot": false,
	}); err != nil {
		t.Fatalf("create user message: %v", err)
	}

	groupRoomType := 0
	allow, err := svc.allowAutonomousOldMessagePickup(context.Background(), thread, &database.ExternalChatBinding{
		ThreadID:       thread.ID,
		AccountID:      thread.AccountID,
		RemoteRoomType: &groupRoomType,
	})
	if err != nil {
		t.Fatalf("allowAutonomousOldMessagePickup() error = %v", err)
	}
	if allow {
		t.Fatal("expected old group pickup to be suppressed without mention or reply")
	}
}

func TestAllowAutonomousOldMessagePickupAllowsGroupMention(t *testing.T) {
	db := openTestDB(t)
	svc := NewConversationService(db, &config.Config{}, nil, nil)
	thread := &database.ConversationThread{
		ID:        "thread-group-2",
		AccountID: "solar:auto-bot:room-group-2",
		AgentID:   "auto-bot",
		Title:     "Group",
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if _, err := svc.createMessageWithMetadata(context.Background(), thread, nil, "user", "@bot ping", nil, map[string]any{
		"source":        "solar",
		"room_type":     0,
		"mentioned_bot": true,
	}); err != nil {
		t.Fatalf("create user message: %v", err)
	}

	groupRoomType := 0
	allow, err := svc.allowAutonomousOldMessagePickup(context.Background(), thread, &database.ExternalChatBinding{
		ThreadID:       thread.ID,
		AccountID:      thread.AccountID,
		RemoteRoomType: &groupRoomType,
	})
	if err != nil {
		t.Fatalf("allowAutonomousOldMessagePickup() error = %v", err)
	}
	if !allow {
		t.Fatal("expected old group pickup to be allowed after mention")
	}
}

func TestEnsureMetadataMapInitializesNilInput(t *testing.T) {
	got := ensureMetadataMap(nil, 4)
	if got == nil {
		t.Fatal("expected metadata map to be initialized")
	}
	got["source"] = "autonomous"
	if got["source"] != "autonomous" {
		t.Fatalf("expected writable metadata map, got %#v", got)
	}
}
