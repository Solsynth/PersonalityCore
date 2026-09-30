package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func newAgentTestService(t *testing.T) *service.ConversationService {
	t.Helper()
	raw, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	db := &database.DB{DB: raw}
	if err := db.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	registry, err := agent.NewRegistry([]config.AgentConfig{
		{ID: "mochi", Name: "Mochi", Model: "test", SystemPrompt: "You are Mochi.", Abilities: []string{"memory"}, Enabled: true},
		{ID: "general", Name: "General", Model: "test", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service.NewConversationService(db, &config.Config{}, registry, nil)
}

func newAgentTestRouter(svc *service.ConversationService, accountID string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		identity.SetAccountID(c, accountID)
		c.Next()
	})
	RegisterRoutes(r.Group("/api"), svc)
	return r
}

func TestListAgentsReturnsEveryEnabledAgentWithoutSystemPrompts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newAgentTestRouter(newAgentTestService(t), "acct-1")

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var agents []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &agents); err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Fatalf("returned %d agents, want 2", len(agents))
	}
	if body := response.Body.String(); strings.Contains(body, "You are Mochi.") {
		t.Fatalf("system prompt leaked into the agent list: %s", body)
	}
}

func TestDeleteAgentMemoriesEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAgentTestService(t)
	ctx := t.Context()
	thread, err := svc.CreateConversation(ctx, "acct-1", service.CreateConversationInput{AgentID: "mochi", Title: "Chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveMemory(ctx, "acct-1", "mochi", service.MemoryInput{
		Scope: "user", Category: "identity", Key: "name", Content: "Mochi knows my name.",
		Confidence: 1, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	r := newAgentTestRouter(svc, "acct-1")

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/agents/mochi/memories", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204; body = %s", response.Code, response.Body.String())
	}
	if _, err := svc.GetConversation(ctx, "acct-1", thread.ID); err != service.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete endpoint, got %v", err)
	}
	memories, err := svc.ListMemories(ctx, "acct-1", "mochi", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 0 {
		t.Fatalf("memories survived reset: %#v", memories)
	}
}
