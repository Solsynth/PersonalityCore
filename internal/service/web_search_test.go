package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"src.solsynth.dev/sosys/persona/internal/agent"
	"src.solsynth.dev/sosys/persona/internal/config"
	"src.solsynth.dev/sosys/persona/internal/database"
	"src.solsynth.dev/sosys/persona/internal/websearch"
)

type stubWebSearchEngine struct {
	response  *websearch.Response
	err       error
	billed    bool
	metered   bool
	lastQuery websearch.Query
}

func (e *stubWebSearchEngine) Search(_ context.Context, query websearch.Query) (*websearch.Response, error) {
	e.lastQuery = query
	if e.err != nil {
		return nil, e.err
	}
	return e.response, nil
}

func (e *stubWebSearchEngine) Billed() bool { return e.billed }

func (e *stubWebSearchEngine) Metered() bool { return e.metered }

func webSearchCall(arguments string) schema.ToolCall {
	return schema.ToolCall{
		ID: "call-search",
		Function: schema.FunctionCall{
			Name:      webSearchToolName,
			Arguments: arguments,
		},
	}
}

func toolNamesOf(tools []*schema.ToolInfo) map[string]int {
	names := make(map[string]int, len(tools))
	for _, tool := range tools {
		names[tool.Name]++
	}
	return names
}

func TestSearchWebRequiresConfiguredEngine(t *testing.T) {
	svc := newTestConversationService(t)

	_, err := svc.SearchWeb(context.Background(), "acct-1", WebSearchInput{Query: "postgres 18"})
	if !errors.Is(err, websearch.ErrNotConfigured) {
		t.Fatalf("error = %v, want ErrNotConfigured", err)
	}
}

func TestSearchWebForwardsRequestFields(t *testing.T) {
	engine := &stubWebSearchEngine{response: &websearch.Response{Query: "postgres 18"}}
	svc := &ConversationService{cfg: &config.Config{}, webSearch: engine}

	if _, err := svc.SearchWeb(context.Background(), "acct-1", WebSearchInput{
		Query:     "  postgres 18  ",
		Limit:     4,
		Freshness: "Week",
		Domains:   []string{"postgresql.org"},
		Language:  "en",
	}); err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}

	got := engine.lastQuery
	if got.Text != "postgres 18" || got.Limit != 4 || got.Freshness != websearch.FreshnessWeek {
		t.Fatalf("unexpected query: %#v", got)
	}
	if len(got.Domains) != 1 || got.Domains[0] != "postgresql.org" || got.Language != "en" {
		t.Fatalf("unexpected query: %#v", got)
	}
}

func TestBuildToolInfosGatesWebSearchOnAbility(t *testing.T) {
	newService := func(dynamicSkills bool) *ConversationService {
		return &ConversationService{cfg: &config.Config{
			Personality: config.PersonalityConfig{DynamicSkills: dynamicSkills},
		}}
	}
	capable := agent.Definition{ID: "researcher", Abilities: []string{"web_search"}}
	plain := agent.Definition{ID: "plain"}

	for _, dynamic := range []bool{true, false} {
		svc := newService(dynamic)

		names := toolNamesOf(svc.buildToolInfos(capable, nil, 0))
		if names[webSearchToolName] != 1 {
			t.Fatalf("dynamicSkills=%v: capable agent has web_search %d times, want once", dynamic, names[webSearchToolName])
		}
		if names := toolNamesOf(svc.buildToolInfos(plain, nil, 0)); names[webSearchToolName] != 0 {
			t.Fatalf("dynamicSkills=%v: agent without the ability must not get web_search", dynamic)
		}
	}
}

func TestWebSearchSkillIsNotAdvertised(t *testing.T) {
	svc := &ConversationService{cfg: &config.Config{}}

	// An agent without the ability never sees the skill: it cannot activate it.
	plain := svc.executeListSkillsToolCall(agent.Definition{ID: "plain"}, map[string]bool{}, 0, nil, nil)
	if strings.Contains(plain.Content, `"web_search"`) {
		t.Fatalf("agent without the ability was offered the skill: %s", plain.Content)
	}

	// An agent with the ability already holds the tool, so the skill stays out
	// of the list as well: activating it would add a duplicate tool name.
	capable := agent.Definition{ID: "researcher", Abilities: []string{"web_search"}}
	listed := svc.executeListSkillsToolCall(capable, map[string]bool{}, 0, nil, nil)
	if strings.Contains(listed.Content, `"web_search"`) {
		t.Fatalf("an already-loaded skill must not be advertised: %s", listed.Content)
	}
	if names := toolNamesOf(svc.buildToolInfos(capable, nil, 0)); names[webSearchToolName] != 1 {
		t.Fatalf("the tool must already be loaded for the capable agent, got %d copies", names[webSearchToolName])
	}
}

func TestActivateSkillRefusesWebSearchWithoutAbility(t *testing.T) {
	svc := &ConversationService{cfg: &config.Config{}}
	call := schema.ToolCall{
		ID:       "call-activate",
		Function: schema.FunctionCall{Name: "activate_skill", Arguments: `{"skill":"web_search"}`},
	}

	active := map[string]bool{}
	result, _ := svc.executeActivateSkillToolCall(call, active, agent.Definition{ID: "plain"}, nil)
	if !strings.Contains(result.Content, "skill not available") {
		t.Fatalf("activation must be refused: %s", result.Content)
	}
	if active["web_search"] {
		t.Fatal("a refused skill must not be marked active")
	}

	capable, _ := svc.executeActivateSkillToolCall(call, active, agent.Definition{ID: "researcher", Abilities: []string{"web_search"}}, nil)
	if !strings.Contains(capable.Content, `"ok":true`) || !active["web_search"] {
		t.Fatalf("capable agent must be able to activate the skill: %s", capable.Content)
	}
}

func TestExecuteWebSearchToolCallReturnsResults(t *testing.T) {
	engine := &stubWebSearchEngine{response: &websearch.Response{
		Query: "postgres 18",
		Results: []websearch.Result{{
			Title:    "PostgreSQL 18",
			URL:      "https://www.postgresql.org/docs/current/release-18.html",
			Snippet:  "Release notes",
			Provider: "bing",
		}},
	}}
	svc := &ConversationService{cfg: &config.Config{}, webSearch: engine}

	result, err := svc.executeWebSearchToolCall(context.Background(), "acct-1", webSearchCall(
		`{"query":"postgres 18","limit":3,"freshness":"month","domains":["postgresql.org"]}`,
	))
	if err != nil {
		t.Fatalf("executeWebSearchToolCall() error = %v", err)
	}
	if result.ToolName != webSearchToolName || result.ToolCallID != "call-search" {
		t.Fatalf("unexpected tool result identity: %#v", result)
	}

	var payload struct {
		OK      bool               `json:"ok"`
		Query   string             `json:"query"`
		Results []websearch.Result `json:"results"`
	}
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("decode tool output: %v", err)
	}
	if !payload.OK || payload.Query != "postgres 18" || len(payload.Results) != 1 {
		t.Fatalf("unexpected tool output: %s", result.Content)
	}
	if payload.Results[0].URL != "https://www.postgresql.org/docs/current/release-18.html" {
		t.Fatalf("unexpected result: %#v", payload.Results[0])
	}
	if engine.lastQuery.Freshness != websearch.FreshnessMonth || engine.lastQuery.Limit != 3 {
		t.Fatalf("tool arguments were not forwarded: %#v", engine.lastQuery)
	}
}

func TestExecuteWebSearchToolCallReportsFailureToTheModel(t *testing.T) {
	engine := &stubWebSearchEngine{err: errors.New("all web search engines failed: bing: engine returned 503")}
	svc := &ConversationService{cfg: &config.Config{}, webSearch: engine}

	result, err := svc.executeWebSearchToolCall(context.Background(), "acct-1", webSearchCall(`{"query":"postgres 18"}`))
	if err != nil {
		t.Fatalf("a failed search must not abort the run: %v", err)
	}
	if !strings.Contains(result.Content, `"ok":false`) || !strings.Contains(result.Content, "503") {
		t.Fatalf("unexpected tool output: %s", result.Content)
	}
}

func TestExecuteWebSearchToolCallRejectsBadArguments(t *testing.T) {
	svc := &ConversationService{cfg: &config.Config{}, webSearch: &stubWebSearchEngine{}}

	result, err := svc.executeWebSearchToolCall(context.Background(), "acct-1", webSearchCall(`{"query":`))
	if err != nil {
		t.Fatalf("executeWebSearchToolCall() error = %v", err)
	}
	if !strings.Contains(result.Content, `"ok":false`) {
		t.Fatalf("unexpected tool output: %s", result.Content)
	}
}

func TestIsWebSearchToolName(t *testing.T) {
	if !isWebSearchToolName(webSearchToolName) {
		t.Fatal("web_search must dispatch to the web search tool")
	}
	if isWebSearchToolName("read_webpage") || isWebSearchToolName("search_accounts") {
		t.Fatal("unrelated tools must not dispatch to web search")
	}
}

// newBilledTestService is a service with billing enabled in golds so charges can
// be inspected on the ledger.
func newBilledTestService(t *testing.T, engine WebSearchEngine) (*ConversationService, *database.DB) {
	t.Helper()
	return newBilledTestServiceWithProviders(t, engine, nil)
}

// newBilledTestServiceWithProviders adds the model providers a metered search
// engine prices its tokens against.
func newBilledTestServiceWithProviders(t *testing.T, engine WebSearchEngine, providers []config.ProviderConfig) (*ConversationService, *database.DB) {
	t.Helper()
	registry, err := agent.NewRegistry(nil)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	db := openTestDB(t)
	cfg := &config.Config{
		Billing:   config.BillingConfig{Enabled: true, Currency: "golds", ServiceFeePercentage: "0"},
		Providers: providers,
	}
	svc := NewConversationService(db, cfg, registry, nil)
	svc.billing.SetWalletChecker(openAICompatibleTestWalletChecker{})
	svc.webSearch = engine
	return svc, db
}

// deepseekProvider prices the model the deepseek search engine calls, which is
// what makes its tokens billable.
func deepseekProvider(input, output string) []config.ProviderConfig {
	return []config.ProviderConfig{{
		ID: "deepseek", Type: "openai", APIKey: "sk-test", BaseURL: "https://api.deepseek.com",
		Models: []config.ModelConfig{{
			Name:    "deepseek-flash",
			Pricing: &config.ModelPricingConfig{Input: &input, Output: &output},
		}},
	}}
}

func TestSearchWebBillsMeteredEngineTokens(t *testing.T) {
	engine := &stubWebSearchEngine{
		// A metered engine needs no per-query price: the tokens are the charge.
		metered: true,
		response: &websearch.Response{
			Query: "postgres 18",
			Engines: []websearch.EngineReport{{
				Name:    "deepseek",
				Results: 3,
				Usage: &websearch.Usage{
					Provider: "deepseek", Model: "deepseek-flash",
					InputTokens: 3_000, OutputTokens: 500, Searches: 1,
				},
			}},
		},
	}
	svc, db := newBilledTestServiceWithProviders(t, engine, deepseekProvider("0.15", "0.6"))

	response, err := svc.SearchWeb(context.Background(), "acct-1", WebSearchInput{Query: "postgres 18"})
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}

	// 3000 input at 0.15 per million plus 500 output at 0.6 per million.
	rows := ledgerRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("got %d ledger rows, want 1: %#v", len(rows), rows)
	}
	row := rows[0]
	if row.Model != "web_search/deepseek/tokens" || row.AccountID != "acct-1" {
		t.Fatalf("unexpected ledger row: %#v", row)
	}
	if row.Amount != "0.00075000" || row.Currency != "golds" {
		t.Fatalf("unexpected charge: %#v", row)
	}
	if row.InputTokens != 3_000 || row.OutputTokens != 500 {
		t.Fatalf("the ledger row must keep the counts it was charged for: %#v", row)
	}
	if row.RunID != nil {
		t.Fatalf("an action charge is not a run and must not claim a run id: %#v", row.RunID)
	}
	if len(response.Charges) != 1 || response.Charges[0].Kind != websearch.ChargeKindUsage {
		t.Fatalf("the response must report the token charge: %#v", response.Charges)
	}
	if response.Charges[0].Engine != "deepseek" || response.Charges[0].Amount != "0.00075000" {
		t.Fatalf("unexpected reported charge: %#v", response.Charges[0])
	}
}

func TestSearchWebChargesNothingForAnUnpricedMeteredModel(t *testing.T) {
	engine := &stubWebSearchEngine{
		metered:  true,
		response: &websearch.Response{Engines: []websearch.EngineReport{{Name: "deepseek", Usage: &websearch.Usage{Provider: "deepseek", Model: "deepseek-flash", InputTokens: 3_000}}}},
	}
	svc, db := newBilledTestService(t, engine)

	response, err := svc.SearchWeb(context.Background(), "acct-1", WebSearchInput{Query: "postgres 18"})
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	if rows := ledgerRows(t, db); len(rows) != 0 {
		t.Fatalf("a model without pricing must not be charged: %#v", rows)
	}
	if len(response.Charges) != 0 {
		t.Fatalf("charges = %#v, want none", response.Charges)
	}
}

func TestSearchWebRequiresPaymentWalletForMeteredEngine(t *testing.T) {
	engine := &stubWebSearchEngine{
		metered:  true,
		response: &websearch.Response{Engines: []websearch.EngineReport{{Name: "deepseek", Usage: &websearch.Usage{Provider: "deepseek", Model: "deepseek-flash", InputTokens: 3_000}}}},
	}
	svc, db := newBilledTestServiceWithProviders(t, engine, deepseekProvider("0.15", "0.6"))
	// Tokens cost golds even though no engine states a per-query price, so the
	// caller has to be able to pay before the search runs.
	svc.billing.SetWalletChecker(fakeWalletChecker{exists: false})

	_, err := svc.SearchWeb(context.Background(), "acct-1", WebSearchInput{Query: "postgres 18"})
	if !errors.Is(err, ErrPaymentWalletRequired) {
		t.Fatalf("error = %v, want ErrPaymentWalletRequired", err)
	}
	if engine.lastQuery.Text != "" {
		t.Fatal("a search the caller cannot pay for must not reach the engine")
	}
	var rows []database.BillingUsage
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a refused search must not be charged: %#v", rows)
	}
}

func ledgerRows(t *testing.T, db *database.DB) []database.BillingUsage {
	t.Helper()
	var rows []database.BillingUsage
	if err := db.Where("model LIKE ?", "web_search/%").Find(&rows).Error; err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	return rows
}

func TestSearchWebChargesGoldsForPaidEngine(t *testing.T) {
	engine := &stubWebSearchEngine{
		billed:   true,
		response: &websearch.Response{Query: "postgres 18", Charges: []websearch.Charge{{Engine: "exa", Amount: "1.5"}}},
	}
	svc, db := newBilledTestService(t, engine)

	if _, err := svc.SearchWeb(context.Background(), "acct-1", WebSearchInput{Query: "postgres 18"}); err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}

	rows := ledgerRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("got %d ledger rows, want 1: %#v", len(rows), rows)
	}
	row := rows[0]
	if row.AccountID != "acct-1" || row.Model != "web_search/exa" {
		t.Fatalf("unexpected ledger row: %#v", row)
	}
	if row.Currency != "golds" || row.Amount != "1.50000000" || row.OriginalAmount != "1.50000000" {
		t.Fatalf("unexpected charge: %#v", row)
	}
	if row.RunID != nil {
		t.Fatalf("an action charge is not a run and must not claim a run id: %#v", row.RunID)
	}
}

func TestSearchWebChargesEveryBilledEngine(t *testing.T) {
	engine := &stubWebSearchEngine{
		billed: true,
		response: &websearch.Response{Charges: []websearch.Charge{
			{Engine: "exa", Amount: "1.5"},
			{Engine: "tavily", Amount: "0.5"},
		}},
	}
	svc, db := newBilledTestService(t, engine)

	if _, err := svc.SearchWeb(context.Background(), "acct-1", WebSearchInput{Query: "postgres 18"}); err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	rows := ledgerRows(t, db)
	if len(rows) != 2 {
		t.Fatalf("got %d ledger rows, want 2: %#v", len(rows), rows)
	}
}

func TestSearchWebDoesNotChargeFreeOrCachedAnswers(t *testing.T) {
	scraped := &stubWebSearchEngine{response: &websearch.Response{Query: "postgres 18"}}
	svc, db := newBilledTestService(t, scraped)

	if _, err := svc.SearchWeb(context.Background(), "acct-1", WebSearchInput{Query: "postgres 18"}); err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	if rows := ledgerRows(t, db); len(rows) != 0 {
		t.Fatalf("a scraped search must stay free, got %#v", rows)
	}

	// A cache hit made no upstream call, so it carries no charges to record.
	cached := &stubWebSearchEngine{
		billed:   true,
		response: &websearch.Response{Query: "postgres 18", Cached: true},
	}
	svc.webSearch = cached
	if _, err := svc.SearchWeb(context.Background(), "acct-1", WebSearchInput{Query: "postgres 18"}); err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	if rows := ledgerRows(t, db); len(rows) != 0 {
		t.Fatalf("a cached search must stay free, got %#v", rows)
	}
}

func TestSearchWebRefusesBilledSearchForBlacklistedAccount(t *testing.T) {
	engine := &stubWebSearchEngine{billed: true, response: &websearch.Response{}}
	svc, db := newBilledTestService(t, engine)
	if err := svc.billing.Blacklist(context.Background(), "acct-1", "unpaid"); err != nil {
		t.Fatalf("Blacklist() error = %v", err)
	}

	_, err := svc.SearchWeb(context.Background(), "acct-1", WebSearchInput{Query: "postgres 18"})
	if !errors.Is(err, ErrBillingBlacklisted) {
		t.Fatalf("error = %v, want ErrBillingBlacklisted", err)
	}
	if engine.lastQuery.Text != "" {
		t.Fatal("a refused search must not reach the engine")
	}
	if rows := ledgerRows(t, db); len(rows) != 0 {
		t.Fatalf("a refused search must not be charged, got %#v", rows)
	}
}

func TestWebSearchToolCallChargesTheCaller(t *testing.T) {
	engine := &stubWebSearchEngine{
		billed:   true,
		response: &websearch.Response{Query: "postgres 18", Charges: []websearch.Charge{{Engine: "exa", Amount: "2"}}},
	}
	svc, db := newBilledTestService(t, engine)

	result, err := svc.executeWebSearchToolCall(context.Background(), "acct-7", webSearchCall(`{"query":"postgres 18"}`))
	if err != nil {
		t.Fatalf("executeWebSearchToolCall() error = %v", err)
	}
	if !strings.Contains(result.Content, `"ok":true`) {
		t.Fatalf("unexpected tool output: %s", result.Content)
	}
	rows := ledgerRows(t, db)
	if len(rows) != 1 || rows[0].AccountID != "acct-7" || rows[0].Amount != "2.00000000" {
		t.Fatalf("unexpected ledger rows: %#v", rows)
	}
}
