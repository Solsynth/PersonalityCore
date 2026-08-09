package humanize

import (
	"context"
	"strings"
	"testing"

	"src.solsynth.dev/sosys/personality/internal/agent"
)

func TestStructuredMemorySupersedesChangedFacts(t *testing.T) {
	manager := NewManager(openHumanizeTestDB(t))
	ctx := context.Background()

	first, err := manager.SaveMemory(ctx, "acct-1", "michan", MemoryInput{
		Category:  "preference",
		Key:       "favorite_drink",
		Content:   "The user prefers tea.",
		Confirmed: true,
	})
	if err != nil {
		t.Fatalf("SaveMemory() first error = %v", err)
	}
	second, err := manager.SaveMemory(ctx, "acct-1", "michan", MemoryInput{
		Category:  "preference",
		Key:       "favorite_drink",
		Content:   "The user prefers coffee.",
		Confirmed: true,
	})
	if err != nil {
		t.Fatalf("SaveMemory() replacement error = %v", err)
	}
	if second.SupersedesID != first.ID {
		t.Fatalf("expected replacement to supersede %q, got %q", first.ID, second.SupersedesID)
	}

	items, err := manager.ListMemories(ctx, "acct-1", "michan", "drink", 10)
	if err != nil {
		t.Fatalf("ListMemories() error = %v", err)
	}
	if len(items) != 1 || items[0].Content != "The user prefers coffee." {
		t.Fatalf("unexpected active memories: %#v", items)
	}
	if err := manager.ForgetMemory(ctx, "acct-1", "michan", second.ID); err != nil {
		t.Fatalf("ForgetMemory() error = %v", err)
	}
	items, err = manager.ListMemories(ctx, "acct-1", "michan", "", 10)
	if err != nil {
		t.Fatalf("ListMemories() after forget error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no active memories after forget, got %#v", items)
	}
}

func TestObserveInteractionWritesStructuredFactsAndPromptUsesThem(t *testing.T) {
	manager := NewManager(openHumanizeTestDB(t))
	def := agent.Definition{ID: "michan", Abilities: []string{"memory"}}
	ctx := context.Background()

	if err := manager.ObserveInteraction(ctx, "acct-1", def, "My name is Jamie. I live in Taipei.", "Nice to meet you.", "msg-1", "run-1"); err != nil {
		t.Fatalf("ObserveInteraction() error = %v", err)
	}
	memories, err := manager.ListMemories(ctx, "acct-1", "michan", "", 10)
	if err != nil {
		t.Fatalf("ListMemories() error = %v", err)
	}
	if len(memories) != 2 || memories[0].SourceMessageID != "msg-1" || memories[0].SourceRunID != "run-1" {
		t.Fatalf("memory provenance missing: %#v", memories)
	}
	state, err := manager.BuildPromptState(ctx, "acct-1", "acct-1", "thread-1", def)
	if err != nil {
		t.Fatalf("BuildPromptState() error = %v", err)
	}
	if !strings.Contains(state.MemorySummary, "Jamie") || !strings.Contains(state.MemorySummary, "Taipei") {
		t.Fatalf("structured memories missing from prompt state: %q", state.MemorySummary)
	}
}
