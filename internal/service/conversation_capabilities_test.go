package service

import (
	"context"
	"encoding/json"
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

// capabilityTestThread stores a thread and returns it, so a test can exercise
// the paths that read a conversation's own state.
func capabilityTestThread(t *testing.T, svc *ConversationService, id string) *database.ConversationThread {
	t.Helper()
	thread := &database.ConversationThread{
		ID:        id,
		AccountID: "acct-1",
		AgentID:   "server-maid",
		Title:     "Capability chat",
	}
	if err := svc.db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	return thread
}

// TestBuildModelMessagesPlacesCallerContextAfterAgentPrompt pins where a
// caller's own prompt text lands: directly after the agent's prompt, before
// the overlays the server derives. It is capability text, so it belongs with
// the instructions rather than after the character and persona notes.
func TestBuildModelMessagesPlacesCallerContextAfterAgentPrompt(t *testing.T) {
	svc := &ConversationService{
		db: openTestDB(t),
		cfg: &config.Config{
			Personality: config.PersonalityConfig{MaxHistoryMessages: 24},
		},
	}
	registry, err := agent.NewRegistry([]config.AgentConfig{{
		ID:           "server-maid",
		Name:         "Server Maid",
		Model:        "openai/test",
		Abilities:    []string{"chat"},
		Enabled:      true,
		SystemPrompt: "You are helpful.",
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	svc.registry = registry

	thread := capabilityTestThread(t, svc, "thread-caller-context")
	if _, err := svc.createMessageWithMetadata(context.Background(), thread, nil, "user", "hello", nil, nil); err != nil {
		t.Fatalf("create user message: %v", err)
	}

	messages, _, err := svc.buildModelMessages(
		context.Background(), thread.AccountID, thread.ID, 0, "", "",
		[]string{"  ", "Web tools run on the user's own connection."},
	)
	if err != nil {
		t.Fatalf("buildModelMessages() error = %v", err)
	}
	if len(messages) < 2 {
		t.Fatalf("expected at least two messages, got %d", len(messages))
	}
	if messages[0].Role != schema.System || messages[0].Content != "You are helpful." {
		t.Fatalf("first message = %#v", messages[0])
	}
	if messages[1].Role != schema.System {
		t.Fatalf("caller context must be a system message, got %#v", messages[1])
	}
	if !strings.Contains(messages[1].Content, "own connection") {
		t.Fatalf("caller context = %q", messages[1].Content)
	}
	// A blank fragment is dropped rather than sent as an empty system message.
	for _, message := range messages {
		if message.Role == schema.System && strings.TrimSpace(message.Content) == "" {
			t.Fatal("a blank caller fragment reached the model")
		}
	}
}

// TestActivatedSkillsRoundTripThroughThread covers the conversation's own
// record of what it switched on: it survives a reload, and the run path builds
// the activated skill's tools from it without the model asking again.
//
// The skill under test is one the agent does not auto-load, so its tools being
// offered can only have come from the conversation's record.
func TestActivatedSkillsRoundTripThroughThread(t *testing.T) {
	svc := &ConversationService{
		db: openTestDB(t),
		// Dynamic skills: only chat and a few others are auto-loaded, so what
		// the conversation has activated is what decides the rest.
		cfg: &config.Config{Personality: config.PersonalityConfig{DynamicSkills: true}},
	}
	registry, err := agent.NewRegistry([]config.AgentConfig{{
		ID:        "server-maid",
		Name:      "Server Maid",
		Model:     "openai/test",
		Abilities: []string{"chat"},
		Enabled:   true,
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	svc.registry = registry

	thread := capabilityTestThread(t, svc, "thread-activated-skills")
	if active := activatedSkills(thread); len(active) != 0 {
		t.Fatalf("a fresh conversation has nothing activated, got %#v", active)
	}

	active := map[string]bool{"memory": true}
	svc.persistActivatedSkills(context.Background(), thread, active)

	var reloaded database.ConversationThread
	if err := svc.db.Where("id = ?", thread.ID).First(&reloaded).Error; err != nil {
		t.Fatalf("reload thread: %v", err)
	}
	if got := activatedSkills(&reloaded); !got["memory"] || len(got) != 1 {
		t.Fatalf("activated skills did not survive the round trip: %#v", got)
	}

	def, ok := registry.Get("server-maid")
	if !ok {
		t.Fatal("agent missing from the registry")
	}
	// The next run must offer the activated skill's tools from its first round.
	activated := toolNamesOf(svc.conversationToolInfos(def, activatedSkills(&reloaded), 0, false))
	for _, name := range []string{memorySearchToolName, memorySaveToolName} {
		if activated[name] == 0 {
			t.Fatalf("an activated skill's tools must be built into the run, got %#v", activated)
		}
	}
	// A conversation that activated nothing gets none of them: the record is
	// what carries the activation, not the skill's mere existence.
	fresh := toolNamesOf(svc.conversationToolInfos(def, map[string]bool{}, 0, false))
	for _, name := range []string{memorySearchToolName, memorySaveToolName} {
		if fresh[name] != 0 {
			t.Fatalf("an unactivated skill's tools leaked into the run, got %#v", fresh)
		}
	}
}

// TestStreamRunAddsClientToolsLoadedMidRun covers a capability loaded by name
// during a turn: the caller answers `activate_local_skill` with the tool
// definitions that load made callable, and the same run must offer them on its
// next model call. Without this, loading something would only pay off from the
// following message.
func TestStreamRunAddsClientToolsLoadedMidRun(t *testing.T) {
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
		switch len(requestBodies) {
		case 1:
			flush(sseData(map[string]any{
				"choices": []any{map[string]any{
					"index": 0,
					"delta": map[string]any{
						"role": "assistant",
						"tool_calls": []any{map[string]any{
							"index": 0, "id": "call-activate", "type": "function",
							"function": map[string]any{
								"name":      clientToolNamespace + "load_skill",
								"arguments": `{"skill":"local_device"}`,
							},
						}},
					},
					"finish_reason": nil,
				}},
			}))
			flush(sseData(map[string]any{
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}},
			}))
		case 2:
			if !toolNamesInRequest(body)[clientToolNamespace+"discount"] {
				t.Fatalf("the tools the caller loaded mid-run were not offered: %#v", body["tools"])
			}
			flush(sseData(map[string]any{
				"choices": []any{map[string]any{
					"index": 0, "delta": map[string]any{"role": "assistant", "content": "Loaded."}, "finish_reason": nil,
				}},
			}))
			flush(sseData(map[string]any{
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			}))
		default:
			t.Fatalf("unexpected model round %d", len(requestBodies))
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
	thread := capabilityTestThread(t, svc, "thread-mid-run-load")

	type handoff struct {
		runID string
		call  schema.ToolCall
	}
	handed := make(chan handoff, 1)
	runResult := make(chan *RunResult, 1)
	runErr := make(chan error, 1)

	go func() {
		result, err := svc.StreamRun(context.Background(), "acct-1", thread.ID, RunInput{
			Message:      "what is on this machine",
			Stream:       true,
			ClientTools:  []*schema.ToolInfo{clientToolInfo("load_skill")},
			ClientSkills: []ClientSkill{{Name: "device", Description: "Read the user's files"}},
		}, StreamCallbacks{
			OnClientTool: func(runID string, call schema.ToolCall) error {
				handed <- handoff{runID: runID, call: call}
				return nil
			},
		})
		if err != nil {
			runErr <- err
			return
		}
		runResult <- result
	}()

	select {
	case got := <-handed:
		if got.call.Function.Name != loadSkillToolName {
			t.Fatalf("handed back the wrong tool: %#v", got.call)
		}
		resumed, err := svc.SubmitClientToolResult(
			context.Background(), "acct-1", got.runID, got.call.ID,
			`{"ok":true,"skill":"local_device"}`,
			[]*schema.ToolInfo{clientToolInfo("discount")},
		)
		if err != nil {
			t.Fatalf("SubmitClientToolResult() error = %v", err)
		}
		if !resumed {
			t.Fatal("SubmitClientToolResult() did not find the waiter")
		}
	case err := <-runErr:
		t.Fatalf("StreamRun() error = %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the run never handed the loading call back")
	}

	select {
	case result := <-runResult:
		if result.ResponseContent != "Loaded." {
			t.Fatalf("response content = %q", result.ResponseContent)
		}
	case err := <-runErr:
		t.Fatalf("StreamRun() error = %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the run never finished after the tool result")
	}

	if len(requestBodies) != 2 {
		t.Fatalf("model rounds = %d, want 2", len(requestBodies))
	}
}

// TestActivateSkillRefusesCallerOwnedSkill keeps the two activation routes
// apart: a caller-owned capability can only be loaded by the caller, so the
// server must say so rather than report a skill it does not have.
func TestActivateSkillRefusesCallerOwnedSkill(t *testing.T) {
	svc := &ConversationService{cfg: &config.Config{}}
	call := schema.ToolCall{
		ID:       "call-local-load",
		Function: schema.FunctionCall{Name: "activate_skill", Arguments: `{"skill":"local_device"}`},
	}
	// The caller declares the skill under its own name; the server namespaces
	// it before the model, and the model therefore asks for the namespaced one.
	clientSkills := []ClientSkill{{Name: "device", Description: "Read the user's files"}}

	result, activated := svc.executeActivateSkillToolCall(call, map[string]bool{}, agent.Definition{ID: "plain"}, clientSkills)
	if activated {
		t.Fatal("a caller-owned capability must not be recorded as activated server-side")
	}
	if !strings.Contains(result.Content, loadSkillToolName) {
		t.Fatalf("the refusal must name the tool that loads it: %s", result.Content)
	}

	// The catalogue the model reads offers both routes, each with the tool
	// that loads it.
	listed := svc.executeListSkillsToolCall(agent.Definition{ID: "plain"}, map[string]bool{}, 0, clientSkills)
	if !strings.Contains(listed.Content, `"name":"local_device"`) {
		t.Fatalf("the caller's capability was not listed: %s", listed.Content)
	}
	if !strings.Contains(listed.Content, `"activate_with":"`+loadSkillToolName+`"`) {
		t.Fatalf("the listing must name the loading tool: %s", listed.Content)
	}
}

// toolNamesInRequest reports the tool names the model was offered in one
// completion request.
func toolNamesInRequest(body map[string]any) map[string]bool {
	names := map[string]bool{}
	tools, _ := body["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		function, _ := tool["function"].(map[string]any)
		if name, ok := function["name"].(string); ok {
			names[name] = true
		}
	}
	return names
}
