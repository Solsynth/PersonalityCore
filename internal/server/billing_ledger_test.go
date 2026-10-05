package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/persona/internal/config"
	"src.solsynth.dev/sosys/persona/internal/service"
)

// A billable action taken through the real router must reach the ledger with
// the endpoint, address and device id the audit middleware captured, so the
// account owner can trace the charge back to the call that caused it.
func TestBillingLedgerCapturesRequestAttribution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stack := newSearchStack(t)
	cfg := webSearchConfig(stack)
	cfg.Billing = config.BillingConfig{Enabled: true, Currency: "golds"}
	cfg.WebSearch.Engines[0].Price = "1.5"
	router, conversations, _ := newWebSearchRouterWithService(t, cfg)
	conversations.Billing().SetWalletChecker(testWalletChecker{exists: true})

	search := httptest.NewRequest(http.MethodPost, "/api/web/search", strings.NewReader(`{"query":"postgres 18 release"}`))
	search.Header.Set("Content-Type", "application/json")
	search.Header.Set("X-Device-Id", "dev-smoke")
	searchResponse := httptest.NewRecorder()
	router.ServeHTTP(searchResponse, search)
	if searchResponse.Code != http.StatusOK {
		t.Fatalf("search status = %d, body = %s", searchResponse.Code, searchResponse.Body.String())
	}

	ledger := httptest.NewRequest(http.MethodGet, "/api/billing/me/ledger?device_id=dev-smoke", nil)
	ledgerResponse := httptest.NewRecorder()
	router.ServeHTTP(ledgerResponse, ledger)
	if ledgerResponse.Code != http.StatusOK {
		t.Fatalf("ledger status = %d, body = %s", ledgerResponse.Code, ledgerResponse.Body.String())
	}
	if got := ledgerResponse.Header().Get("X-Total"); got != "1" {
		t.Fatalf("X-Total = %q, want 1; body = %s", got, ledgerResponse.Body.String())
	}

	var entries []service.BillingLedgerEntry
	if err := json.Unmarshal(ledgerResponse.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode ledger: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Surface != "/api/web/search" {
		t.Errorf("surface = %q, want /api/web/search", entry.Surface)
	}
	if entry.ClientIP != "192.0.2.1" {
		t.Errorf("client_ip = %q, want the test request address", entry.ClientIP)
	}
	if entry.DeviceID != "dev-smoke" {
		t.Errorf("device_id = %q, want dev-smoke", entry.DeviceID)
	}
	if !strings.HasPrefix(entry.Action, "web_search/") {
		t.Errorf("action = %q, want a web_search charge", entry.Action)
	}
	if entry.Amount != "1.50000000" {
		t.Errorf("amount = %q, want 1.50000000", entry.Amount)
	}
}
