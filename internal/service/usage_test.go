package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"gorm.io/datatypes"

	"src.solsynth.dev/sosys/persona/internal/agent"
	"src.solsynth.dev/sosys/persona/internal/config"
	"src.solsynth.dev/sosys/persona/internal/database"
)

func TestRunUsageSumsRoundsAndTracksPeakPrompt(t *testing.T) {
	usage := &runUsage{}
	usage.add(&schema.TokenUsage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110})
	usage.add(nil)
	usage.add(&schema.TokenUsage{PromptTokens: 250, CompletionTokens: 20, TotalTokens: 270})

	if usage.Rounds != 2 {
		t.Fatalf("rounds = %d, want 2", usage.Rounds)
	}
	if usage.InputTokens != 350 || usage.OutputTokens != 30 || usage.TotalTokens != 380 {
		t.Fatalf("summed usage = %d/%d/%d, want 350/30/380", usage.InputTokens, usage.OutputTokens, usage.TotalTokens)
	}
	if usage.PeakInputTokens != 250 {
		t.Fatalf("peak prompt = %d, want 250", usage.PeakInputTokens)
	}

	summed := usage.tokenUsage()
	if summed == nil || summed.PromptTokens != 350 || summed.CompletionTokens != 30 {
		t.Fatalf("tokenUsage() = %#v", summed)
	}
	if empty := (&runUsage{}).tokenUsage(); empty != nil {
		t.Fatalf("an empty accumulator must yield nil, got %#v", empty)
	}
}

// TestStreamRunRecordsSummedUsageAcrossToolRounds covers the reason usage is
// accumulated rather than taken from the last call: the provider reports only
// one round's tokens per response, so a two-round tool run must report their
// sum on the run, and the peak prompt must describe the fullest context.
func TestStreamRunRecordsSummedUsageAcrossToolRounds(t *testing.T) {
	requestCount := 0
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected completion path %q", r.URL.Path)
		}
		requestCount++
		flush := func(payload string) {
			if _, err := w.Write([]byte(payload)); err != nil {
				t.Fatalf("write sse: %v", err)
			}
			w.(http.Flusher).Flush()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requestCount == 1 {
			flush(sseStreamData(map[string]any{
				"choices": []any{map[string]any{
					"index": 0,
					"delta": map[string]any{
						"role": "assistant",
						"tool_calls": []any{map[string]any{
							"index": 0, "id": "call-1", "type": "function",
							"function": map[string]any{"name": memorySearchToolName, "arguments": `{"query":"tea"}`},
						}},
					},
					"finish_reason": nil,
				}},
			}))
			flush(sseStreamData(map[string]any{
				"choices": []any{map[string]any{
					"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls",
				}},
				"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 7, "total_tokens": 12},
			}))
			return
		}
		flush(sseStreamData(map[string]any{
			"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"role": "assistant", "content": "Found it."}, "finish_reason": nil,
			}},
		}))
		flush(sseStreamData(map[string]any{
			"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{}, "finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1000, "completion_tokens": 500, "total_tokens": 1500},
		}))
	}))
	defer modelServer.Close()

	cfg := &config.Config{
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Type:    "openai-compatible",
			APIKey:  "test",
			BaseURL: modelServer.URL + "/v1",
			Timeout: time.Second,
			Models:  []config.ModelConfig{{Name: "model", ContextWindow: 4096}},
		}},
	}
	registry, err := agent.NewRegistry([]config.AgentConfig{{
		ID:        "michan",
		Name:      "Michan",
		Model:     "openai/model",
		Abilities: []string{"chat", "memory"},
		Enabled:   true,
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	executor, err := agent.NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	db := openTestDB(t)
	svc := NewConversationService(db, cfg, registry, executor)
	svc.sn = &stubSolarBridge{}

	thread := &database.ConversationThread{
		ID:        "thread-usage-1",
		AccountID: "acct-1",
		AgentID:   "michan",
		Title:     "Usage chat",
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}

	result, err := svc.StreamRun(context.Background(), "acct-1", thread.ID, RunInput{Message: "check my notes"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("StreamRun() error = %v", err)
	}

	payload, ok := decodeRunUsage(result.Run.Usage)
	if !ok {
		t.Fatalf("run has no usage recorded: %s", string(result.Run.Usage))
	}
	if payload.Rounds != 2 {
		t.Fatalf("rounds = %d, want 2 (one per model call)", payload.Rounds)
	}
	if payload.InputTokens != 1005 || payload.OutputTokens != 507 || payload.TotalTokens != 1512 {
		t.Fatalf("usage = %d/%d/%d, want 1005/507/1512", payload.InputTokens, payload.OutputTokens, payload.TotalTokens)
	}
	if payload.Context == nil || payload.Context.UsedTokens != 1000 {
		t.Fatalf("context usage = %#v, want the peak prompt of 1000", payload.Context)
	}
	if payload.Context.WindowTokens != 4096 {
		t.Fatalf("context window = %d, want the configured 4096", payload.Context.WindowTokens)
	}
	if payload.Context.UsedRatio == nil || *payload.Context.UsedRatio != 0.244141 {
		t.Fatalf("context ratio = %v, want 0.244141", payload.Context.UsedRatio)
	}

	// The run is readable back through the storage the API serves, so the same
	// totals must survive a reload rather than living only in memory.
	reloaded, err := svc.GetRun(context.Background(), "acct-1", thread.ID, result.Run.ID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if string(reloaded.Usage) != string(result.Run.Usage) {
		t.Fatalf("reloaded usage = %s, want %s", string(reloaded.Usage), string(result.Run.Usage))
	}

	aggregate, err := svc.ConversationUsage(context.Background(), "acct-1", thread.ID)
	if err != nil {
		t.Fatalf("ConversationUsage() error = %v", err)
	}
	if aggregate.Runs != 1 || aggregate.InputTokens != 1005 || aggregate.OutputTokens != 507 || aggregate.TotalTokens != 1512 {
		t.Fatalf("aggregate = %#v", aggregate)
	}
	if aggregate.PeakContextUsedTokens != 1000 || aggregate.ContextWindowTokens != 4096 {
		t.Fatalf("aggregate context = %d/%d, want 1000/4096", aggregate.PeakContextUsedTokens, aggregate.ContextWindowTokens)
	}
}

func TestConversationUsageAggregatesStoredRunsAndIsAccountScoped(t *testing.T) {
	svc := newTestConversationService(t)
	ctx := context.Background()

	thread := &database.ConversationThread{ID: "thread-aggregate", AccountID: "acct-1", AgentID: "mochi"}
	if err := svc.db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}

	runs := []database.ConversationRun{
		{
			ID: "run-a", ThreadID: thread.ID, AccountID: "acct-1", AgentID: "mochi", Status: "completed",
			Usage: datatypes.JSON([]byte(`{"input_tokens":100,"output_tokens":10,"total_tokens":110,"rounds":1,"context":{"used_tokens":100,"window_tokens":4096}}`)),
		},
		{
			ID: "run-b", ThreadID: thread.ID, AccountID: "acct-1", AgentID: "mochi", Status: "completed",
			Usage: datatypes.JSON([]byte(`{"input_tokens":200,"output_tokens":20,"total_tokens":220,"rounds":3,"context":{"used_tokens":150,"window_tokens":8192}}`)),
		},
		{
			// A run created before this feature, or one whose provider reported
			// nothing, stores "{}" and must contribute nothing but a run count.
			ID: "run-c", ThreadID: thread.ID, AccountID: "acct-1", AgentID: "mochi", Status: "completed",
			Usage: datatypes.JSON([]byte(`{}`)),
		},
		{
			// Another account's run on the same thread id must never leak into
			// the aggregate even if a row were mis-scoped.
			ID: "run-other", ThreadID: thread.ID, AccountID: "acct-2", AgentID: "mochi", Status: "completed",
			Usage: datatypes.JSON([]byte(`{"input_tokens":9999,"output_tokens":9999,"total_tokens":19998,"rounds":1}`)),
		},
	}
	for i := range runs {
		if err := svc.db.Create(&runs[i]).Error; err != nil {
			t.Fatalf("create run: %v", err)
		}
	}

	aggregate, err := svc.ConversationUsage(ctx, "acct-1", thread.ID)
	if err != nil {
		t.Fatalf("ConversationUsage() error = %v", err)
	}
	if aggregate.Runs != 2 || aggregate.InputTokens != 300 || aggregate.OutputTokens != 30 || aggregate.TotalTokens != 330 {
		t.Fatalf("aggregate = %#v, want 2 runs and 300/30/330", aggregate)
	}
	if aggregate.PeakContextUsedTokens != 150 || aggregate.ContextWindowTokens != 8192 {
		t.Fatalf("aggregate context = %d/%d, want 150/8192", aggregate.PeakContextUsedTokens, aggregate.ContextWindowTokens)
	}
}

// TestDecodeRunUsageRejectsEmptyAndMalformed pins the contract that an absent
// payload is "no usage" rather than a zero-valued run that inflates the count.
func TestDecodeRunUsageRejectsEmptyAndMalformed(t *testing.T) {
	for _, raw := range []string{"", "{}", "not json"} {
		if _, ok := decodeRunUsage(datatypes.JSON([]byte(raw))); ok {
			t.Fatalf("decodeRunUsage(%q) reported usage", raw)
		}
	}
	payload, ok := decodeRunUsage(datatypes.JSON([]byte(`{"input_tokens":1}`)))
	if !ok || payload.InputTokens != 1 {
		t.Fatalf("decodeRunUsage kept %#v (ok=%v)", payload, ok)
	}

	// The stored document must round-trip through encoding/json the way a
	// client sees it, including an omitted window.
	var wire map[string]any
	if err := json.Unmarshal([]byte(`{"input_tokens":1,"output_tokens":2,"total_tokens":3,"rounds":1,"context":{"used_tokens":1}}`), &wire); err != nil {
		t.Fatalf("unmarshal wire usage: %v", err)
	}
	context, ok := wire["context"].(map[string]any)
	if !ok {
		t.Fatalf("context = %#v", wire["context"])
	}
	if _, present := context["window_tokens"]; present {
		t.Fatalf("an unknown window must be omitted, got %#v", context)
	}
}
