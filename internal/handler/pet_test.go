package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
	"src.solsynth.dev/sosys/personality/internal/identity"
	"src.solsynth.dev/sosys/personality/internal/service"
)

func newPetFilterTestService(t *testing.T) *service.ConversationService {
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
		{ID: "mochi", Name: "Mochi", Model: "test", Abilities: []string{"pet", "memory"}, Enabled: true},
		{ID: "general", Name: "General", Model: "test", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service.NewConversationService(db, &config.Config{}, registry, nil)
}

func newPetFilterTestRouter(svc *service.ConversationService, accountID string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		identity.SetAccountID(c, accountID)
		c.Next()
	})
	RegisterRoutes(r.Group("/api"), svc)
	return r
}

func TestListAgentsPetFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newPetFilterTestRouter(newPetFilterTestService(t), "acct-1")

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/agents?pet=true", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("pet=true status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var pets []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &pets); err != nil {
		t.Fatal(err)
	}
	if len(pets) != 1 || pets[0]["id"] != "mochi" {
		t.Fatalf("pet=true returned %v, want only mochi", pets)
	}

	response = httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("unfiltered status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var all []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("unfiltered returned %d agents, want 2", len(all))
	}
}

func TestDeleteAgentMemoriesEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newPetFilterTestService(t)
	ctx := t.Context()
	if _, err := svc.GetOrCreatePetThread(ctx, "acct-1", "mochi"); err != nil {
		t.Fatal(err)
	}
	r := newPetFilterTestRouter(svc, "acct-1")

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/agents/mochi/memories", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204; body = %s", response.Code, response.Body.String())
	}
	if _, err := svc.GetPetAffection(ctx, "acct-1", "mochi"); err != service.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete endpoint, got %v", err)
	}
}
