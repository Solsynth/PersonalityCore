package humanize

import (
	"context"
	"strings"
	"testing"

	"src.solsynth.dev/sosys/persona/internal/agent"
	"src.solsynth.dev/sosys/persona/internal/database"
)

func TestPinnedRetentionRendersOutsideLongTermMemory(t *testing.T) {
	manager := NewManager(openHumanizeTestDB(t))
	def := agent.Definition{ID: "michan", Abilities: []string{"memory"}}
	ctx := context.Background()
	if err := manager.db.WithContext(ctx).Create(&database.ConversationGroup{
		ID: "group-1", AccountID: "acct-1", Name: "Trip planning",
	}).Error; err != nil {
		t.Fatal(err)
	}

	// One fact learned in a grouped conversation, one in a plain conversation.
	if err := manager.ObserveInteraction(ctx, "acct-1", def, MemoryRetention{Pinned: true, GroupID: "group-1"}, "My name is Jamie.", "Hi Jamie.", "msg-1", "run-1"); err != nil {
		t.Fatal(err)
	}
	if err := manager.ObserveInteraction(ctx, "acct-1", def, MemoryRetention{}, "I work at Acme.", "Good to know.", "msg-2", "run-2"); err != nil {
		t.Fatal(err)
	}

	memories, err := manager.ListMemories(ctx, "acct-1", "michan", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	pinnedCount := 0
	for _, memory := range memories {
		if memory.Pinned {
			pinnedCount++
			if memory.GroupID != "group-1" || !memory.Confirmed || !strings.Contains(memory.Content, "Jamie") {
				t.Fatalf("pinned memory not written with its tier: %#v", memory)
			}
		}
	}
	if pinnedCount != 1 {
		t.Fatalf("pinned memories = %d, want 1", pinnedCount)
	}

	state, err := manager.BuildPromptState(ctx, "acct-1", "acct-1", "thread-1", def)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.PinnedMemorySummary, "[Trip planning]") || !strings.Contains(state.PinnedMemorySummary, "Jamie") {
		t.Fatalf("pinned summary missing group prefix: %q", state.PinnedMemorySummary)
	}
	if strings.Contains(state.PinnedMemorySummary, "Acme") {
		t.Fatalf("plain fact leaked into the pinned summary: %q", state.PinnedMemorySummary)
	}
	if !strings.Contains(state.MemorySummary, "Acme") {
		t.Fatalf("plain fact missing from long-term memory: %q", state.MemorySummary)
	}
	// The pinned fact must render exactly once.
	if strings.Contains(state.MemorySummary, "Jamie") {
		t.Fatalf("pinned fact also rendered as long-term memory: %q", state.MemorySummary)
	}

	overlay := RenderSystemOverlay(def, state)
	pinnedAt := strings.Index(overlay, "Pinned memories from your important conversations:")
	longTermAt := strings.Index(overlay, "Long-term memory:")
	if pinnedAt < 0 || longTermAt < 0 || pinnedAt > longTermAt {
		t.Fatalf("pinned section is not rendered before long-term memory:\n%s", overlay)
	}
}

func TestPinnedRetentionDropsPrefixWhenGroupIsGone(t *testing.T) {
	manager := NewManager(openHumanizeTestDB(t))
	def := agent.Definition{ID: "michan", Abilities: []string{"memory"}}
	ctx := context.Background()

	if err := manager.ObserveInteraction(ctx, "acct-1", def, MemoryRetention{Pinned: true, GroupID: "group-gone"}, "My name is Jamie.", "Hi.", "msg-1", "run-1"); err != nil {
		t.Fatal(err)
	}
	state, err := manager.BuildPromptState(ctx, "acct-1", "acct-1", "thread-1", def)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(state.PinnedMemorySummary, "[") {
		t.Fatalf("deleted group should not be named: %q", state.PinnedMemorySummary)
	}
	if !strings.Contains(state.PinnedMemorySummary, "Jamie") {
		t.Fatalf("pinned memory missing: %q", state.PinnedMemorySummary)
	}
}

func TestPinnedRetentionSurvivesMergeAndSupersede(t *testing.T) {
	manager := NewManager(openHumanizeTestDB(t))
	ctx := context.Background()

	first, err := manager.SaveMemory(ctx, "acct-1", "michan", MemoryInput{
		Category: "preference", Key: "drink", Content: "The user prefers tea.", Pinned: true, GroupID: "group-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Re-observing the same fact without the tier must not demote it.
	merged, err := manager.SaveMemory(ctx, "acct-1", "michan", MemoryInput{
		Category: "preference", Key: "drink", Content: "The user prefers tea.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !merged.Pinned || merged.GroupID != "group-1" {
		t.Fatalf("merge demoted retention: %#v", merged)
	}

	// Correcting the fact supersedes it; the replacement inherits the tier.
	replacement, err := manager.SaveMemory(ctx, "acct-1", "michan", MemoryInput{
		Category: "preference", Key: "drink", Content: "The user prefers coffee.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.SupersedesID != first.ID {
		t.Fatalf("replacement did not supersede %q: %q", first.ID, replacement.SupersedesID)
	}
	if !replacement.Pinned || replacement.GroupID != "group-1" {
		t.Fatalf("supersede dropped retention: %#v", replacement)
	}

	active, err := manager.listMemories(ctx, "acct-1", "michan", "", 10, new(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].Content != "The user prefers coffee." {
		t.Fatalf("pinned active memories = %#v", active)
	}
}
