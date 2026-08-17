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

	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
	"src.solsynth.dev/sosys/personality/internal/identity"
	"src.solsynth.dev/sosys/personality/internal/service"
)

func newBillingAdminTestService(t *testing.T) *service.ConversationService {
	t.Helper()
	raw, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	db := &database.DB{DB: raw}
	if err := db.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	return service.NewConversationService(db, &config.Config{Billing: config.BillingConfig{Currency: "golds"}}, nil, nil)
}

func newBillingAdminTestRouter(svc *service.ConversationService, accountID string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		identity.SetAccountID(c, accountID)
		c.Next()
	})
	RegisterBillingAdminRoutes(r.Group("/api/admin/billing"), svc)
	return r
}

func TestBillingAdminAccountOperationalEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newBillingAdminTestService(t)
	created, err := svc.CreateOpenAICredential(t.Context(), "account-1", service.CreateOpenAICredentialInput{Name: "test credential", UsageLimit: "0"})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	r := newBillingAdminTestRouter(svc, "admin-1")

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/billing/accounts/account-1/usage", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("usage status = %d, want 200; body = %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/billing/accounts/account-1/openai-credentials", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("credentials status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data []service.OpenAICredential `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode credentials: %v", err)
	}
	if len(payload.Data) != 1 || payload.Data[0].ID != created.Credential.ID {
		t.Fatalf("credentials = %#v, want credential %q", payload.Data, created.Credential.ID)
	}

	response = httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/admin/billing/accounts/account-1/openai-credentials/"+created.Credential.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, want 204; body = %s", response.Code, response.Body.String())
	}
}

func TestBillingAdminEndpointsRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newBillingAdminTestService(t)
	r := gin.New()
	RegisterBillingAdminRoutes(r.Group("/api/admin/billing"), svc)

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/billing/accounts/account-1/usage", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %s", response.Code, response.Body.String())
	}
}
