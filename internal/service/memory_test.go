package service

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/humanize"
)

func TestMemoryToolsSaveAndSearchUseAccountScopedStore(t *testing.T) {
	svc := &ConversationService{humanize: humanize.NewManager(openTestDB(t))}
	ctx := context.Background()
	def := agent.Definition{ID: "michan"}

	saved, err := svc.executeMemoryToolCall(ctx, def, "acct-1", schema.ToolCall{
		ID: "call-save",
		Function: schema.FunctionCall{
			Name:      memorySaveToolName,
			Arguments: `{"category":"preference","key":"favorite_drink","content":"The user prefers tea."}`,
		},
	})
	if err != nil {
		t.Fatalf("memory_save error = %v", err)
	}
	if !strings.Contains(saved.Content, `"saved":true`) {
		t.Fatalf("unexpected save result: %s", saved.Content)
	}

	searched, err := svc.executeMemoryToolCall(ctx, def, "acct-1", schema.ToolCall{
		ID: "call-search",
		Function: schema.FunctionCall{
			Name:      memorySearchToolName,
			Arguments: `{"query":"tea"}`,
		},
	})
	if err != nil {
		t.Fatalf("memory_search error = %v", err)
	}
	if !strings.Contains(searched.Content, "favorite_drink") || strings.Contains(searched.Content, "acct-1") {
		t.Fatalf("unexpected search result: %s", searched.Content)
	}
}
