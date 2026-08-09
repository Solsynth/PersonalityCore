package service

import (
	"context"
	"testing"

	"github.com/oklog/ulid/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
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
	return &ConversationService{db: db, cfg: &config.Config{}, registry: registry}
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

func TestGetOrCreatePetThreadRejectsNonPetAgent(t *testing.T) {
	svc := newPetTestService(t)
	if _, err := svc.GetOrCreatePetThread(context.Background(), "acct-1", "general"); err == nil {
		t.Fatal("expected non-pet agent to be rejected")
	}
}
