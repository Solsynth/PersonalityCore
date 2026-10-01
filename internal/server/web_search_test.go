package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/persona/internal/agent"
	"src.solsynth.dev/sosys/persona/internal/config"
	"src.solsynth.dev/sosys/persona/internal/database"
	"src.solsynth.dev/sosys/persona/internal/service"
)

// searchStack is a fake search engine plus a page host on one origin: the
// engine's result list points back at its own article, so the endpoint can be
// exercised end to end (query, crawl, index, fallback) without touching the
// network.
type searchStack struct {
	*httptest.Server

	mu         sync.Mutex
	failing    bool
	queries    []string
	exaQueries []string
	deepseek   int
}

func newSearchStack(t *testing.T) *searchStack {
	t.Helper()
	stack := &searchStack{}
	stack.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/search":
			stack.mu.Lock()
			stack.queries = append(stack.queries, r.URL.Query().Get("q"))
			failing := stack.failing
			stack.mu.Unlock()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if failing {
				fmt.Fprint(w, `<html><body><div class="anomaly-modal">Unfortunately, bots use DuckDuckGo too.
					<form action="/anomaly.js"><p>Please complete the following challenge to confirm this search was made by a human.</p></form></div></body></html>`)
				return
			}
			fmt.Fprintf(w, `<html><body><ol id="b_results"><li class="b_algo">
				<h2><a href="%s/article">Upgrading to PostgreSQL 18</a></h2>
				<div class="b_caption"><p>%s</p></div></li></ol></body></html>`, stack.URL, "Short.")
		case "/exa-search":
			stack.mu.Lock()
			stack.exaQueries = append(stack.exaQueries, r.URL.Path)
			stack.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"results":[{"title":"PostgreSQL 18 AIO","url":"%s/article","text":"An asynchronous I/O subsystem that improves sequential scans."}]}`, stack.URL)
		case "/deepseek-provider/anthropic/v1/messages":
			stack.mu.Lock()
			stack.deepseek++
			stack.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			// DeepSeek returns the search hits themselves: a title and a URL,
			// with no snippet text, which is what the local index is for. It
			// also reports what the query spent, which the caller is billed for.
			fmt.Fprintf(w, `{"content":[{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[
				{"type":"web_search_result","url":"%s/article","title":"PostgreSQL 18 AIO"}]}],
				"stop_reason":"end_turn",
				"usage":{"input_tokens":3000,"output_tokens":500,"server_tool_use":{"web_search_requests":1}}}`, stack.URL)
		case "/article":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<html><head><title>Upgrading to PostgreSQL 18</title></head><body>
				<nav><a href="/">Home</a></nav>
				<article><p>PostgreSQL 18 introduces an asynchronous I/O subsystem that changes how the server reads heap pages.</p>
				<p>It also adds a new skip scan plan for multicolumn indexes with a low cardinality leading column.</p>
				<p>Operators upgrading from 17 should re-run ANALYZE and recheck autovacuum thresholds, because the planner's cost model changed.</p></article>
				<footer>Copyright notice</footer></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(stack.Close)
	return stack
}

func (s *searchStack) setFailing(failing bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = failing
}

func (s *searchStack) exaQueryCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.exaQueries)
}

func (s *searchStack) searchedQueries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

func newWebSearchRouter(t *testing.T, cfg *config.Config) *gin.Engine {
	t.Helper()
	router, _, _ := newWebSearchRouterWithService(t, cfg)
	return router
}

func newWebSearchRouterWithService(t *testing.T, cfg *config.Config) (*gin.Engine, *service.ConversationService, *database.DB) {
	t.Helper()
	registry, err := agent.NewRegistry(nil)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	raw, err := gorm.Open(sqlite.Open("file:"+ulid.Make().String()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db := &database.DB{DB: raw}
	// Production migrates in app.New; the index needs its table to exist.
	if err := db.AutoMigrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	conversations := service.NewConversationService(db, cfg, registry, nil)
	return NewRouter(cfg, conversations), conversations, db
}

type testWalletChecker struct{ exists bool }

func (c testWalletChecker) CheckWalletExists(context.Context, string) (bool, error) {
	return c.exists, nil
}

func webSearchConfig(stack *searchStack) *config.Config {
	crawlEnabled := true
	engineEnabled := true
	return &config.Config{
		Auth: config.AuthConfig{Offline: true, OfflineAccountID: "local-dev"},
		WebSearch: config.WebSearchConfig{
			Enabled:      true,
			DefaultLimit: 5,
			MaxLimit:     10,
			CacheTTL:     0,
			Engines: []config.WebSearchEngineConfig{{
				ID:      "stack",
				Type:    "bing",
				BaseURL: stack.URL,
				Enabled: &engineEnabled,
				Timeout: 0,
			}},
			Crawl: config.WebSearchCrawlConfig{
				Enabled:          crawlEnabled,
				MaxPagesPerQuery: 2,
				PageTimeout:      0,
				PerHostDelay:     0,
				MaxPageBytes:     1 << 20,
			},
		},
	}
}

func postSearch(t *testing.T, router *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/web/search", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestWebSearchEndpointCrawlsAndIndexesResults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stack := newSearchStack(t)
	router := newWebSearchRouter(t, webSearchConfig(stack))

	response := postSearch(t, router, `{"query":"postgres 18 features","limit":3}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	var payload struct {
		Query   string `json:"query"`
		Results []struct {
			Title    string `json:"title"`
			URL      string `json:"url"`
			Snippet  string `json:"snippet"`
			Provider string `json:"provider"`
		} `json:"results"`
		Engines []struct {
			Name    string `json:"name"`
			Results int    `json:"results"`
			Error   string `json:"error"`
		} `json:"engines"`
		Cached       bool `json:"cached"`
		Fallback     bool `json:"fallback"`
		PagesCrawled int  `json:"pages_crawled"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v (%s)", err, response.Body.String())
	}

	if payload.Query != "postgres 18 features" {
		t.Fatalf("query = %q", payload.Query)
	}
	if len(payload.Results) != 1 {
		t.Fatalf("got %d results: %s", len(payload.Results), response.Body.String())
	}
	result := payload.Results[0]
	if result.URL != stack.URL+"/article" || result.Provider != "stack" {
		t.Fatalf("unexpected result: %#v", result)
	}
	// The engine returned a one-word snippet; the crawler must replace it with
	// text read from the page.
	if !strings.Contains(result.Snippet, "asynchronous I/O subsystem") {
		t.Fatalf("snippet was not enriched from the crawled page: %q", result.Snippet)
	}
	if payload.PagesCrawled != 1 {
		t.Fatalf("pages_crawled = %d, want 1", payload.PagesCrawled)
	}
	if payload.Cached || payload.Fallback {
		t.Fatalf("a live search must be neither cached nor a fallback: %#v", payload)
	}
	if len(payload.Engines) != 1 || payload.Engines[0].Name != "stack" || payload.Engines[0].Results != 1 {
		t.Fatalf("unexpected engine report: %#v", payload.Engines)
	}
	if queries := stack.searchedQueries(); len(queries) != 1 || queries[0] != "postgres 18 features" {
		t.Fatalf("engine received %#v", queries)
	}
}

// TestWebSearchEndpointRunsDeepseekSearch drives the LLM-backed engine through
// the public endpoint. DeepSeek answers with the hits themselves and no snippet
// text, so the crawler is what makes the result answerable.
func TestWebSearchEndpointRunsDeepseekSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stack := newSearchStack(t)
	cfg := webSearchConfig(stack)
	// Shaped the way config.Load leaves it: the engine names a provider entry,
	// and resolution has already copied that provider's key and base URL in.
	inputPrice, outputPrice := "0.15", "0.6"
	cfg.Providers = []config.ProviderConfig{{
		ID: "deepseek", Type: "openai", APIKey: "sk-test", BaseURL: stack.URL + "/deepseek-provider",
		Models: []config.ModelConfig{{
			Name:    "deepseek-flash",
			Pricing: &config.ModelPricingConfig{Input: &inputPrice, Output: &outputPrice},
		}},
	}}
	// The metered tokens are priced with the same service fee a generation
	// would carry.
	cfg.Billing = config.BillingConfig{ServiceFeePercentage: "0"}
	cfg.WebSearch.Engines = []config.WebSearchEngineConfig{{
		ID:       "deepseek",
		Type:     "deepseek",
		Provider: "deepseek",
		APIKey:   "sk-test",
		Price:    "2",
		BaseURL:  stack.URL + "/deepseek-provider",
	}}
	router := newWebSearchRouter(t, cfg)

	response := postSearch(t, router, `{"query":"postgres 18 async io","limit":3}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	var payload struct {
		Results []struct {
			Title    string `json:"title"`
			URL      string `json:"url"`
			Snippet  string `json:"snippet"`
			Provider string `json:"provider"`
		} `json:"results"`
		Engines []struct {
			Name    string `json:"name"`
			Results int    `json:"results"`
			Error   string `json:"error"`
			Usage   *struct {
				Provider     string `json:"provider"`
				Model        string `json:"model"`
				InputTokens  int    `json:"input_tokens"`
				OutputTokens int    `json:"output_tokens"`
				Searches     int    `json:"searches"`
			} `json:"usage"`
		} `json:"engines"`
		Charges []struct {
			Engine string `json:"engine"`
			Kind   string `json:"kind"`
			Amount string `json:"amount"`
		} `json:"charges"`
		PagesCrawled int `json:"pages_crawled"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v (%s)", err, response.Body.String())
	}

	// The search runs on the caller's DeepSeek account, so what it spent is
	// reported and charged: 3000 input at 0.15 per million and 500 output at
	// 0.6 per million.
	if len(payload.Engines) != 1 || payload.Engines[0].Usage == nil {
		t.Fatalf("the provider spend must be reported: %s", response.Body.String())
	}
	usage := payload.Engines[0].Usage
	if usage.Provider != "deepseek" || usage.Model != "deepseek-flash" || usage.InputTokens != 3000 || usage.OutputTokens != 500 || usage.Searches != 1 {
		t.Fatalf("unexpected usage: %#v", usage)
	}
	// The engine's configured price and the tokens it spent are two charges.
	if len(payload.Charges) != 2 {
		t.Fatalf("unexpected charges: %#v", payload.Charges)
	}
	if payload.Charges[0].Kind != "query" || payload.Charges[0].Amount != "2" {
		t.Fatalf("unexpected per-query charge: %#v", payload.Charges[0])
	}
	last := payload.Charges[1]
	if last.Engine != "deepseek" || last.Kind != "usage" || last.Amount != "0.00075000" {
		t.Fatalf("unexpected usage charge: %#v", last)
	}
	if len(payload.Results) != 1 {
		t.Fatalf("got %d results: %s", len(payload.Results), response.Body.String())
	}
	result := payload.Results[0]
	if result.Provider != "deepseek" || result.URL != stack.URL+"/article" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !strings.Contains(result.Snippet, "asynchronous I/O subsystem") {
		t.Fatalf("the crawled page must supply the snippet: %q", result.Snippet)
	}
	if payload.PagesCrawled != 1 {
		t.Fatalf("pages_crawled = %d, want 1", payload.PagesCrawled)
	}
	if len(payload.Engines) != 1 || payload.Engines[0].Name != "deepseek" || payload.Engines[0].Results != 1 || payload.Engines[0].Error != "" {
		t.Fatalf("unexpected engine report: %#v", payload.Engines)
	}

	stack.mu.Lock()
	defer stack.mu.Unlock()
	if stack.deepseek != 1 {
		t.Fatalf("deepseek endpoint was called %d times, want 1", stack.deepseek)
	}
}

func TestWebSearchEndpointServesIndexWhenEnginesBlock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stack := newSearchStack(t)
	router := newWebSearchRouter(t, webSearchConfig(stack))

	// First query crawls the article into the local index.
	if response := postSearch(t, router, `{"query":"postgres asynchronous io"}`); response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	// The engine then starts serving a bot wall for a different query, so the
	// only remaining source is the pages crawled earlier.
	stack.setFailing(true)
	response := postSearch(t, router, `{"query":"asynchronous io postgres","limit":3}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	var payload struct {
		Results []struct {
			URL      string `json:"url"`
			Snippet  string `json:"snippet"`
			Provider string `json:"provider"`
		} `json:"results"`
		Engines []struct {
			Error string `json:"error"`
		} `json:"engines"`
		Fallback bool `json:"fallback"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v (%s)", err, response.Body.String())
	}
	if !payload.Fallback {
		t.Fatalf("expected an index fallback response: %s", response.Body.String())
	}
	if len(payload.Results) != 1 || payload.Results[0].Provider != "index" || payload.Results[0].URL != stack.URL+"/article" {
		t.Fatalf("unexpected fallback results: %#v", payload.Results)
	}
	if !strings.Contains(payload.Results[0].Snippet, "asynchronous I/O subsystem") {
		t.Fatalf("fallback snippet = %q", payload.Results[0].Snippet)
	}
	if len(payload.Engines) != 1 || !strings.Contains(payload.Engines[0].Error, "bot wall") {
		t.Fatalf("engine failure must still be reported: %#v", payload.Engines)
	}
}

func TestWebSearchEndpointRejectsEmptyQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stack := newSearchStack(t)
	router := newWebSearchRouter(t, webSearchConfig(stack))

	response := postSearch(t, router, `{"query":"   "}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
	if len(stack.searchedQueries()) != 0 {
		t.Fatal("an empty query must not reach the engines")
	}
}

func TestWebSearchEndpointReportsUnconfiguredFeature(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Auth: config.AuthConfig{Offline: true, OfflineAccountID: "local-dev"}}
	router := newWebSearchRouter(t, cfg)

	response := postSearch(t, router, `{"query":"postgres 18"}`)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", response.Code, response.Body.String())
	}
}

func TestWebSearchEndpointReportsEngineFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stack := newSearchStack(t)
	stack.setFailing(true)
	cfg := webSearchConfig(stack)
	cfg.WebSearch.Crawl.Enabled = false
	router := newWebSearchRouter(t, cfg)

	response := postSearch(t, router, `{"query":"postgres 18"}`)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "bot wall") {
		t.Fatalf("body = %s", response.Body.String())
	}
}

// TestWebSearchFixtureStillMatchesParser guards against the engine fixture
// drifting away from what the live capture contained.
func TestWebSearchFixtureStillMatchesParser(t *testing.T) {
	fixture := filepath.Join("..", "websearch", "testdata", "bing.html")
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	for _, marker := range []string{`class="b_algo"`, "<h2", `class="b_caption"`} {
		if !strings.Contains(string(body), marker) {
			t.Fatalf("fixture %s no longer contains %q", fixture, marker)
		}
	}
}

// billedWebSearchConfig charges golds for one API-backed engine.
func billedWebSearchConfig(stack *searchStack) *config.Config {
	cfg := &config.Config{
		Auth:    config.AuthConfig{Offline: true, OfflineAccountID: "local-dev"},
		Billing: config.BillingConfig{Enabled: true, Currency: "golds"},
		WebSearch: config.WebSearchConfig{
			Enabled: true, DefaultLimit: 5, MaxLimit: 10, CacheTTL: 0,
			Engines: []config.WebSearchEngineConfig{{
				ID: "exa", Type: "exa", APIKey: "exa-key", Price: "1.5",
				BaseURL: stack.URL + "/exa-search",
			}},
		},
	}
	return cfg
}

func TestWebSearchEndpointChargesGoldsForPaidEngine(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stack := newSearchStack(t)
	router, conversations, db := newWebSearchRouterWithService(t, billedWebSearchConfig(stack))
	conversations.Billing().SetWalletChecker(testWalletChecker{exists: true})

	response := postSearch(t, router, `{"query":"postgres 18 asynchronous io"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	var payload struct {
		Charges []struct {
			Engine string `json:"engine"`
			Amount string `json:"amount"`
		} `json:"charges"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Charges) != 1 || payload.Charges[0].Engine != "exa" || payload.Charges[0].Amount != "1.5" {
		t.Fatalf("response charges = %#v, want the exa price", payload.Charges)
	}

	var rows []database.BillingUsage
	if err := db.Where("model = ?", "web_search/exa").Find(&rows).Error; err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d ledger rows, want 1: %#v", len(rows), rows)
	}
	if rows[0].AccountID != "local-dev" || rows[0].Currency != "golds" || rows[0].Amount != "1.50000000" {
		t.Fatalf("unexpected charge: %#v", rows[0])
	}
}

func TestWebSearchEndpointRefusesBilledEngineWithoutPaymentWallet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stack := newSearchStack(t)
	router, conversations, db := newWebSearchRouterWithService(t, billedWebSearchConfig(stack))
	conversations.Billing().SetWalletChecker(testWalletChecker{exists: false})

	response := postSearch(t, router, `{"query":"postgres 18 asynchronous io"}`)
	if response.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402: %s", response.Code, response.Body.String())
	}
	if stack.exaQueryCount() != 0 {
		t.Fatal("a search the caller cannot pay for must not reach the paid engine")
	}
	var rows []database.BillingUsage
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a refused search must not be charged: %#v", rows)
	}
}
