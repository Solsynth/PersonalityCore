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

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/database"
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
