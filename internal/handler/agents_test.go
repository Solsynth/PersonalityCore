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
	sharedauth "src.solsynth.dev/sosys/go/pkg/auth"
	gen "src.solsynth.dev/sosys/go/proto"

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
		{ID: "planner", Name: "Planner", Model: "test", Enabled: true, Hidden: true},
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
	if body := response.Body.String(); strings.Contains(body, `"planner"`) {
		t.Fatalf("hidden agent leaked into the agent list: %s", body)
	}
	if body := response.Body.String(); strings.Contains(body, "You are Mochi.") {
		t.Fatalf("system prompt leaked into the agent list: %s", body)
	}
}

func TestHiddenAgentStaysUsableById(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAgentTestService(t)
	r := newAgentTestRouter(svc, "acct-1")

	// The hidden agent is not in the catalog...
	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	if strings.Contains(response.Body.String(), `"planner"`) {
		t.Fatalf("hidden agent leaked into the agent list: %s", response.Body.String())
	}

	// ...but it still resolves by id and can back a conversation.
	response = httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/agents/planner", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("get hidden agent status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	if _, err := svc.CreateConversation(t.Context(), "acct-1", service.CreateConversationInput{AgentID: "planner", Title: "Internal"}); err != nil {
		t.Fatalf("creating a conversation for the hidden agent failed: %v", err)
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

// newCatalogTestService builds a service whose catalog carries internal
// billing metadata, so tests can assert what reaches non-privileged callers.
func newCatalogTestService(t *testing.T) *service.ConversationService {
	t.Helper()
	raw, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	db := &database.DB{DB: raw}
	if err := db.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	multiplier := 2.5
	registry, err := agent.NewRegistry([]config.AgentConfig{
		{
			ID:                "mochi",
			Name:              "Mochi",
			Model:             "gpt-test",
			Abilities:         []string{"memory"},
			BillingMultiplier: &multiplier,
			Enabled:           true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := "1"
	output := "2"
	cfg := &config.Config{Providers: []config.ProviderConfig{{
		ID: "openai",
		Models: []config.ModelConfig{{
			Name:    "gpt-test",
			Pricing: &config.ModelPricingConfig{Currency: "golds", Input: &input, Output: &output},
			PerkOverrides: map[int]config.ModelPerkOverride{
				1: {},
			},
		}},
	}}}
	return service.NewConversationService(db, cfg, registry, nil)
}

func newSuperuserRouter(svc *service.ConversationService, accountID string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		identity.SetAccountID(c, accountID)
		sharedauth.WithAuth(c, &sharedauth.AuthResult{Account: &gen.DyAccount{Id: accountID, IsSuperuser: true}}, sharedauth.TokenInfo{Token: "test-token"})
		c.Next()
	})
	RegisterRoutes(r.Group("/api"), svc)
	return r
}

func TestAgentAndModelRoutesRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterRoutes(r.Group("/api"), newCatalogTestService(t))

	for _, path := range []string{"/api/agents", "/api/agents/mochi", "/api/agents/planner", "/api/models"} {
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s status = %d, want 401; body = %s", path, response.Code, response.Body.String())
		}
	}
}

func TestCatalogHidesInternalBillingFromNonPrivilegedCallers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newCatalogTestService(t)

	// A plain authenticated account.
	r := newAgentTestRouter(svc, "acct-1")
	for _, path := range []string{"/api/agents", "/api/agents/mochi", "/api/models"} {
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200; body = %s", path, response.Code, response.Body.String())
		}
		body := response.Body.String()
		for _, field := range []string{"pricing", "perk_overrides", "billing_multiplier"} {
			if strings.Contains(body, field) {
				t.Fatalf("%s exposed internal field %q to a non-privileged caller: %s", path, field, body)
			}
		}
	}

	// A superuser still gets the internal billing metadata.
	super := newSuperuserRouter(svc, "acct-1")
	response := httptest.NewRecorder()
	super.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("superuser agents status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, "billing_multiplier") {
		t.Fatalf("superuser lost the billing multiplier: %s", body)
	}
	response = httptest.NewRecorder()
	super.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/agents/mochi", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("superuser agent detail status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, "billing_multiplier") {
		t.Fatalf("superuser lost the billing multiplier on the agent detail: %s", body)
	}
	response = httptest.NewRecorder()
	super.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("superuser models status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "pricing") || !strings.Contains(body, "perk_overrides") {
		t.Fatalf("superuser lost the model pricing metadata: %s", body)
	}
}
