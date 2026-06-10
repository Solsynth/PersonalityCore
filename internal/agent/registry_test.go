package agent

import (
	"testing"

	"src.solsynth.dev/sosys/personality/internal/config"
)

func TestNewRegistry_RejectsDuplicateIDs(t *testing.T) {
	_, err := NewRegistry([]config.AgentConfig{
		{ID: "a", Name: "A", Enabled: true},
		{ID: "a", Name: "B", Enabled: true},
	})
	if err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestRegistry_ListOnlyEnabled(t *testing.T) {
	registry, err := NewRegistry([]config.AgentConfig{
		{ID: "a", Name: "A", Enabled: true},
		{ID: "b", Name: "B", Enabled: false},
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	if got := len(registry.List()); got != 1 {
		t.Fatalf("expected 1 enabled agent, got %d", got)
	}
}
