package service

import (
	"context"
	"testing"

	"github.com/oklog/ulid/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/persona/internal/agent"
	"src.solsynth.dev/sosys/persona/internal/config"
	"src.solsynth.dev/sosys/persona/internal/database"
	"src.solsynth.dev/sosys/persona/internal/humanize"
)

func newAgentResetTestService(t *testing.T) *ConversationService {
	t.Helper()
	gormDB, err := gorm.Open(sqlite.Open("file:"+ulid.Make().String()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	db := &database.DB{DB: gormDB}
	if err := db.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	registry, err := agent.NewRegistry([]config.AgentConfig{
		{ID: "mochi", Name: "Mochi", Model: "test", Abilities: []string{"memory", "mood", "relationship"}, Enabled: true},
		{ID: "general", Name: "General", Model: "test", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &ConversationService{db: db, cfg: &config.Config{}, registry: registry, humanize: humanize.NewManager(db)}
}

func TestResetAgentMemoriesPurgesEverythingForAccount(t *testing.T) {
	svc := newAgentResetTestService(t)
	ctx := context.Background()
	thread, err := svc.CreateConversation(ctx, "acct-1", CreateConversationInput{AgentID: "mochi", Title: "Chat"})
	if err != nil {
		t.Fatal(err)
	}
	run, _, _, err := svc.CreateRun(ctx, "acct-1", thread.ID, RunInput{Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveMemory(ctx, "acct-1", "mochi", MemoryInput{
		Scope: "user", Category: "identity", Key: "name", Content: "Mochi knows my name.",
		Confidence: 1, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.ResetAgentMemories(ctx, "acct-1", "mochi"); err != nil {
		t.Fatal(err)
	}

	var threadCount, messageCount, runCount, memoryCount, stateCount int64
	svc.db.WithContext(ctx).Model(&database.ConversationThread{}).Where("account_id = ? AND agent_id = ?", "acct-1", "mochi").Count(&threadCount)
	svc.db.WithContext(ctx).Model(&database.ConversationMessage{}).Where("thread_id = ?", thread.ID).Count(&messageCount)
	svc.db.WithContext(ctx).Model(&database.ConversationRun{}).Where("id = ?", run.ID).Count(&runCount)
	svc.db.WithContext(ctx).Model(&database.AgentMemory{}).Where("account_id = ? AND agent_id = ?", "acct-1", "mochi").Count(&memoryCount)
	svc.db.WithContext(ctx).Model(&database.AgentHumanState{}).Where("account_id = ? AND agent_id = ?", "acct-1", "mochi").Count(&stateCount)
	if threadCount != 0 || messageCount != 0 || runCount != 0 || memoryCount != 0 || stateCount != 0 {
		t.Fatalf("leftover rows after reset: threads=%d messages=%d runs=%d memories=%d states=%d",
			threadCount, messageCount, runCount, memoryCount, stateCount)
	}

	if _, err := svc.GetConversation(ctx, "acct-1", thread.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after reset, got %v", err)
	}
}

func TestResetAgentMemoriesLeavesOtherAccountsAndAgentsAlone(t *testing.T) {
	svc := newAgentResetTestService(t)
	ctx := context.Background()
	thread, err := svc.CreateConversation(ctx, "acct-1", CreateConversationInput{AgentID: "mochi", Title: "Chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveMemory(ctx, "acct-1", "mochi", MemoryInput{
		Scope: "user", Category: "identity", Key: "name", Content: "Acct one fact.",
		Confidence: 1, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	otherAccountThread, err := svc.CreateConversation(ctx, "acct-2", CreateConversationInput{AgentID: "mochi", Title: "Other account"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveMemory(ctx, "acct-2", "mochi", MemoryInput{
		Scope: "user", Category: "identity", Key: "name", Content: "Acct two fact.",
		Confidence: 1, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	otherAgentThread, err := svc.CreateConversation(ctx, "acct-1", CreateConversationInput{AgentID: "general", Title: "Other agent"})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.ResetAgentMemories(ctx, "acct-1", "mochi"); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.GetConversation(ctx, "acct-1", thread.ID); err != ErrNotFound {
		t.Fatalf("reset account's thread still present: %v", err)
	}
	if _, err := svc.GetConversation(ctx, "acct-1", otherAgentThread.ID); err != nil {
		t.Fatalf("reset removed another agent's thread: %v", err)
	}
	if _, err := svc.GetConversation(ctx, "acct-2", otherAccountThread.ID); err != nil {
		t.Fatalf("reset removed another account's thread: %v", err)
	}
	keptMemories, err := svc.ListMemories(ctx, "acct-2", "mochi", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(keptMemories) == 0 {
		t.Fatal("other account's memories were purged")
	}
}

func TestResetAgentMemoriesRejectsUnknownAgent(t *testing.T) {
	svc := newAgentResetTestService(t)
	if err := svc.ResetAgentMemories(context.Background(), "acct-1", "ghost"); err == nil {
		t.Fatal("expected unknown agent to be rejected")
	}
}
