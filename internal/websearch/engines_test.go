package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"src.solsynth.dev/sosys/persona/internal/config"
)

// fixtureServer serves one testdata file and records every request it receives,
// so tests can assert the request shape an engine builds as well as its parsing.
type fixtureServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
}

func serveFixture(t *testing.T, file string) *fixtureServer {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	fixture := &fixtureServer{}
	fixture.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		fixture.requests = append(fixture.requests, r.Clone(context.Background()))
		fixture.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(body)
	}))
	t.Cleanup(fixture.Close)
	return fixture
}

func (f *fixtureServer) lastRequest(t *testing.T) *http.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("engine did not request the fixture server")
	}
	return f.requests[len(f.requests)-1]
}

func newFixtureEngine(t *testing.T, engineType, baseURL string) Engine {
	t.Helper()
	engine, err := newEngine(config.WebSearchEngineConfig{Type: engineType, BaseURL: baseURL}, config.WebSearchConfig{})
	if err != nil {
		t.Fatalf("newEngine(%s) error = %v", engineType, err)
	}
	return engine
}

func TestDuckDuckGoEngineParsesResultPage(t *testing.T) {
	fixture := serveFixture(t, "duckduckgo.html")
	engine := newFixtureEngine(t, "duckduckgo", fixture.URL)

	results, err := engine.Search(context.Background(), Query{
		Text:      "postgres 18 features",
		Limit:     5,
		Freshness: FreshnessWeek,
		Domains:   []string{"postgresql.org"},
		Language:  "en",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	request := fixture.lastRequest(t)
	query := request.URL.Query()
	if want := "postgres 18 features site:postgresql.org"; query.Get("q") != want {
		t.Fatalf("q = %q, want %q", query.Get("q"), want)
	}
	if query.Get("df") != "w" {
		t.Fatalf("df = %q, want w", query.Get("df"))
	}
	if request.Header.Get("User-Agent") == "" {
		t.Fatal("engine must send a user agent")
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2: %#v", len(results), results)
	}
	first := results[0]
	if first.Title == "" || first.Snippet == "" {
		t.Fatalf("title and snippet must be populated: %#v", first)
	}
	if !strings.HasPrefix(first.URL, "https://") || strings.Contains(first.URL, "duckduckgo.com") {
		t.Fatalf("redirect wrapper was not unwrapped: %q", first.URL)
	}
	if first.Provider != "duckduckgo" {
		t.Fatalf("provider = %q, want duckduckgo", first.Provider)
	}
}

func TestBingEngineParsesResultPage(t *testing.T) {
	fixture := serveFixture(t, "bing.html")
	engine := newFixtureEngine(t, "bing", fixture.URL)

	results, err := engine.Search(context.Background(), Query{
		Text:      "postgres 18 features",
		Limit:     4,
		Freshness: FreshnessDay,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	query := fixture.lastRequest(t).URL.Query()
	if query.Get("count") != "4" {
		t.Fatalf("count = %q, want 4", query.Get("count"))
	}
	if query.Get("filters") != `ex1:"ez1"` {
		t.Fatalf("filters = %q, want the past-day filter", query.Get("filters"))
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2: %#v", len(results), results)
	}
	if results[0].Title == "" || results[0].Snippet == "" {
		t.Fatalf("title and snippet must be populated: %#v", results[0])
	}
}

func TestGoogleEngineParsesResultPage(t *testing.T) {
	fixture := serveFixture(t, "google.html")
	engine := newFixtureEngine(t, "google", fixture.URL)

	results, err := engine.Search(context.Background(), Query{Text: "postgres 18", Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	// The settings link is engine-internal and must be dropped.
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2: %#v", len(results), results)
	}
	if results[0].URL != "https://www.postgresql.org/docs/current/release-18.html" {
		t.Fatalf("google /url wrapper was not unwrapped: %q", results[0].URL)
	}
	if !strings.Contains(results[0].Snippet, "new features of PostgreSQL 18") {
		t.Fatalf("snippet not extracted: %q", results[0].Snippet)
	}
}

func TestEnginesReportBotWallsAsErrors(t *testing.T) {
	cases := map[string]string{
		"duckduckgo": "duckduckgo-blocked.html",
		"google":     "google-wall.html",
	}
	for engineType, fixtureName := range cases {
		fixture := serveFixture(t, fixtureName)
		engine := newFixtureEngine(t, engineType, fixture.URL)

		results, err := engine.Search(context.Background(), Query{Text: "query"})
		if err == nil {
			t.Fatalf("%s: expected an error instead of %d silent results", engineType, len(results))
		}
		if !strings.Contains(err.Error(), "bot wall") {
			t.Fatalf("%s: error = %v, want a bot wall error", engineType, err)
		}
	}
}

func TestEnginesReportUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	engine := newFixtureEngine(t, "duckduckgo", server.URL)
	_, err := engine.Search(context.Background(), Query{Text: "query"})
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("error = %v, want an upstream status error", err)
	}
}

func TestNewEngineValidatesType(t *testing.T) {
	if _, err := newEngine(config.WebSearchEngineConfig{Type: "altavista"}, config.WebSearchConfig{}); err == nil {
		t.Fatal("expected an error for an unsupported engine type")
	}
}

func TestEngineDefaultsAndOverrides(t *testing.T) {
	cfg := config.WebSearchConfig{Language: "zh"}
	engine, err := newEngine(config.WebSearchEngineConfig{Type: "bing", Region: "zh-CN"}, cfg)
	if err != nil {
		t.Fatalf("newEngine() error = %v", err)
	}
	bing, ok := engine.(*bingEngine)
	if !ok {
		t.Fatalf("engine = %T, want *bingEngine", engine)
	}
	if bing.baseURL != "https://www.bing.com" || bing.region != "zh-CN" || bing.language != "zh" {
		t.Fatalf("unexpected engine: %#v", bing)
	}
	if bing.client.Timeout != defaultTimeout {
		t.Fatalf("timeout = %v, want the %v default", bing.client.Timeout, defaultTimeout)
	}
}

func TestEnginesRejectResultsUnrelatedToQuery(t *testing.T) {
	// Scraped engines can answer with a real result page for a different topic.
	// Those rows must not be reported as ranked results for this query.
	fixture := serveFixture(t, "bing.html")
	engine := newFixtureEngine(t, "bing", fixture.URL)

	results, err := engine.Search(context.Background(), Query{Text: "kubernetes operator patterns", Limit: 5})
	if err == nil {
		t.Fatalf("expected unrelated results to be rejected, got %#v", results)
	}
	if !strings.Contains(err.Error(), "unrelated to the query") {
		t.Fatalf("error = %v, want a relevance error", err)
	}
}

func TestRelevanceGuard(t *testing.T) {
	rows := []Result{{Title: "PostgreSQL 18 release notes", URL: "https://example.com/a"}}

	if err := relevanceGuard(rows, Query{Text: "postgres 18"}); err != nil {
		t.Fatalf("a matching result set must pass: %v", err)
	}
	if err := relevanceGuard(rows, Query{Text: "kubernetes operators"}); err == nil {
		t.Fatal("a result set sharing no term with the query must be rejected")
	}
	// Queries without usable terms cannot be judged.
	if err := relevanceGuard(rows, Query{Text: "a 1 !?"}); err != nil {
		t.Fatalf("a query without terms must not be judged: %v", err)
	}
	if err := relevanceGuard(nil, Query{Text: "postgres"}); err != nil {
		t.Fatalf("an empty result set is not a relevance failure: %v", err)
	}
}

func TestEngineReportsThrottling(t *testing.T) {
	// DuckDuckGo answers a throttled request with 202 and an anomaly page. That
	// must surface as an engine failure, never as an empty result set.
	body, err := os.ReadFile(filepath.Join("testdata", "duckduckgo-blocked.html"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		w.Write(body)
	}))
	t.Cleanup(server.Close)

	engine := newFixtureEngine(t, "duckduckgo", server.URL)
	results, err := engine.Search(context.Background(), Query{Text: "nix flakes"})
	if err == nil {
		t.Fatalf("expected a throttle error, got %#v", results)
	}
	if !strings.Contains(err.Error(), "202") || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("error = %v, want an explicit throttle error", err)
	}
}
