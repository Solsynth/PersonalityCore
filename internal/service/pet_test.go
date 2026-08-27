package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/oklog/ulid/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
	"src.solsynth.dev/sosys/personality/internal/humanize"
)

func newPetTestService(t *testing.T) *ConversationService {
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
		{ID: "mochi", Name: "Mochi", Model: "test", Abilities: []string{"pet", "memory", "mood", "relationship"}, Enabled: true},
		{ID: "general", Name: "General", Model: "test", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &ConversationService{db: db, cfg: &config.Config{}, registry: registry, humanize: humanize.NewManager(db)}
}

func TestGetOrCreatePetThreadIsCanonicalAndHidden(t *testing.T) {
	svc := newPetTestService(t)
	ctx := context.Background()

	first, err := svc.GetOrCreatePetThread(ctx, "acct-1", "mochi")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.GetOrCreatePetThread(ctx, "acct-1", "mochi")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("pet thread changed across calls: %q != %q", first.ID, second.ID)
	}
	if first.Kind != petThreadKind {
		t.Fatalf("thread kind = %q, want %q", first.Kind, petThreadKind)
	}

	items, total, err := svc.ListConversations(ctx, "acct-1", ListInput{Take: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("pet thread leaked into normal conversation list: total=%d items=%d", total, len(items))
	}

	if err := svc.ResetPetThread(ctx, "acct-1", "mochi"); err != nil {
		t.Fatal(err)
	}
	third, err := svc.GetOrCreatePetThread(ctx, "acct-1", "mochi")
	if err != nil {
		t.Fatal(err)
	}
	if third.ID == first.ID {
		t.Fatal("reset did not create a new pet thread")
	}
}

func TestGetPetAffectionDefaultsToFiftyBeforeAdjustment(t *testing.T) {
	svc := newPetTestService(t)
	ctx := context.Background()
	if _, err := svc.GetOrCreatePetThread(ctx, "acct-1", "mochi"); err != nil {
		t.Fatal(err)
	}
	affection, err := svc.GetPetAffection(ctx, "acct-1", "mochi")
	if err != nil {
		t.Fatal(err)
	}
	if affection.Affection != 50 || affection.Level != "familiar" {
		t.Fatalf("default affection = %d (%s), want 50 (familiar)", affection.Affection, affection.Level)
	}
}

func TestAdjustPetAffectionAppliesDeltaAndReason(t *testing.T) {
	svc := newPetTestService(t)
	ctx := context.Background()
	if _, err := svc.GetOrCreatePetThread(ctx, "acct-1", "mochi"); err != nil {
		t.Fatal(err)
	}

	after, err := svc.AdjustPetAffection(ctx, "acct-1", "mochi", 10, "The user gave me a treat.")
	if err != nil {
		t.Fatal(err)
	}
	if after.Affection != 60 || after.Level != "familiar" {
		t.Fatalf("affection = %d (%s), want 60 (familiar)", after.Affection, after.Level)
	}
	if after.Reason != "The user gave me a treat." {
		t.Fatalf("reason = %q, want %q", after.Reason, "The user gave me a treat.")
	}

	lower, err := svc.AdjustPetAffection(ctx, "acct-1", "mochi", -15, "The user ignored me.")
	if err != nil {
		t.Fatal(err)
	}
	if lower.Affection != 45 || lower.Level != "familiar" {
		t.Fatalf("affection = %d (%s), want 45 (familiar)", lower.Affection, lower.Level)
	}
	if lower.Reason != "The user ignored me." {
		t.Fatalf("reason = %q, want %q", lower.Reason, "The user ignored me.")
	}
}

func TestAdjustPetAffectionClampsToBounds(t *testing.T) {
	svc := newPetTestService(t)
	ctx := context.Background()
	if _, err := svc.GetOrCreatePetThread(ctx, "acct-1", "mochi"); err != nil {
		t.Fatal(err)
	}
	high, err := svc.AdjustPetAffection(ctx, "acct-1", "mochi", 1000, "Adored.")
	if err != nil {
		t.Fatal(err)
	}
	if high.Affection != 100 || high.Level != "devoted" {
		t.Fatalf("clamped high affection = %d (%s), want 100 (devoted)", high.Affection, high.Level)
	}
	low, err := svc.AdjustPetAffection(ctx, "acct-1", "mochi", -1000, "Betrayed.")
	if err != nil {
		t.Fatal(err)
	}
	if low.Affection != 0 || low.Level != "estranged" {
		t.Fatalf("clamped low affection = %d (%s), want 0 (estranged)", low.Affection, low.Level)
	}
}

func TestExecutePetToolCallAdjustsSessionAffection(t *testing.T) {
	svc := newPetTestService(t)
	ctx := context.Background()
	if _, err := svc.GetOrCreatePetThread(ctx, "acct-1", "mochi"); err != nil {
		t.Fatal(err)
	}
	result, err := svc.executePetToolCall(ctx, "acct-1", "mochi", schema.ToolCall{
		ID: "call-1",
		Function: schema.FunctionCall{
			Name:      petAdjustAffectionToolName,
			Arguments: `{"delta": 8, "reason": "She petted me."}`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ToolName != petAdjustAffectionToolName {
		t.Fatalf("tool name = %q, want %q", result.ToolName, petAdjustAffectionToolName)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("tool result is not JSON: %v", err)
	}
	if payload["affection"] != float64(58) || payload["level"] != "familiar" {
		t.Fatalf("tool result = %v, want affection 58 (familiar)", payload)
	}
	affection, err := svc.GetPetAffection(ctx, "acct-1", "mochi")
	if err != nil {
		t.Fatal(err)
	}
	if affection.Affection != 58 || affection.Reason != "She petted me." {
		t.Fatalf("session affection = %d (%q), want 58 (%q)", affection.Affection, affection.Reason, "She petted me.")
	}
}
func TestGetPetAffectionRequiresExistingSession(t *testing.T) {
	svc := newPetTestService(t)
	if _, err := svc.GetPetAffection(context.Background(), "acct-1", "mochi"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for session-less lookup, got %v", err)
	}
}
func TestListPetAgentsFiltersByAbility(t *testing.T) {
	svc := newPetTestService(t)
	all := svc.ListPetAgents(false)
	if len(all) != 2 {
		t.Fatalf("unfiltered list = %d agents, want 2", len(all))
	}
	pets := svc.ListPetAgents(true)
	if len(pets) != 1 || pets[0].ID != "mochi" {
		t.Fatalf("pet-only list = %+v, want only mochi", pets)
	}
	if pets[0].SystemPrompt != "" {
		t.Fatal("system prompt should be stripped from listed agents")
	}
}

func TestResetAgentMemoriesPurgesEverythingForAccount(t *testing.T) {
	svc := newPetTestService(t)
	ctx := context.Background()
	thread, err := svc.GetOrCreatePetThread(ctx, "acct-1", "mochi")
	if err != nil {
		t.Fatal(err)
	}
	run, _, _, err := svc.CreateRun(ctx, "acct-1", thread.ID, RunInput{Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AdjustPetAffection(ctx, "acct-1", "mochi", 15, "warm"); err != nil {
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

	var threadCount, messageCount, runCount, sessionCount, memoryCount, stateCount int64
	svc.db.WithContext(ctx).Model(&database.ConversationThread{}).Where("account_id = ? AND agent_id = ?", "acct-1", "mochi").Count(&threadCount)
	svc.db.WithContext(ctx).Model(&database.ConversationMessage{}).Where("thread_id = ?", thread.ID).Count(&messageCount)
	svc.db.WithContext(ctx).Model(&database.ConversationRun{}).Where("id = ?", run.ID).Count(&runCount)
	svc.db.WithContext(ctx).Model(&database.PetSession{}).Where("account_id = ? AND agent_id = ?", "acct-1", "mochi").Count(&sessionCount)
	svc.db.WithContext(ctx).Model(&database.AgentMemory{}).Where("account_id = ? AND agent_id = ?", "acct-1", "mochi").Count(&memoryCount)
	svc.db.WithContext(ctx).Model(&database.AgentHumanState{}).Where("account_id = ? AND agent_id = ?", "acct-1", "mochi").Count(&stateCount)
	if threadCount != 0 || messageCount != 0 || runCount != 0 || sessionCount != 0 || memoryCount != 0 || stateCount != 0 {
		t.Fatalf("leftover rows after reset: threads=%d messages=%d runs=%d sessions=%d memories=%d states=%d",
			threadCount, messageCount, runCount, sessionCount, memoryCount, stateCount)
	}

	if _, err := svc.GetPetAffection(ctx, "acct-1", "mochi"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after reset, got %v", err)
	}
}

func TestResetAgentMemoriesLeavesOtherAccountsAndAgentsAlone(t *testing.T) {
	svc := newPetTestService(t)
	ctx := context.Background()
	thread, err := svc.GetOrCreatePetThread(ctx, "acct-1", "mochi")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveMemory(ctx, "acct-1", "mochi", MemoryInput{
		Scope: "user", Category: "identity", Key: "name", Content: "Acct one fact.",
		Confidence: 1, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetOrCreatePetThread(ctx, "acct-2", "mochi"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveMemory(ctx, "acct-2", "mochi", MemoryInput{
		Scope: "user", Category: "identity", Key: "name", Content: "Acct two fact.",
		Confidence: 1, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.ResetAgentMemories(ctx, "acct-1", "mochi"); err != nil {
		t.Fatal(err)
	}

	var keptThreads, keptMemories int64
	svc.db.WithContext(ctx).Model(&database.ConversationThread{}).Where("id = ?", thread.ID).Count(&keptThreads)
	svc.db.WithContext(ctx).Model(&database.AgentMemory{}).Where("account_id = ? AND agent_id = ?", "acct-2", "mochi").Count(&keptMemories)
	if keptThreads != 0 {
		t.Fatal("reset account's thread still present")
	}
	otherAffection, err := svc.GetPetAffection(ctx, "acct-2", "mochi")
	if err != nil {
		t.Fatalf("other account's session was deleted: %v", err)
	}
	if otherAffection.Affection != 50 {
		t.Fatalf("other account affection = %d, want 50", otherAffection.Affection)
	}
	if keptMemories == 0 {
		t.Fatal("other account's memories were purged")
	}
}

func TestResetAgentMemoriesRejectsUnknownAgent(t *testing.T) {
	svc := newPetTestService(t)
	if err := svc.ResetAgentMemories(context.Background(), "acct-1", "ghost"); err == nil {
		t.Fatal("expected unknown agent to be rejected")
	}
}
