package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/persona/internal/agent"
	"src.solsynth.dev/sosys/persona/internal/config"
	"src.solsynth.dev/sosys/persona/internal/database"
	"src.solsynth.dev/sosys/persona/internal/service"
)

// reasoningCapture is a stand-in provider that records every completion
// payload it is handed and answers with a plain assistant message.
type reasoningCapture struct {
	bodies []map[string]any
}

func (c *reasoningCapture) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode completion request: %v", err)
			return
		}
		c.bodies = append(c.bodies, body)
		if streamed, _ := body["stream"].(bool); streamed {
			w.Header().Set("Content-Type", "text/event-stream")
			flush := func(payload map[string]any) {
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Errorf("marshal sse: %v", err)
					return
				}
				if _, err := w.Write([]byte("data: " + string(raw) + "\n\n")); err != nil {
					t.Errorf("write sse: %v", err)
					return
				}
				w.(http.Flusher).Flush()
			}
			flush(map[string]any{"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": nil,
			}}})
			flush(map[string]any{"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{}, "finish_reason": "stop",
			}}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 1,
			"model":   "model",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
		})
	}))
}

// newRunReasoningRouter wires the real run handler over a service whose agents
// share one fake provider, so a request body is observable end to end.
func newRunReasoningRouter(t *testing.T, baseURL string) (*gin.Engine, *database.DB) {
	t.Helper()

	raw, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	db := &database.DB{DB: raw}
	if err := db.AutoMigrate(); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	cfg := &config.Config{Providers: []config.ProviderConfig{{
		ID:      "openai",
		Type:    "openai-compatible",
		APIKey:  "test",
		BaseURL: baseURL + "/v1",
		Timeout: time.Second,
		Models:  []config.ModelConfig{{Name: "model"}},
	}}}
	registry, err := agent.NewRegistry([]config.AgentConfig{
		{ID: "general", Name: "General", Model: "openai/model", Enabled: true},
		{ID: "quiet", Name: "Quiet", Model: "openai/model", Enabled: true, DisableThinking: new(true)},
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	executor, err := agent.NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("account_id", "acct-1")
		c.Next()
	})
	conversations := service.NewConversationService(db, cfg, registry, executor)
	router.POST("/api/conversations/:id/runs", func(c *gin.Context) { createRun(c, conversations) })
	return router, db
}

func TestCreateRunAppliesReasoningControls(t *testing.T) {
	capture := &reasoningCapture{}
	modelServer := capture.server(t)
	defer modelServer.Close()

	router, db := newRunReasoningRouter(t, modelServer.URL)

	createThread := func(t *testing.T, id, agentID string) {
		t.Helper()
		if err := db.Create(&database.ConversationThread{ID: id, AccountID: "acct-1", AgentID: agentID, Title: "Reasoning"}).Error; err != nil {
			t.Fatalf("create thread: %v", err)
		}
	}

	cases := []struct {
		name         string
		agentID      string
		body         string
		wantStatus   int
		wantEffort   *string
		wantThinking bool
	}{
		{name: "effort override", agentID: "general", body: `{"message":"hi","reasoning_effort":"high"}`, wantStatus: http.StatusOK, wantEffort: new("high")},
		{name: "effort is case folded", agentID: "general", body: `{"message":"hi","reasoning_effort":"MAX"}`, wantStatus: http.StatusOK, wantEffort: new("max")},
		{name: "disable wins over effort", agentID: "general", body: `{"message":"hi","reasoning_effort":"high","disable_reasoning":true}`, wantStatus: http.StatusOK, wantThinking: true},
		{name: "agent default disables", agentID: "quiet", body: `{"message":"hi"}`, wantStatus: http.StatusOK, wantThinking: true},
		{name: "explicit false re-enables", agentID: "quiet", body: `{"message":"hi","disable_reasoning":false,"reasoning_effort":"low"}`, wantStatus: http.StatusOK, wantEffort: new("low")},
		{name: "unknown effort rejected", agentID: "general", body: `{"message":"hi","reasoning_effort":"hgih"}`, wantStatus: http.StatusBadRequest},
	}

	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			threadID := "thread-reasoning-" + strconv.Itoa(index)
			createThread(t, threadID, tc.agentID)
			before := len(capture.bodies)

			req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+threadID+"/runs", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				if !strings.Contains(recorder.Body.String(), "unsupported reasoning_effort") {
					t.Fatalf("error body = %s", recorder.Body.String())
				}
				if got := len(capture.bodies); got != before {
					t.Fatalf("rejected run still reached the provider: %d calls", got-before)
				}
				return
			}

			if got := len(capture.bodies); got != before+1 {
				t.Fatalf("provider calls = %d, want 1", got-before)
			}
			sent := capture.bodies[len(capture.bodies)-1]

			effort, hasEffort := sent["reasoning_effort"]
			if tc.wantEffort == nil {
				if hasEffort {
					t.Fatalf("reasoning_effort = %v, want it omitted", effort)
				}
			} else if !hasEffort || effort != *tc.wantEffort {
				t.Fatalf("reasoning_effort = %v (present %v), want %q", effort, hasEffort, *tc.wantEffort)
			}

			thinking, hasThinking := sent["thinking"]
			if hasThinking != tc.wantThinking {
				t.Fatalf("thinking = %v (present %v), want present %v", thinking, hasThinking, tc.wantThinking)
			}
			if tc.wantThinking {
				nested, ok := thinking.(map[string]any)
				if !ok || nested["type"] != "disabled" {
					t.Fatalf("thinking = %#v, want {\"type\":\"disabled\"}", thinking)
				}
			}
		})
	}
}

// TestStreamRunAppliesReasoningControls covers the streaming wiring, which
// resolves the overrides on its own path into the executor.
func TestStreamRunAppliesReasoningControls(t *testing.T) {
	capture := &reasoningCapture{}
	modelServer := capture.server(t)
	defer modelServer.Close()

	router, db := newRunReasoningRouter(t, modelServer.URL)
	if err := db.Create(&database.ConversationThread{ID: "thread-reasoning-stream", AccountID: "acct-1", AgentID: "general", Title: "Stream"}).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/conversations/thread-reasoning-stream/runs",
		bytes.NewBufferString(`{"message":"hi","stream":true,"reasoning_effort":"xhigh"}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "run.completed") {
		t.Fatalf("stream did not complete: %s", recorder.Body.String())
	}
	if len(capture.bodies) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(capture.bodies))
	}
	if got := capture.bodies[0]["reasoning_effort"]; got != "xhigh" {
		t.Fatalf("reasoning_effort = %v, want xhigh", got)
	}
}
