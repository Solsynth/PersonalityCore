package humanize

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/persona/internal/agent"
	"src.solsynth.dev/sosys/persona/internal/database"
)

func TestBuildPromptStateReusesImpressionAcrossSolarRoomsAndDirectRuns(t *testing.T) {
	db := openHumanizeTestDB(t)
	manager := NewManager(db)
	def := agent.Definition{
		ID:        "support-bot",
		Abilities: []string{"cross_conversation_memory", "relationship"},
	}

	now := time.Now().UTC()
	directThread := &database.ConversationThread{
		ID:        "thread-direct",
		AccountID: "acct-user-1",
		AgentID:   def.ID,
		Title:     "Direct support",
		UpdatedAt: now.Add(-time.Hour),
	}
	solarThread := &database.ConversationThread{
		ID:        "thread-solar",
		AccountID: "solar:support-bot:room-1",
		AgentID:   def.ID,
		Title:     "Room one",
		UpdatedAt: now,
	}
	currentThread := &database.ConversationThread{
		ID:        "thread-current",
		AccountID: "solar:support-bot:room-2",
		AgentID:   def.ID,
		Title:     "Room two",
		UpdatedAt: now.Add(time.Minute),
	}
	for _, thread := range []*database.ConversationThread{directThread, solarThread, currentThread} {
		if err := db.Create(thread).Error; err != nil {
			t.Fatalf("create thread %s: %v", thread.ID, err)
		}
	}

	if err := db.Create(&database.ExternalChatBinding{
		ID:              "binding-1",
		AgentID:         def.ID,
		RemoteRoomID:    "room-1",
		ThreadID:        solarThread.ID,
		AccountID:       solarThread.AccountID,
		RemoteAccountID: "acct-user-1",
		RemoteAccount:   "alice",
		LastMessageAt:   ptrTime(now),
	}).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}

	if err := db.Create(&database.ConversationMessage{
		ID:        "msg-direct-1",
		ThreadID:  directThread.ID,
		AccountID: directThread.AccountID,
		Role:      "user",
		Content:   "I need help with billing.",
		Sequence:  1,
	}).Error; err != nil {
		t.Fatalf("create direct message: %v", err)
	}
	if err := db.Create(&database.ConversationMessage{
		ID:        "msg-solar-1",
		ThreadID:  solarThread.ID,
		AccountID: solarThread.AccountID,
		Role:      "user",
		Content:   "Can you check my last order?",
		Sequence:  1,
	}).Error; err != nil {
		t.Fatalf("create solar message: %v", err)
	}

	state, err := manager.BuildPromptState(context.Background(), "acct-user-1", currentThread.AccountID, currentThread.ID, def)
	if err != nil {
		t.Fatalf("BuildPromptState() error = %v", err)
	}
	if state == nil {
		t.Fatal("expected prompt state")
	}
	if !strings.Contains(state.CrossConversation, "Direct support") {
		t.Fatalf("expected direct thread in cross conversation summary, got %q", state.CrossConversation)
	}
	if !strings.Contains(state.CrossConversation, "Room one") {
		t.Fatalf("expected solar thread in cross conversation summary, got %q", state.CrossConversation)
	}
}

func TestAgentSelfNotesAreGlobalPerAgentAndRenderedIntoOverlay(t *testing.T) {
	db := openHumanizeTestDB(t)
	manager := NewManager(db)
	ctx := context.Background()

	if _, err := manager.SaveSelfNote(ctx, "michan", MemoryInput{
		Key:      "favorite_drink",
		Category: "preference",
		Content:  "I like hojicha lattes.",
	}); err != nil {
		t.Fatalf("SaveSelfNote() error = %v", err)
	}
	// A note is identified by its key, so saving it again updates it instead
	// of leaving two notes behind.
	if _, err := manager.SaveSelfNote(ctx, "michan", MemoryInput{
		Key:      "favorite_drink",
		Category: "preference",
		Content:  "I like hojicha lattes, hot only.",
	}); err != nil {
		t.Fatalf("SaveSelfNote() second error = %v", err)
	}
	if _, err := manager.SaveSelfNote(ctx, "michan", MemoryInput{
		Key:      "current_project",
		Category: "project",
		Content:  "I'm tinkering with a room mood tracker.",
	}); err != nil {
		t.Fatalf("SaveSelfNote() third error = %v", err)
	}

	notes, err := manager.ListSelfNotes(ctx, "michan", "", 0)
	if err != nil {
		t.Fatalf("ListSelfNotes() error = %v", err)
	}
	if len(notes) != 2 {
		t.Fatalf("expected 2 self notes, got %d", len(notes))
	}
	for _, note := range notes {
		if note.Scope != MemoryScopeAgent || note.AccountID != "" {
			t.Fatalf("self note is not agent-global: %#v", note)
		}
	}

	// Notes belong to the agent rather than to the person talking, so they
	// must not surface as that account's own memories.
	userMemories, err := manager.ListMemories(ctx, "acct-1", "michan", "", 10)
	if err != nil {
		t.Fatalf("ListMemories() error = %v", err)
	}
	if len(userMemories) != 0 {
		t.Fatalf("agent notes leaked into user memories: %#v", userMemories)
	}

	overlay, err := manager.BuildAgentIdentityOverlay(ctx, "michan")
	if err != nil {
		t.Fatalf("BuildAgentIdentityOverlay() error = %v", err)
	}
	if !strings.Contains(overlay, "favorite_drink") {
		t.Fatalf("expected favorite_drink in overlay, got %q", overlay)
	}
	if !strings.Contains(overlay, "room mood tracker") {
		t.Fatalf("expected project note in overlay, got %q", overlay)
	}
}

func TestRenderSystemOverlayAlwaysCarriesNaturalLanguageConstraints(t *testing.T) {
	humanized := agent.Definition{ID: "michan", Abilities: []string{"humanizer"}}

	empty := RenderSystemOverlay(humanized, &PromptState{})
	if !strings.Contains(empty, "## Natural language") {
		t.Fatalf("empty humanize state lost natural language section: %q", empty)
	}
	if strings.Contains(empty, "Internal persona state:") {
		t.Fatalf("empty humanize state rendered a persona-state header: %q", empty)
	}

	populated := RenderSystemOverlay(humanized, &PromptState{
		MemorySummary:       "name: Jamie",
		RelationshipSummary: "new acquaintance",
		CurrentMood:         "neutral",
	})
	if !strings.Contains(populated, "Internal persona state:") {
		t.Fatalf("populated humanize state lost persona state: %q", populated)
	}
	if !strings.Contains(populated, "## Natural language") {
		t.Fatalf("populated humanize state lost natural language section: %q", populated)
	}

	plain := agent.Definition{ID: "worker", Abilities: []string{"chat"}}
	if overlay := RenderSystemOverlay(plain, &PromptState{MemorySummary: "name: Jamie"}); overlay != "" {
		t.Fatalf("non-humanize agent got an overlay: %q", overlay)
	}
	if overlay := RenderSystemOverlay(humanized, nil); overlay != "" {
		t.Fatalf("nil state produced an overlay: %q", overlay)
	}
}

func openHumanizeTestDB(t *testing.T) *database.DB {
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

func ptrTime(v time.Time) *time.Time {
	return &v
}
