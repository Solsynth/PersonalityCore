package service

import (
	"context"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"src.solsynth.dev/sosys/persona/internal/agent"
	"src.solsynth.dev/sosys/persona/internal/config"
	"src.solsynth.dev/sosys/persona/internal/database"
)

func testAuditBilling(t *testing.T) *BillingService {
	t.Helper()
	return NewBillingService(openTestDB(t), &config.Config{Billing: config.BillingConfig{Enabled: true, Currency: "golds"}})
}

func findBucket(buckets []BillingLedgerBucket, key, currency string) (BillingLedgerBucket, bool) {
	for _, bucket := range buckets {
		if bucket.Key == key && bucket.Currency == currency {
			return bucket, true
		}
	}
	return BillingLedgerBucket{}, false
}

// A generation's ledger row must carry the endpoint, credential, address,
// device and user agent of the call that reserved it, so the audit can answer
// "which call from which device spent this".
func TestBillingLedgerRecordsGenerationAttribution(t *testing.T) {
	svc := testAuditBilling(t)
	def := agent.Definition{ID: "mochi", Model: "openai/model"}
	ctx := WithAuditAttribution(context.Background(), AuditAttribution{
		Surface:   "/api/conversations/:id/runs",
		ClientIP:  "203.0.113.7",
		DeviceID:  "device-9",
		UserAgent: "PersonaClient/1.0",
	})

	usageID, err := svc.AuthorizeRun(ctx, "acct-1", def)
	if err != nil {
		t.Fatalf("AuthorizeRun() error = %v", err)
	}
	if usageID == "" {
		t.Fatal("AuthorizeRun() returned no reservation")
	}
	if err := svc.RecordUsage(ctx, usageID, "run-1", def, &schema.TokenUsage{PromptTokens: 1000, CompletionTokens: 500}); err != nil {
		t.Fatalf("RecordUsage() error = %v", err)
	}

	entries, total, err := svc.Ledger(context.Background(), BillingLedgerQuery{AccountID: "acct-1"})
	if err != nil {
		t.Fatalf("Ledger() error = %v", err)
	}
	if total != 1 || len(entries) != 1 {
		t.Fatalf("Ledger() = %d entries of total %d, want 1 of 1", len(entries), total)
	}
	entry := entries[0]
	if entry.Action != billingActionGeneration {
		t.Errorf("action = %q, want %q", entry.Action, billingActionGeneration)
	}
	if entry.Surface != "/api/conversations/:id/runs" {
		t.Errorf("surface = %q", entry.Surface)
	}
	if entry.ClientIP != "203.0.113.7" || entry.DeviceID != "device-9" || entry.UserAgent != "PersonaClient/1.0" {
		t.Errorf("attribution = %q / %q / %q", entry.ClientIP, entry.DeviceID, entry.UserAgent)
	}
	if entry.Model != "openai/model" {
		t.Errorf("model = %q, want openai/model", entry.Model)
	}
	if entry.RunID == nil || *entry.RunID != "run-1" {
		t.Errorf("run_id = %v, want run-1", entry.RunID)
	}
	if entry.InputTokens != 1000 || entry.OutputTokens != 500 {
		t.Errorf("tokens = %d/%d, want 1000/500", entry.InputTokens, entry.OutputTokens)
	}
}

// A credential-scoped call must keep the credential id on the row, so usage
// made through a third-party API key is traceable to that key.
func TestBillingLedgerRecordsCredentialAttribution(t *testing.T) {
	svc := testAuditBilling(t)
	ctx := WithAuditAttribution(context.Background(), AuditAttribution{Surface: "/v1/chat/completions"})
	ctx = WithAuditCredential(ctx, "credential-7")

	if _, err := svc.AuthorizeRun(ctx, "acct-1", agent.Definition{Model: "openai/model"}); err != nil {
		t.Fatalf("AuthorizeRun() error = %v", err)
	}

	entries, _, err := svc.Ledger(context.Background(), BillingLedgerQuery{AccountID: "acct-1", CredentialID: "credential-7"})
	if err != nil {
		t.Fatalf("Ledger() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("credential filter returned %d entries, want 1", len(entries))
	}
	if entries[0].CredentialID == nil || *entries[0].CredentialID != "credential-7" {
		t.Fatalf("credential_id = %v, want credential-7", entries[0].CredentialID)
	}
}

// A non-generation charge is its own action, filterable as such, and carries
// the same request attribution as a generation.
func TestBillingChargeActionStampsActionAndAttribution(t *testing.T) {
	svc := testAuditBilling(t)
	ctx := WithAuditAttribution(context.Background(), AuditAttribution{
		Surface:  "/api/web/search",
		ClientIP: "198.51.100.4",
		DeviceID: "device-web",
	})
	if err := svc.ChargeAction(ctx, "acct-2", "web_search/tavily", "0.5"); err != nil {
		t.Fatalf("ChargeAction() error = %v", err)
	}

	entries, total, err := svc.Ledger(context.Background(), BillingLedgerQuery{AccountID: "acct-2", Action: "web_search/tavily"})
	if err != nil {
		t.Fatalf("Ledger() error = %v", err)
	}
	if total != 1 || len(entries) != 1 {
		t.Fatalf("Ledger() = %d of %d, want 1 of 1", len(entries), total)
	}
	entry := entries[0]
	if entry.Action != "web_search/tavily" || entry.Model != "web_search/tavily" {
		t.Errorf("action/model = %q/%q", entry.Action, entry.Model)
	}
	if entry.Amount != "0.50000000" {
		t.Errorf("amount = %q, want 0.50000000", entry.Amount)
	}
	if entry.Surface != "/api/web/search" || entry.ClientIP != "198.51.100.4" || entry.DeviceID != "device-web" {
		t.Errorf("attribution = %q / %q / %q", entry.Surface, entry.ClientIP, entry.DeviceID)
	}
	if entry.RunID != nil {
		t.Errorf("run_id = %v, want nil for a non-generation charge", entry.RunID)
	}

	other, otherTotal, err := svc.Ledger(context.Background(), BillingLedgerQuery{AccountID: "acct-2", Action: billingActionGeneration})
	if err != nil {
		t.Fatalf("Ledger() error = %v", err)
	}
	if otherTotal != 0 || len(other) != 0 {
		t.Fatalf("generation filter returned %d rows, want none", len(other))
	}
}

// The summary must split consumption along every audit dimension the request
// asked about, keeping each bucket single-currency.
func TestBillingLedgerSummaryGroupsByAuditDimension(t *testing.T) {
	db := openTestDB(t)
	svc := NewBillingService(db, &config.Config{Billing: config.BillingConfig{Enabled: true, Currency: "golds"}})
	day1 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	rows := []database.BillingUsage{
		{ID: newID(), AccountID: "acct-3", Action: "generation", Surface: "/api/conversations/:id/runs", Model: "openai/model", Currency: "golds", ClientIP: "10.0.0.1", DeviceID: "a", Amount: "1.00000000", OriginalAmount: "1.00000000", CreatedAt: day1},
		{ID: newID(), AccountID: "acct-3", Action: "generation", Surface: "/v1/chat/completions", Model: "openai/model", Currency: "golds", ClientIP: "10.0.0.2", DeviceID: "b", Amount: "2.50000000", OriginalAmount: "2.50000000", CreatedAt: day1},
		{ID: newID(), AccountID: "acct-3", Action: "web_search/tavily", Surface: "/api/web/search", Model: "web_search/tavily", Currency: "points", ClientIP: "10.0.0.1", DeviceID: "a", Amount: "4.00000000", OriginalAmount: "4.00000000", CreatedAt: day2},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	summary, err := svc.LedgerSummary(context.Background(), BillingLedgerQuery{AccountID: "acct-3"})
	if err != nil {
		t.Fatalf("LedgerSummary() error = %v", err)
	}
	if summary.Entries != 3 {
		t.Fatalf("entries = %d, want 3", summary.Entries)
	}

	generation, ok := findBucket(summary.ByAction, "generation", "golds")
	if !ok || generation.Entries != 2 || generation.Amount != "3.50000000" {
		t.Errorf("by_action generation = %+v (found=%v), want 2 entries / 3.50000000", generation, ok)
	}
	search, ok := findBucket(summary.ByAction, "web_search/tavily", "points")
	if !ok || search.Entries != 1 || search.Amount != "4.00000000" {
		t.Errorf("by_action web_search = %+v (found=%v)", search, ok)
	}

	// One device spans two currencies, so it yields one bucket per currency.
	if _, ok := findBucket(summary.ByDeviceID, "a", "golds"); !ok {
		t.Errorf("by_device_id missing a/golds in %+v", summary.ByDeviceID)
	}
	if _, ok := findBucket(summary.ByDeviceID, "a", "points"); !ok {
		t.Errorf("by_device_id missing a/points in %+v", summary.ByDeviceID)
	}
	if ip, ok := findBucket(summary.ByClientIP, "10.0.0.2", "golds"); !ok || ip.Entries != 1 || ip.Amount != "2.50000000" {
		t.Errorf("by_client_ip 10.0.0.2 = %+v (found=%v)", ip, ok)
	}
	if surface, ok := findBucket(summary.BySurface, "/v1/chat/completions", "golds"); !ok || surface.Entries != 1 {
		t.Errorf("by_surface openai = %+v (found=%v)", surface, ok)
	}
	if currency, ok := findBucket(summary.ByCurrency, "golds", "golds"); !ok || currency.Entries != 2 || currency.Amount != "3.50000000" {
		t.Errorf("by_currency golds = %+v (found=%v)", currency, ok)
	}
	if day, ok := findBucket(summary.ByDay, "2026-03-01", "golds"); !ok || day.Entries != 2 || day.Amount != "3.50000000" {
		t.Errorf("by_day 2026-03-01 = %+v (found=%v)", day, ok)
	}
	if day, ok := findBucket(summary.ByDay, "2026-03-02", "points"); !ok || day.Entries != 1 || day.Amount != "4.00000000" {
		t.Errorf("by_day 2026-03-02 = %+v (found=%v)", day, ok)
	}
}

// The ledger list must honour the time window, the unpaid filter and the
// take/offset window like every other list endpoint.
func TestBillingLedgerFiltersAndPagination(t *testing.T) {
	db := openTestDB(t)
	svc := NewBillingService(db, &config.Config{Billing: config.BillingConfig{Enabled: true, Currency: "golds"}})
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	paid := "payment-1"
	rows := []database.BillingUsage{
		{ID: newID(), AccountID: "acct-4", Action: "generation", Currency: "golds", Amount: "1.00000000", OriginalAmount: "1.00000000", PaymentID: &paid, CreatedAt: base},
		{ID: newID(), AccountID: "acct-4", Action: "generation", Currency: "golds", Amount: "2.00000000", OriginalAmount: "2.00000000", CreatedAt: base.Add(24 * time.Hour)},
		{ID: newID(), AccountID: "acct-4", Action: "generation", Currency: "golds", Amount: "3.00000000", OriginalAmount: "3.00000000", CreatedAt: base.Add(48 * time.Hour)},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	from := base.Add(12 * time.Hour)
	to := base.Add(60 * time.Hour)
	entries, total, err := svc.Ledger(context.Background(), BillingLedgerQuery{AccountID: "acct-4", From: &from, To: &to})
	if err != nil {
		t.Fatalf("Ledger() error = %v", err)
	}
	if total != 2 || len(entries) != 2 {
		t.Fatalf("windowed ledger = %d of %d, want 2 of 2", len(entries), total)
	}
	if !entries[0].CreatedAt.After(entries[1].CreatedAt) {
		t.Errorf("ledger is not newest-first: %v then %v", entries[0].CreatedAt, entries[1].CreatedAt)
	}

	_, unpaidTotal, err := svc.Ledger(context.Background(), BillingLedgerQuery{AccountID: "acct-4", UnpaidOnly: true})
	if err != nil {
		t.Fatalf("Ledger() error = %v", err)
	}
	if unpaidTotal != 2 {
		t.Fatalf("unpaid ledger total = %d, want 2", unpaidTotal)
	}

	page, pageTotal, err := svc.Ledger(context.Background(), BillingLedgerQuery{AccountID: "acct-4", Take: 1, Offset: 1})
	if err != nil {
		t.Fatalf("Ledger() error = %v", err)
	}
	if pageTotal != 3 || len(page) != 1 {
		t.Fatalf("paged ledger = %d of %d, want 1 of 3", len(page), pageTotal)
	}
	if page[0].Amount != "2.00000000" {
		t.Errorf("second page amount = %q, want 2.00000000", page[0].Amount)
	}
}

func TestTruncateRunesKeepsWholeRunes(t *testing.T) {
	if got := truncateRunes("abcdef", 3); got != "abc" {
		t.Errorf("truncateRunes(asdf) = %q, want abc", got)
	}
	if got := truncateRunes("héllo", 2); got != "hé" {
		t.Errorf("truncateRunes(héllo, 2) = %q, want hé", got)
	}
	if got := truncateRunes("short", 64); got != "short" {
		t.Errorf("truncateRunes(short, 64) = %q, want short", got)
	}
}
