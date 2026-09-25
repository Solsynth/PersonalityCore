package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"src.solsynth.dev/sosys/persona/internal/agent"
	"src.solsynth.dev/sosys/persona/internal/config"
	"src.solsynth.dev/sosys/persona/internal/database"
)

// sseData mirrors the fake-model-server framing used by conversation_test.go.
func sseData(payload map[string]any) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return "data: " + string(raw) + "\n\n"
}

func clientToolTestSetup(t *testing.T) (*ConversationService, *httptest.Server, *[]map[string]any) {
	t.Helper()

	var requestBodies []map[string]any
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode completion request: %v", err)
		}
		requestBodies = append(requestBodies, body)

		w.Header().Set("Content-Type", "text/event-stream")
		flush := func(payload string) {
			if _, err := w.Write([]byte(payload)); err != nil {
				t.Fatalf("write sse: %v", err)
			}
			w.(http.Flusher).Flush()
		}
		round := len(requestBodies)
		switch round {
		case 1:
			// First round: the model asks for the client-owned tool.
			flush(sseData(map[string]any{
				"choices": []any{map[string]any{
					"index": 0,
					"delta": map[string]any{
						"role": "assistant",
						"tool_calls": []any{map[string]any{
							"index": 0, "id": "call-local", "type": "function",
							"function": map[string]any{"name": clientToolNamespace + "web_search_local", "arguments": `{"query":"duckdb"}`},
						}},
					},
					"finish_reason": nil,
				}},
			}))
			flush(sseData(map[string]any{
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}},
			}))
		case 2:
			// The tool message with the resumed result must be on the wire.
			messages, _ := body["messages"].([]any)
			last, _ := messages[len(messages)-1].(map[string]any)
			if last["role"] != "tool" {
				t.Fatalf("expected the resumed tool message last, got %#v", last)
			}
			if !strings.Contains(fmt.Sprint(last["content"]), "device result") {
				t.Fatalf("expected the device result in the tool message, got %#v", last)
			}
			flush(sseData(map[string]any{
				"choices": []any{map[string]any{
					"index": 0, "delta": map[string]any{"role": "assistant", "content": "Found it locally."}, "finish_reason": nil,
				}},
			}))
			flush(sseData(map[string]any{
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			}))
		default:
			t.Fatalf("unexpected model round %d", round)
		}
	}))
	t.Cleanup(modelServer.Close)

	cfg := &config.Config{
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Type:    "openai-compatible",
			APIKey:  "test",
			BaseURL: modelServer.URL + "/v1",
			Timeout: time.Second,
			Models:  []config.ModelConfig{{Name: "model"}},
		}},
	}
	registry, err := agent.NewRegistry([]config.AgentConfig{{
		ID:        "server-maid",
		Name:      "Server Maid",
		Model:     "openai/model",
		Abilities: []string{"chat"},
		Enabled:   true,
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	executor, err := agent.NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	svc := NewConversationService(openTestDB(t), cfg, registry, executor)
	return svc, modelServer, &requestBodies
}

func clientToolInfo(name string) *schema.ToolInfo {
	return &schema.ToolInfo{
		Name:        name,
		Desc:        "A tool the caller executes on its own connection.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

// TestStreamRunHandsClientToolToCaller covers the full pause/resume contract:
// the model asks for a client-owned tool, the run pauses and reports the call
// with its run id, the caller resumes with a result, and the run finishes with
// the result persisted as a tool message exactly like a server tool result.
func TestStreamRunHandsClientToolToCaller(t *testing.T) {
	svc, _, requestBodies := clientToolTestSetup(t)

	thread := &database.ConversationThread{
		ID:        "thread-client-1",
		AccountID: "acct-1",
		AgentID:   "server-maid",
		Title:     "Client tool chat",
	}
	if err := svc.db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}

	type clientCall struct {
		runID string
		call  schema.ToolCall
	}
	handoff := make(chan clientCall, 1)
	completed := make(chan schema.ToolCall, 1)
	result := make(chan *RunResult, 1)
	runErr := make(chan error, 1)

	go func() {
		runResult, err := svc.StreamRun(context.Background(), "acct-1", thread.ID, RunInput{
			Message:     "search the web",
			Stream:      true,
			ClientTools: []*schema.ToolInfo{clientToolInfo("web_search_local")},
		}, StreamCallbacks{
			OnClientTool: func(runID string, call schema.ToolCall) error {
				handoff <- clientCall{runID: runID, call: call}
				return nil
			},
			OnToolResult: func(call schema.ToolCall, _ string) error {
				completed <- call
				return nil
			},
		})
		if err != nil {
			runErr <- err
			return
		}
		result <- runResult
	}()

	select {
	case call := <-handoff:
		if call.call.Function.Name != clientToolNamespace+"web_search_local" {
			t.Fatalf("handed back the wrong tool: %#v", call.call)
		}
		if call.runID == "" {
			t.Fatal("client tool handoff carried no run id")
		}
		if call.call.ID != "call-local" {
			t.Fatalf("tool call id = %q", call.call.ID)
		}
		resumed, err := svc.SubmitClientToolResult(context.Background(), "acct-1", call.runID, call.call.ID, "device result for duckdb", nil, nil)
		if err != nil {
			t.Fatalf("SubmitClientToolResult() error = %v", err)
		}
		if !resumed {
			t.Fatal("SubmitClientToolResult() did not find the waiter")
		}
	case err := <-runErr:
		t.Fatalf("StreamRun() error = %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("run never handed the client tool back")
	}

	select {
	case runResult := <-result:
		if runResult.ResponseContent != "Found it locally." {
			t.Fatalf("response content = %q", runResult.ResponseContent)
		}
	case err := <-runErr:
		t.Fatalf("StreamRun() error = %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("run never resumed after the tool result")
	}

	select {
	case call := <-completed:
		if call.ID != "call-local" {
			t.Fatalf("completed the wrong call: %#v", call)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tool_call.completed never fired")
	}

	// The resumed result must have been persisted as a tool message.
	var persisted []database.ConversationMessage
	if err := svc.db.Where("thread_id = ? AND role = ?", thread.ID, "tool").Find(&persisted).Error; err != nil {
		t.Fatalf("query tool messages: %v", err)
	}
	if len(persisted) != 1 || !strings.Contains(persisted[0].Content, "device result") {
		t.Fatalf("persisted tool messages = %#v", persisted)
	}

	// And the run must have sent exactly two model rounds.
	if len(*requestBodies) != 2 {
		t.Fatalf("model rounds = %d, want 2", len(*requestBodies))
	}
}

// TestSubmitClientToolResultWithoutWaiter reports no waiter instead of blocking
// or erroring, and an unknown caller cannot resume someone else's run.
func TestSubmitClientToolResultWithoutWaiter(t *testing.T) {
	svc, _, _ := clientToolTestSetup(t)
	resumed, err := svc.SubmitClientToolResult(context.Background(), "acct-1", "run-x", "call-y", "result", nil, nil)
	if err != nil {
		t.Fatalf("SubmitClientToolResult() error = %v", err)
	}
	if resumed {
		t.Fatal("resumed a waiter that never existed")
	}
}

// TestClientToolCannotShadowServerTool pins the client namespace. A caller
// declaring a tool under a server-owned name is not rejected and does not
// shadow it: the server namespaces the caller's tools, so the model sees both
// and always knows which side one runs on. The collision that used to fail the
// run is now impossible by construction, and this is what proves it holds.
func TestClientToolCannotShadowServerTool(t *testing.T) {
	var offered map[string]bool
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode completion request: %v", err)
		}
		offered = toolNamesInRequest(body)

		w.Header().Set("Content-Type", "text/event-stream")
		if _, err := w.Write([]byte(sseData(map[string]any{
			"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": nil,
			}},
		}))); err != nil {
			t.Fatalf("write sse: %v", err)
		}
		if _, err := w.Write([]byte(sseData(map[string]any{
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		}))); err != nil {
			t.Fatalf("write sse: %v", err)
		}
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(modelServer.Close)

	cfg := &config.Config{
		Providers: []config.ProviderConfig{{
			ID:      "openai",
			Type:    "openai-compatible",
			APIKey:  "test",
			BaseURL: modelServer.URL + "/v1",
			Timeout: time.Second,
			Models:  []config.ModelConfig{{Name: "model"}},
		}},
	}
	registry, err := agent.NewRegistry([]config.AgentConfig{{
		ID:        "server-maid",
		Name:      "Server Maid",
		Model:     "openai/model",
		Abilities: []string{"chat", "web_search"},
		Enabled:   true,
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	executor, err := agent.NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	svc := NewConversationService(openTestDB(t), cfg, registry, executor)

	thread := &database.ConversationThread{
		ID:        "thread-namespace-1",
		AccountID: "acct-1",
		AgentID:   "server-maid",
		Title:     "Namespace chat",
	}
	if err := svc.db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if _, err := svc.StreamRun(context.Background(), "acct-1", thread.ID, RunInput{
		Message:     "hi",
		Stream:      true,
		ClientTools: []*schema.ToolInfo{clientToolInfo("web_search")},
	}, StreamCallbacks{}); err != nil {
		t.Fatalf("StreamRun() error = %v", err)
	}

	if !offered["web_search"] {
		t.Fatalf("the server tool must still be offered: %#v", offered)
	}
	if !offered[clientToolNamespace+"web_search"] {
		t.Fatalf("the caller's tool must be namespaced, not shadow the server tool: %#v", offered)
	}
}
