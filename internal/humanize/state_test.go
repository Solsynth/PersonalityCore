package humanize

import (
	"strings"
	"testing"
	"time"
)

func TestExtractMemoryFacts(t *testing.T) {
	now := time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)
	facts := ExtractMemoryFacts("My name is Jamie. I live in Taipei. I like tea.", now)

	if len(facts) != 3 {
		t.Fatalf("expected 3 facts, got %d", len(facts))
	}
	if facts[0].Category != "name" {
		t.Fatalf("first category = %q", facts[0].Category)
	}
	if !strings.Contains(facts[1].Content, "Taipei") {
		t.Fatalf("location fact = %q", facts[1].Content)
	}
}

func TestMergeMemoryFactsReplacesStrongCategories(t *testing.T) {
	now := time.Now()
	existing := []MemoryFact{{Category: "name", Content: "The user's name is Alex.", UpdatedAt: now.Add(-time.Hour)}}
	incoming := []MemoryFact{{Category: "name", Content: "The user's name is Jamie.", UpdatedAt: now}}

	merged := MergeMemoryFacts(existing, incoming)
	if len(merged) != 1 {
		t.Fatalf("merged len = %d", len(merged))
	}
	if !strings.Contains(merged[0].Content, "Jamie") {
		t.Fatalf("merged content = %q", merged[0].Content)
	}
}

func TestDeriveMood(t *testing.T) {
	mood, reason := DeriveMood("neutral", "I am really anxious about tomorrow", "")
	if mood != "gentle and protective" {
		t.Fatalf("mood = %q", mood)
	}
	if reason == "" {
		t.Fatal("expected reason")
	}
}

func TestExtractSavedMemoryCandidates(t *testing.T) {
	candidates := ExtractSavedMemoryCandidates("Please remember my birthday is May 5. Don't forget I prefer tea over coffee.")
	if len(candidates) != 2 {
		t.Fatalf("candidate len = %d", len(candidates))
	}
	if candidates[0].Category == "" || candidates[0].Content == "" {
		t.Fatalf("first candidate = %#v", candidates[0])
	}
}
