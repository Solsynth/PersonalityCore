package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// newBillingLedgerTestService wires a conversation service with billing
// enabled, so authorizations and action charges actually write ledger rows.
func newBillingLedgerTestService(t *testing.T) *service.ConversationService {
	t.Helper()
	raw, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	db := &database.DB{DB: raw}
	if err := db.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	return service.NewConversationService(db, &config.Config{Billing: config.BillingConfig{Enabled: true, Currency: "golds"}}, nil, nil)
}

func newBillingSelfTestRouter(svc *service.ConversationService, accountID string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		identity.SetAccountID(c, accountID)
		c.Next()
	})
	RegisterBillingRoutes(r.Group("/api/billing"), svc)
	return r
}

func seedAuditedLedger(t *testing.T, svc *service.ConversationService, accountID string) {
	t.Helper()
	ctx := service.WithAuditAttribution(context.Background(), service.AuditAttribution{
		Surface:   "/api/conversations/:id/runs",
		ClientIP:  "203.0.113.9",
		DeviceID:  "device-1",
		UserAgent: "PersonaClient/1.0",
	})
	if _, err := svc.Billing().AuthorizeRun(ctx, accountID, agent.Definition{ID: "mochi", Model: "openai/model"}); err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}
	if err := svc.Billing().ChargeAction(ctx, accountID, "web_search/tavily", "2"); err != nil {
		t.Fatalf("ChargeAction: %v", err)
	}
}

func TestBillingLedgerSelfEndpointFiltersAndExposesAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newBillingLedgerTestService(t)
	seedAuditedLedger(t, svc, "account-1")
	r := newBillingSelfTestRouter(svc, "account-1")

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/billing/me/ledger?action=web_search%2Ftavily", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("X-Total"); got != "1" {
		t.Fatalf("X-Total = %q, want 1", got)
	}
	var entries []service.BillingLedgerEntry
	if err := json.Unmarshal(response.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Action != "web_search/tavily" || entry.Amount != "2.00000000" {
		t.Errorf("entry = %+v", entry)
	}
	if entry.Surface != "/api/conversations/:id/runs" || entry.ClientIP != "203.0.113.9" || entry.DeviceID != "device-1" {
		t.Errorf("attribution = %q / %q / %q", entry.Surface, entry.ClientIP, entry.DeviceID)
	}
}

func TestBillingLedgerSelfSummaryGroupsByDevice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newBillingLedgerTestService(t)
	seedAuditedLedger(t, svc, "account-1")
	r := newBillingSelfTestRouter(svc, "account-1")

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/billing/me/ledger/summary", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var summary service.BillingLedgerSummary
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary.Entries != 2 {
		t.Fatalf("entries = %d, want 2", summary.Entries)
	}
	found := false
	for _, bucket := range summary.ByDeviceID {
		if bucket.Key == "device-1" && bucket.Currency == "golds" && bucket.Entries == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("by_device_id = %+v, want a device-1/golds bucket with 2 entries", summary.ByDeviceID)
	}
}

func TestBillingLedgerRejectsBadTimeWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newBillingLedgerTestService(t)
	r := newBillingSelfTestRouter(svc, "account-1")

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/billing/me/ledger?from=yesterday", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.String())
	}
}

func TestBillingAdminLedgerIsScopedAndRequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newBillingLedgerTestService(t)
	seedAuditedLedger(t, svc, "account-1")

	r := newBillingAdminTestRouter(svc, "admin-1")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/billing/accounts/account-1/ledger", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("X-Total"); got != "2" {
		t.Fatalf("X-Total = %q, want 2", got)
	}

	// Another account's ledger must be empty, not shared.
	other := httptest.NewRecorder()
	r.ServeHTTP(other, httptest.NewRequest(http.MethodGet, "/api/admin/billing/accounts/account-2/ledger", nil))
	if other.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", other.Code)
	}
	if got := other.Header().Get("X-Total"); got != "0" {
		t.Fatalf("account-2 X-Total = %q, want 0", got)
	}

	unauthenticated := gin.New()
	RegisterBillingAdminRoutes(unauthenticated.Group("/api/admin/billing"), svc)
	denied := httptest.NewRecorder()
	unauthenticated.ServeHTTP(denied, httptest.NewRequest(http.MethodGet, "/api/admin/billing/accounts/account-1/ledger", nil))
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", denied.Code)
	}
}
