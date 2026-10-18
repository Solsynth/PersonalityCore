package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
// engines' result lists point back at the article the same server serves, so
// the endpoint can be exercised end to end (query, read, index, fallback)
// without touching the network.
type searchStack struct {
	*httptest.Server

	mu            sync.Mutex
	failing       bool
	queries       []string
	exaQueries    []string
	deepseekCalls int
	deepseekQuery string
	fetchCalls    int
	articleHits   int
}

// fixturePageBody is the article text the search returns for /article. It is
// past the crawler's minimum for usable page text.
const fixturePageBody = "PostgreSQL 18 introduces an asynchronous I/O subsystem that changes how the server reads heap pages, index pages, and WAL. It also adds a new skip scan plan for multicolumn indexes with a low cardinality leading column, and reworks the wait event reporting that operators rely on during a rollout."

// fixtureMarkdown is what the provider's fetch endpoint returns for a page the
// search did not already hand over, also past that minimum.
const fixtureMarkdown = "A skip scan plan reads a multicolumn index without a constraint on the leading column. The planner picks it when the leading column has few distinct values and the remaining columns are selective, which turns an index that was previously unusable into one that answers the query without a sequential scan."

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
		case "/deepseek-provider/v1/tools/search":
			raw, _ := io.ReadAll(r.Body)
			stack.mu.Lock()
			stack.deepseekCalls++
			stack.deepseekQuery = string(raw)
			stack.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			// DeepSeek's search answers with the hits themselves: a display
			// snippet, the model-readable summary, and — because the engine asks
			// for it — the page content, so that hit needs no further read. The
			// second hit carries none, and its page is not fetchable directly:
			// only the provider's fetch endpoint can answer for it.
			fmt.Fprintf(w, `{"results":[
				{"title":"PostgreSQL 18 AIO","url":"%s/article",
				 "snippet":"Short display summary.",
				 "summary":"PostgreSQL 18 adds an asynchronous I/O subsystem that improves sequential scans and vacuum throughput.",
				 "content":"%s","site_name":"PostgreSQL","authority":4,"rerank_score":2.4},
				{"title":"Skip scan guide","url":"%s/docs",
				 "snippet":"A thin one-liner.","summary":"","content":"","site_name":"Example","authority":2,"rerank_score":1.7}
			]}`, stack.URL, fixturePageBody, stack.URL)
		case "/deepseek-provider/v1/tools/fetch":
			stack.mu.Lock()
			stack.fetchCalls++
			stack.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"title":"Skip scan guide","content":"`+fixtureMarkdown+`"}`)
		case "/article":
			stack.mu.Lock()
			stack.articleHits++
			stack.mu.Unlock()
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

// deepseekSearches reports how many search requests the provider saw and the
// body of the last one.
func (s *searchStack) deepseekSearches() (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deepseekCalls, s.deepseekQuery
}

func (s *searchStack) fetchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetchCalls
}

// directPageReads counts requests for the article page itself, which is how a
// test proves a page the engine already returned is not read a second time.
func (s *searchStack) directPageReads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.articleHits
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

// TestWebSearchEndpointRunsDeepseekSearch drives the API-backed engine through
// the public endpoint. DeepSeek answers with real text — a summary per hit and
// the page content of one — so the result is answerable from the search alone,
// and the page the search already read is never read a second time.
func TestWebSearchEndpointRunsDeepseekSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stack := newSearchStack(t)
	cfg := webSearchConfig(stack)
	// Shaped the way config.Load leaves it: the engine names a provider entry,
	// and resolution has already copied that provider's key and base URL in.
	cfg.Providers = []config.ProviderConfig{{
		ID: "deepseek", Type: "openai", APIKey: "sk-test", BaseURL: stack.URL + "/deepseek-provider",
	}}
	cfg.WebSearch.Engines = []config.WebSearchEngineConfig{{
		ID:       "deepseek",
		Type:     "deepseek",
		Provider: "deepseek",
		APIKey:   "sk-test",
		Price:    "0.1",
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
			Name    string          `json:"name"`
			Results int             `json:"results"`
			Error   string          `json:"error"`
			Usage   json.RawMessage `json:"usage"`
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

	// The search is billed per request, so the caller owes the engine's price
	// and nothing else: no model tokens are spent, and none are reported.
	if len(payload.Charges) != 1 || payload.Charges[0].Kind != "query" || payload.Charges[0].Amount != "0.1" {
		t.Fatalf("unexpected charges: %#v", payload.Charges)
	}
	if len(payload.Engines) != 1 || payload.Engines[0].Usage != nil {
		t.Fatalf("no engine reports a token spend any more: %s", response.Body.String())
	}
	if payload.Engines[0].Name != "deepseek" || payload.Engines[0].Results != 2 || payload.Engines[0].Error != "" {
		t.Fatalf("unexpected engine report: %#v", payload.Engines)
	}

	if len(payload.Results) != 2 {
		t.Fatalf("got %d results: %s", len(payload.Results), response.Body.String())
	}
	first := payload.Results[0]
	if first.Provider != "deepseek" || first.URL != stack.URL+"/article" {
		t.Fatalf("unexpected result: %#v", first)
	}
	// The provider's own page text supplies the snippet, without an HTTP GET
	// for the article.
	if !strings.Contains(first.Snippet, "asynchronous I/O subsystem") {
		t.Fatalf("snippet = %q, want text from the content the search returned", first.Snippet)
	}
	// The second hit carried no content, so its page was read through the
	// provider's fetch endpoint.
	second := payload.Results[1]
	if !strings.Contains(second.Snippet, "skip scan plan") {
		t.Fatalf("second snippet = %q, want text fetched through the provider", second.Snippet)
	}

	// Both pages reach the index: one from the search reply, one from the fetch.
	if payload.PagesCrawled != 2 {
		t.Fatalf("pages_crawled = %d, want 2", payload.PagesCrawled)
	}
	calls, body := stack.deepseekSearches()
	if calls != 1 {
		t.Fatalf("deepseek search was called %d times, want 1", calls)
	}
	if !strings.Contains(body, `"need_content":true`) {
		t.Fatalf("search request must ask for page content: %s", body)
	}
	if stack.fetchCount() != 1 {
		t.Fatalf("provider fetch was called %d times, want 1 for the one page without content", stack.fetchCount())
	}
	if hits := stack.directPageReads(); hits != 0 {
		t.Fatalf("the article was fetched directly %d times although the search returned its text", hits)
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

// The engine catalog and the account's preference are what let a user keep
// searches on the provider whose price it accepts: the catalog states each
// price, and a preference names one engine for this account alone.
func TestWebSearchPreferenceEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stack := newSearchStack(t)
	cfg := webSearchConfig(stack)
	cfg.WebSearch.Engines[0].Price = "1.5"
	router, _, _ := newWebSearchRouterWithService(t, cfg)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/web/search/engines", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("engines status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var catalog struct {
		Currency string `json:"currency"`
		Engines  []struct {
			ID    string `json:"id"`
			Price string `json:"price"`
			Free  bool   `json:"free"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if catalog.Currency != "golds" {
		t.Errorf("currency = %q, want golds", catalog.Currency)
	}
	if len(catalog.Engines) != 1 || catalog.Engines[0].ID != "stack" {
		t.Fatalf("engines = %+v, want the configured stack engine", catalog.Engines)
	}
	if catalog.Engines[0].Price != "1.5" || catalog.Engines[0].Free {
		t.Fatalf("stack = %+v, want a priced engine", catalog.Engines[0])
	}

	preference := func() string {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/web/search/preference", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("preference status = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		var payload struct {
			Engine string `json:"engine"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode preference: %v", err)
		}
		return payload.Engine
	}
	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPut, "/api/web/search/preference", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	if engine := preference(); engine != "" {
		t.Fatalf("engine = %q, want empty before the account chooses", engine)
	}
	if recorder := put(`{"engine":"nope"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown engine status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := put(`{"engine":"stack"}`); recorder.Code != http.StatusOK {
		t.Fatalf("choose status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if engine := preference(); engine != "stack" {
		t.Fatalf("engine = %q, want the chosen stack", engine)
	}
	if recorder := put(`{"engine":""}`); recorder.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if engine := preference(); engine != "" {
		t.Fatalf("engine = %q, want empty after clearing", engine)
	}
}
