package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	"src.solsynth.dev/sosys/persona/internal/identity"
	"src.solsynth.dev/sosys/persona/internal/service"
)

// newUsageRouter wires the real conversation routes over a provider that
// reports token usage on both the streaming and non-streaming paths.
func newUsageRouter(t *testing.T) (*gin.Engine, *database.DB) {
	t.Helper()

	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if streamed, _ := body["stream"].(bool); streamed {
			w.Header().Set("Content-Type", "text/event-stream")
			write := func(payload map[string]any) {
				raw, _ := json.Marshal(payload)
				_, _ = w.Write([]byte("data: " + string(raw) + "\n\n"))
				w.(http.Flusher).Flush()
			}
			write(map[string]any{"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"role": "assistant", "content": "hello"}, "finish_reason": nil,
			}}})
			write(map[string]any{
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
				"usage":   map[string]any{"prompt_tokens": 4, "completion_tokens": 6, "total_tokens": 10},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "completion-1", "model": "model",
			"choices": []any{map[string]any{
				"index": 0, "message": map[string]any{"role": "assistant", "content": "hello"}, "finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 3, "total_tokens": 5},
		})
	}))
	t.Cleanup(modelServer.Close)

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
		BaseURL: modelServer.URL + "/v1",
		Timeout: time.Second,
		Models:  []config.ModelConfig{{Name: "model", ContextWindow: 1000}},
	}}}
	registry, err := agent.NewRegistry([]config.AgentConfig{{ID: "general", Name: "General", Model: "openai/model", Enabled: true}})
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
		identity.SetAccountID(c, "acct-1")
		c.Next()
	})
	RegisterRoutes(router.Group("/api"), service.NewConversationService(db, cfg, registry, executor))
	return router, db
}

func TestRunResponseCarriesUsageAndConversationUsageTotals(t *testing.T) {
	router, db := newUsageRouter(t)
	if err := db.Create(&database.ConversationThread{ID: "thread-usage", AccountID: "acct-1", AgentID: "general", Title: "Usage"}).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}

	run := func() map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/conversations/thread-usage/runs", strings.NewReader(`{"message":"hi"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("run status = %d; body = %s", response.Code, response.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal run response: %v", err)
		}
		return payload
	}

	for range 2 {
		payload := run()
		runObject, ok := payload["run"].(map[string]any)
		if !ok {
			t.Fatalf("run object missing: %#v", payload["run"])
		}
		usage, ok := runObject["usage"].(map[string]any)
		if !ok {
			t.Fatalf("run usage missing: %#v", runObject["usage"])
		}
		if usage["input_tokens"] != float64(2) || usage["output_tokens"] != float64(3) || usage["total_tokens"] != float64(5) {
			t.Fatalf("run usage = %#v, want 2/3/5", usage)
		}
		context, ok := usage["context"].(map[string]any)
		if !ok {
			t.Fatalf("run context usage missing: %#v", usage["context"])
		}
		if context["used_tokens"] != float64(2) || context["window_tokens"] != float64(1000) {
			t.Fatalf("run context = %#v, want 2/1000", context)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/conversations/thread-usage/usage", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("usage status = %d; body = %s", response.Code, response.Body.String())
	}
	var aggregate map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &aggregate); err != nil {
		t.Fatalf("unmarshal usage response: %v", err)
	}
	if aggregate["runs"] != float64(2) || aggregate["input_tokens"] != float64(4) || aggregate["output_tokens"] != float64(6) || aggregate["total_tokens"] != float64(10) {
		t.Fatalf("aggregate = %#v, want 2 runs and 4/6/10", aggregate)
	}
	if aggregate["peak_context_used_tokens"] != float64(2) || aggregate["context_window_tokens"] != float64(1000) {
		t.Fatalf("aggregate context = %#v", aggregate)
	}
}

func TestStreamRunCompletedEventCarriesUsage(t *testing.T) {
	router, db := newUsageRouter(t)
	if err := db.Create(&database.ConversationThread{ID: "thread-stream-usage", AccountID: "acct-1", AgentID: "general", Title: "Usage"}).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/conversations/thread-stream-usage/runs", strings.NewReader(`{"message":"hi","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("stream status = %d; body = %s", response.Code, response.Body.String())
	}

	body := response.Body.String()
	if !strings.Contains(body, "event: run.completed") {
		t.Fatalf("stream did not complete: %s", body)
	}
	for _, frame := range strings.Split(body, "\n\n") {
		lines := strings.Split(strings.TrimSpace(frame), "\n")
		if len(lines) < 2 || strings.TrimSpace(strings.TrimPrefix(lines[0], "event:")) != "run.completed" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(lines[1], "data:"))), &payload); err != nil {
			t.Fatalf("unmarshal run.completed: %v", err)
		}
		usage, ok := payload["usage"].(map[string]any)
		if !ok {
			t.Fatalf("run.completed usage missing: %#v", payload["usage"])
		}
		if usage["input_tokens"] != float64(4) || usage["output_tokens"] != float64(6) || usage["total_tokens"] != float64(10) {
			t.Fatalf("streamed usage = %#v, want 4/6/10", usage)
		}
		return
	}
	t.Fatalf("run.completed frame not found: %s", body)
}
