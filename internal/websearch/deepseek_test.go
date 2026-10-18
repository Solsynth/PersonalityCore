package websearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"src.solsynth.dev/sosys/persona/internal/config"
)

// The fixture answers the endpoints the engine really calls.
const (
	fixtureSearchPath = deepseekSearchPath
	fixtureFetchPath  = deepseekFetchPath
)

// fixturePageContent is long enough to pass the crawler's minimum, so one hit
// carrying it becomes an indexable page.
var fixturePageContent = strings.Repeat("Release eighteen content. ", 40)

// fixtureMarkdown is the Fetch API's answer: Markdown text, no HTML.
var fixtureMarkdown = strings.Repeat("PostgreSQL 18 adds asynchronous I/O. ", 20)

const deepseekSearchPayload = `{
  "results": [
    {
      "rerank_score": 2.4,
      "title": "PostgreSQL 18 release notes",
      "url": "https://www.postgresql.org/docs/current/release-18.html",
      "snippet": "Short display summary of the release.",
      "summary": "PostgreSQL 18 adds an asynchronous I/O subsystem that improves sequential scans, plus a skip scan plan for multicolumn indexes.",
      "content": "PLACEHOLDER_CONTENT",
      "site_name": "PostgreSQL",
      "site_icon": "https://www.postgresql.org/favicon.ico",
      "date": "2025-09-25T00:00:00+00:00",
      "authority": 4
    },
    {
      "rerank_score": 1.9,
      "title": "",
      "url": "https://example.com/postgres-18-guide",
      "snippet": "A short snippet with no summary behind it.",
      "summary": "",
      "content": "",
      "site_name": "Example Blog",
      "site_icon": null,
      "date": null,
      "authority": 1
    },
    {
      "rerank_score": 0.4,
      "title": "Ignored row",
      "url": "",
      "snippet": "A hit without a URL cannot be shown or fetched.",
      "summary": "",
      "content": "",
      "site_name": null,
      "site_icon": null,
      "date": null,
      "authority": -1
    }
  ]
}`

// searchFixture answers the Search and Fetch endpoints and records every
// request, so a test can pin both the request shape and the parsing.
type searchFixture struct {
	mu       sync.Mutex
	requests []capturedRequest
	handler  func(w http.ResponseWriter, r *http.Request)
}

type capturedRequest struct {
	path    string
	header  http.Header
	body    map[string]any
	rawBody string
}

func (f *searchFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)

	f.mu.Lock()
	f.requests = append(f.requests, capturedRequest{
		path:    r.URL.Path,
		header:  r.Header.Clone(),
		body:    body,
		rawBody: string(raw),
	})
	handler := f.handler
	f.mu.Unlock()

	if handler != nil {
		handler(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case fixtureFetchPath:
		io.WriteString(w, `{"title":"Upgrading to PostgreSQL 18","content":"`+fixtureMarkdown+`"}`)
	default:
		io.WriteString(w, strings.Replace(deepseekSearchPayload, "PLACEHOLDER_CONTENT", fixturePageContent, 1))
	}
}

func (f *searchFixture) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, request := range f.requests {
		if request.path == path {
			count++
		}
	}
	return count
}

func (f *searchFixture) last(t *testing.T) capturedRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("engine never called the fixture")
	}
	return f.requests[len(f.requests)-1]
}

func newDeepSeekEngine(t *testing.T, baseURL string) Engine {
	t.Helper()
	engine, err := newEngine(config.WebSearchEngineConfig{
		Type: "deepseek", APIKey: "sk-test", BaseURL: baseURL, Provider: "deepseek", Price: "1",
	}, config.WebSearchConfig{})
	if err != nil {
		t.Fatalf("newEngine() error = %v", err)
	}
	return engine
}

func newDeepSeekFixture(t *testing.T, handler func(http.ResponseWriter, *http.Request)) (*searchFixture, string) {
	t.Helper()
	fixture := &searchFixture{handler: handler}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	return fixture, server.URL
}

// TestDeepSeekEngineCallsSearchAPI pins the request the direct Search endpoint
// receives: its path beside the chat base, bearer auth, and the fields that
// make the reply useful.
func TestDeepSeekEngineCallsSearchAPI(t *testing.T) {
	fixture, baseURL := newDeepSeekFixture(t, nil)

	response, err := newDeepSeekEngine(t, baseURL).Search(context.Background(), Query{
		Text:  "postgres 18 async io",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	request := fixture.last(t)
	if request.path != fixtureSearchPath {
		t.Fatalf("path = %q, want %q", request.path, fixtureSearchPath)
	}
	if got := request.header.Get("Authorization"); got != "Bearer sk-test" {
		t.Fatalf("Authorization = %q, want the bearer key the provider requires", got)
	}
	if got := request.header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if request.body["query"] != "postgres 18 async io" {
		t.Fatalf("query = %#v", request.body["query"])
	}
	if request.body["count"] != float64(5) {
		t.Fatalf("count = %#v, want the caller's limit", request.body["count"])
	}
	// Page content is what makes a hit answerable without a second request, so
	// it is always asked for.
	if request.body["need_content"] != true {
		t.Fatalf("need_content = %#v, want true", request.body["need_content"])
	}
	// An unmodified query needs no second copy for the provider's own
	// understanding.
	if _, ok := request.body["ori_question"]; ok {
		t.Fatalf("ori_question must be absent when the query is untouched: %s", request.rawBody)
	}
	// The model-backed request shape is gone entirely.
	for _, leftover := range []string{"max_tokens", "web_search_20250305", "thinking", "tools"} {
		if strings.Contains(request.rawBody, leftover) {
			t.Fatalf("request still carries the Messages API field %q: %s", leftover, request.rawBody)
		}
	}

	// The row without a URL is dropped; the other two survive with the text the
	// provider returned.
	if len(response.Results) != 2 {
		t.Fatalf("got %d results, want 2: %#v", len(response.Results), response.Results)
	}
	first := response.Results[0]
	if first.Provider != "deepseek" || first.Title != "PostgreSQL 18 release notes" {
		t.Fatalf("unexpected first result: %#v", first)
	}
	if !strings.Contains(first.Snippet, "skip scan plan") {
		t.Fatalf("snippet = %q, want the long summary the provider means for a model to read", first.Snippet)
	}
	// A hit with no title falls back to the site it came from rather than
	// disappearing.
	if second := response.Results[1]; second.Title != "Example Blog" {
		t.Fatalf("titleless hit = %#v, want the site name as its title", second)
	}
}

// TestDeepSeekEngineReturnsPageText asserts the content the search already read
// is handed to the index instead of being fetched again.
func TestDeepSeekEngineReturnsPageText(t *testing.T) {
	fixture, baseURL := newDeepSeekFixture(t, nil)

	response, err := newDeepSeekEngine(t, baseURL).Search(context.Background(), Query{
		Text: "postgres 18 async io", Limit: 5,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	// Only the hit that carried usable page text becomes a page.
	if len(response.Pages) != 1 {
		t.Fatalf("got %d pages, want 1: %#v", len(response.Pages), response.Pages)
	}
	page := response.Pages[0]
	if !strings.Contains(page.Text, "Release eighteen content") {
		t.Fatalf("page text = %q", truncate(page.Text, 80))
	}
	if page.URL != "https://www.postgresql.org/docs/current/release-18.html" {
		t.Fatalf("page url = %q", page.URL)
	}
	if page.Title != "PostgreSQL 18 release notes" {
		t.Fatalf("page title = %q", page.Title)
	}
	if fixture.count(fixtureFetchPath) != 0 {
		t.Fatal("the engine already returned the page; nothing should be fetched")
	}
}

// TestDeepSeekEngineSendsOperatorClauses checks the caller's domains, language,
// and recency window go out as the provider's own query operators.
func TestDeepSeekEngineSendsOperatorClauses(t *testing.T) {
	fixture, baseURL := newDeepSeekFixture(t, nil)

	_, err := newDeepSeekEngine(t, baseURL).Search(context.Background(), Query{
		Text:      "postgres 18 async io",
		Limit:     3,
		Freshness: FreshnessMonth,
		Domains:   []string{"postgresql.org", "sqlite.org"},
		Language:  "en-US",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	request := fixture.last(t)
	query, _ := request.body["query"].(string)
	for _, want := range []string{
		"postgres 18 async io",
		"site:postgresql.org OR site:sqlite.org",
		"lang:en",
		"after:",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("query %q missing %q", query, want)
		}
	}
	if len([]rune(query)) > deepseekMaxQueryCharacters {
		t.Fatalf("query is %d characters, over the provider's cap", len([]rune(query)))
	}
	// The provider gets the pristine question beside the composed query.
	if original, _ := request.body["ori_question"].(string); original != "postgres 18 async io" {
		t.Fatalf("ori_question = %q, want the caller's query unchanged", original)
	}
	// A domain-restricted search overfetches, because the engine filters the
	// hits afterwards.
	if request.body["count"] != float64(9) {
		t.Fatalf("count = %#v, want 3 results overfetched for the domain filter", request.body["count"])
	}
}

// TestDeepSeekEngineEnforcesDomainsTheProviderMayIgnore keeps a restriction
// working when its clause is dropped to fit the 128-character query cap, and
// fails the search when nothing inside the domains came back.
func TestDeepSeekEngineEnforcesDomainsTheProviderMayIgnore(t *testing.T) {
	fixture, baseURL := newDeepSeekFixture(t, nil)
	engine := newDeepSeekEngine(t, baseURL)

	response, err := engine.Search(context.Background(), Query{
		Text: "postgres 18 async io", Limit: 5, Domains: []string{"postgresql.org"},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(response.Results) != 1 || !strings.HasPrefix(response.Results[0].URL, "https://www.postgresql.org/") {
		t.Fatalf("off-domain hits leaked into the results: %#v", response.Results)
	}

	_, err = engine.Search(context.Background(), Query{
		Text: "postgres 18 async io", Domains: []string{"sqlite.org"},
	})
	if err == nil || !strings.Contains(err.Error(), "sqlite.org") {
		t.Fatalf("error = %v, want a domain mismatch naming the requested domain", err)
	}

	// A clause that no longer fits is dropped from the request while the terms
	// keep their budget, and the restriction still holds on the way back.
	long := "postgres 18 async io " + strings.Repeat("vacuum ", 20)
	response, err = engine.Search(context.Background(), Query{Text: long, Limit: 5, Domains: []string{"postgresql.org"}})
	if err != nil {
		t.Fatalf("Search() with a long query error = %v", err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %#v, want the domain filter enforced after the clause was dropped", response.Results)
	}
	query, _ := fixture.last(t).body["query"].(string)
	if strings.Contains(query, "site:") {
		t.Fatalf("a query at the cap must drop the clause, not the terms: %q", query)
	}
	if !strings.HasPrefix(query, "postgres 18 async io") {
		t.Fatalf("query lost its leading terms: %q", query)
	}
}

// TestDeepSeekEngineReadsPagesThroughFetchAPI pins the Fetch endpoint: bearer
// auth, the url field, and Markdown text with no HTML parsing.
func TestDeepSeekEngineReadsPagesThroughFetchAPI(t *testing.T) {
	fixture, baseURL := newDeepSeekFixture(t, nil)

	engine := newDeepSeekEngine(t, baseURL)
	reader, ok := engine.(PageReader)
	if !ok {
		t.Fatalf("engine = %T, want a PageReader", engine)
	}
	const target = "https://example.com/postgres-18-guide"
	page, err := reader.ReadPage(context.Background(), target)
	if err != nil {
		t.Fatalf("ReadPage() error = %v", err)
	}
	if page.URL != target {
		t.Fatalf("page url = %q, want %q", page.URL, target)
	}
	if page.Title != "Upgrading to PostgreSQL 18" {
		t.Fatalf("page title = %q", page.Title)
	}
	if !strings.Contains(page.Text, "asynchronous I/O") {
		t.Fatalf("page text = %q", truncate(page.Text, 80))
	}

	request := fixture.last(t)
	if request.path != fixtureFetchPath {
		t.Fatalf("path = %q, want %q", request.path, fixtureFetchPath)
	}
	if got := request.header.Get("Authorization"); got != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", got)
	}
	if request.body["url"] != target {
		t.Fatalf("body = %#v, want only the url", request.body)
	}
}

// TestDeepSeekEngineRejectsUnfetchableURLs checks a doomed request is refused
// before it is sent: the provider rejects these itself, and a fetch it refuses
// on policy grounds is one the account should not wait on.
func TestDeepSeekEngineRejectsUnfetchableURLs(t *testing.T) {
	fixture, baseURL := newDeepSeekFixture(t, nil)
	reader := newDeepSeekEngine(t, baseURL).(PageReader)

	cases := map[string]string{
		"empty":       "",
		"ftp scheme":  "ftp://example.com/page",
		"no host":     "https:///page",
		"credentials": "https://user:secret@example.com/page",
		"oversized":   "https://example.com/" + strings.Repeat("a", deepseekMaxURLCharacters),
	}
	for name, target := range cases {
		if _, err := reader.ReadPage(context.Background(), target); err == nil {
			t.Fatalf("%s: ReadPage(%q) error = nil, want a refusal", name, truncate(target, 40))
		}
	}
	if fixture.count(fixtureFetchPath) != 0 {
		t.Fatalf("refused urls must not reach the provider, got %d requests", fixture.count(fixtureFetchPath))
	}
}

// TestDeepSeekEngineReportsProviderErrors keeps the provider's own explanation
// and the advice for the retryable statuses in the engine error, so a failed
// engine is diagnosable from the API response alone.
func TestDeepSeekEngineReportsProviderErrors(t *testing.T) {
	cases := map[string]struct {
		status  int
		payload string
		wants   []string
	}{
		"invalid count": {
			status:  http.StatusBadRequest,
			payload: `{"error":{"message":"'count' must be between 1 and 50","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`,
			wants:   []string{"400", "'count' must be between 1 and 50"},
		},
		"bad key": {
			status:  http.StatusUnauthorized,
			payload: `{"error":{"message":"Authentication Fails"}}`,
			wants:   []string{"401", "apiKey"},
		},
		"no balance": {
			status:  http.StatusPaymentRequired,
			payload: `{"error":{"message":"Insufficient Balance"}}`,
			wants:   []string{"402", "top it up"},
		},
		"throttled": {
			status:  http.StatusTooManyRequests,
			payload: `{"error":{"message":"Rate limit reached"}}`,
			wants:   []string{"429", "retry later"},
		},
		"page unreadable": {
			status:  http.StatusNotFound,
			payload: `{"error":{"message":"no usable content"}}`,
			wants:   []string{"404", "no usable content"},
		},
		"upstream": {
			status:  http.StatusBadGateway,
			payload: `not json`,
			wants:   []string{"502", "not json"},
		},
	}
	for name, testCase := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(testCase.status)
			io.WriteString(w, testCase.payload)
		}))
		_, err := newDeepSeekEngine(t, server.URL).Search(context.Background(), Query{Text: "postgres 18 async io"})
		server.Close()
		if err == nil {
			t.Fatalf("%s: Search() error = nil, want the provider failure", name)
		}
		for _, want := range testCase.wants {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: error = %q, want it to name %q", name, err, want)
			}
		}
	}
}

// TestDeepSeekEngineKeepsAnEmptySearchSuccessful: no matches is an answer, not
// a failure, and the provider bills nothing for it.
func TestDeepSeekEngineKeepsAnEmptySearchSuccessful(t *testing.T) {
	_, baseURL := newDeepSeekFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[]}`)
	})

	response, err := newDeepSeekEngine(t, baseURL).Search(context.Background(), Query{Text: "nothing matches this query at all"})
	if err != nil {
		t.Fatalf("Search() error = %v, want an empty result set", err)
	}
	if len(response.Results) != 0 {
		t.Fatalf("results = %#v, want none", response.Results)
	}
}

// TestDeepSeekEngineRejectsAnEmptyQuery tells the caller its query cannot be
// searched rather than reporting an engine failure.
func TestDeepSeekEngineRejectsAnEmptyQuery(t *testing.T) {
	fixture, baseURL := newDeepSeekFixture(t, nil)

	_, err := newDeepSeekEngine(t, baseURL).Search(context.Background(), Query{Text: "   ", Limit: 5})
	if err == nil {
		t.Fatal("Search() error = nil, want a rejected query")
	}
	if !strings.Contains(err.Error(), "search term") {
		t.Fatalf("error = %v, want it to name the missing terms", err)
	}
	if fixture.count(fixtureSearchPath) != 0 {
		t.Fatal("an unusable query must not be sent")
	}
}

func TestDeepSeekEngineRequiresAPIKey(t *testing.T) {
	if _, err := newEngine(config.WebSearchEngineConfig{Type: "deepseek", Price: "1"}, config.WebSearchConfig{}); err == nil {
		t.Fatal("expected an error when an API engine has no key")
	}
}

// TestDeepSeekEngineRaisesTimeoutAboveScrapedDefault: one search now reads
// whole pages on the provider's side, so the scraped default is not enough.
func TestDeepSeekEngineRaisesTimeoutAboveScrapedDefault(t *testing.T) {
	engine, err := newEngine(config.WebSearchEngineConfig{Type: "deepseek", APIKey: "sk-test", Price: "1"}, config.WebSearchConfig{})
	if err != nil {
		t.Fatalf("newEngine() error = %v", err)
	}
	deepseek, ok := engine.(*deepseekEngine)
	if !ok {
		t.Fatalf("engine = %T, want *deepseekEngine", engine)
	}
	if deepseek.client.Timeout != deepseekMinTimeout {
		t.Fatalf("timeout = %s, want %s", deepseek.client.Timeout, deepseekMinTimeout)
	}
	if deepseek.baseURL != deepseekDefaultBaseURL {
		t.Fatalf("baseURL = %q, want the DeepSeek default %q", deepseek.baseURL, deepseekDefaultBaseURL)
	}
	if deepseek.client.Timeout <= defaultTimeout {
		t.Fatalf("timeout = %s, want more than the scraped default %s", deepseek.client.Timeout, defaultTimeout)
	}
}

// TestDeepSeekEngineHonoursLongerTimeout keeps an explicit timeout authoritative.
func TestDeepSeekEngineHonoursLongerTimeout(t *testing.T) {
	engine, err := newEngine(config.WebSearchEngineConfig{
		Type: "deepseek", APIKey: "sk-test", Price: "1", Timeout: 2 * time.Minute,
	}, config.WebSearchConfig{})
	if err != nil {
		t.Fatalf("newEngine() error = %v", err)
	}
	if got := engine.(*deepseekEngine).client.Timeout; got != 2*time.Minute {
		t.Fatalf("timeout = %s, want the configured 2m", got)
	}
}

// TestDeepSeekEngineDerivesAPIRoot checks the tool endpoints are reached beside
// the provider entry's chat base, however that base was written: bare, with the
// OpenAI version segment, or at the Anthropic endpoint this engine used to call.
func TestDeepSeekEngineDerivesAPIRoot(t *testing.T) {
	cases := map[string][2]string{
		"chat base":              {"https://api.deepseek.com", "https://api.deepseek.com"},
		"trailing slash":         {"https://api.deepseek.com/", "https://api.deepseek.com"},
		"version segment":        {"https://api.deepseek.com/v1", "https://api.deepseek.com"},
		"anthropic endpoint":     {"https://api.deepseek.com/anthropic", "https://api.deepseek.com"},
		"proxy with a path":      {"https://proxy.example/deepseek", "https://proxy.example/deepseek"},
		"proxy version segment":  {"https://proxy.example/deepseek/v1", "https://proxy.example/deepseek"},
		"provider without base":  {"", deepseekDefaultBaseURL},
		"whitespace around base": {"  https://api.deepseek.com  ", "https://api.deepseek.com"},
	}
	for name, testCase := range cases {
		if got := deepseekAPIRoot(testCase[0]); got != testCase[1] {
			t.Fatalf("%s: deepseekAPIRoot(%q) = %q, want %q", name, testCase[0], got, testCase[1])
		}
	}
}

func TestWebSearchEngineDomainMatching(t *testing.T) {
	cases := map[string]struct {
		url    string
		domain string
		want   bool
	}{
		"exact host":         {"https://postgresql.org/docs", "postgresql.org", true},
		"subdomain":          {"https://www.postgresql.org/docs", "postgresql.org", true},
		"deeper subdomain":   {"https://wiki.postgresql.org/x", "postgresql.org", true},
		"path prefix":        {"https://example.com/docs/1", "example.com/docs", true},
		"path mismatch":      {"https://example.com/blog/1", "example.com/docs", false},
		"lookalike suffix":   {"https://notpostgresql.org.co/x", "postgresql.org", false},
		"other host":         {"https://sqlite.org", "postgresql.org", false},
		"domain with scheme": {"https://postgresql.org", "https://postgresql.org", true},
	}
	for name, testCase := range cases {
		normalized := normalizeDomains([]string{testCase.domain})
		if len(normalized) == 0 {
			t.Fatalf("%s: domain normalized away", name)
		}
		if got := urlInDomain(testCase.url, normalized[0]); got != testCase.want {
			t.Fatalf("%s: urlInDomain(%q, %q) = %v, want %v", name, testCase.url, normalized[0], got, testCase.want)
		}
	}
}

// TestDeepSeekEngineComposesQueryBudget pins the operator mapping in isolation,
// including what gives way when the provider's 128-character cap bites.
func TestDeepSeekEngineComposesQueryBudget(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name  string
		query Query
		want  string
	}{
		{
			name:  "whitespace is folded",
			query: Query{Text: "postgres 18   async io"},
			want:  "postgres 18 async io",
		},
		{
			name:  "single domain",
			query: Query{Text: "postgres release", Domains: []string{"postgresql.org"}},
			want:  "postgres release site:postgresql.org",
		},
		{
			name:  "domains join with OR",
			query: Query{Text: "postgres release", Domains: []string{"postgresql.org", "sqlite.org"}},
			want:  "postgres release site:postgresql.org OR site:sqlite.org",
		},
		{
			name:  "language region is reduced",
			query: Query{Text: "postgres release", Language: "zh-CN"},
			want:  "postgres release lang:zh",
		},
		{
			name:  "locale junk is dropped",
			query: Query{Text: "postgres release", Language: "en;q=0.8"},
			want:  "postgres release",
		},
		{
			name:  "freshness becomes a date floor",
			query: Query{Text: "postgres release", Freshness: FreshnessWeek},
			want:  "postgres release after:2026-02-22",
		},
		{
			name:  "unknown freshness has no date",
			query: Query{Text: "postgres release", Freshness: Freshness("decade")},
			want:  "postgres release",
		},
	}
	for _, testCase := range cases {
		if got := deepseekSearchQuery(testCase.query, now); got != testCase.want {
			t.Fatalf("%s: deepseekSearchQuery() = %q, want %q", testCase.name, got, testCase.want)
		}
	}

	// Every clause competing for the same budget: the domain filter keeps its
	// place first, then language, then the date.
	combined := deepseekSearchQuery(Query{
		Text:      "postgres release",
		Domains:   []string{"postgresql.org"},
		Language:  "en",
		Freshness: FreshnessYear,
	}, now)
	order := []string{"postgres release", "site:postgresql.org", "lang:en", "after:2025-03-01"}
	for index, want := range order {
		position := strings.Index(combined, want)
		if position < 0 {
			t.Fatalf("clause %q missing from %q", want, combined)
		}
		if index > 0 && position <= strings.Index(combined, order[index-1]) {
			t.Fatalf("clauses out of priority order in %q", combined)
		}
	}

	// A query that cannot fit both its terms and a clause keeps the terms.
	got := deepseekSearchQuery(Query{Text: strings.Repeat("word ", 40), Domains: []string{"postgresql.org"}}, now)
	if len([]rune(got)) > deepseekMaxQueryCharacters {
		t.Fatalf("composed query is %d characters, over the cap", len([]rune(got)))
	}
	if strings.Contains(got, "site:") {
		t.Fatalf("the clause should have been dropped, not the terms: %q", got)
	}

	// Only whitespace: nothing searchable, so nothing is sent.
	if got := deepseekSearchQuery(Query{Text: "     "}, now); got != "" {
		t.Fatalf("deepseekSearchQuery(blank) = %q, want no query", got)
	}
}

// TestDeepSeekMarkdownKeepsStructure: the provider returns Markdown, so the
// page text keeps its line breaks and its angle brackets — code spans like
// `Vec<u8>` are content, not tags, and flattening a document into one line
// would waste the shape the index and the model both read.
func TestDeepSeekMarkdownKeepsStructure(t *testing.T) {
	raw := "# Release\r\n\r\n\r\nA generic `Vec<u8>` buffer & an &amp; escape.\r\n\r\n\r\n\r\nFinal paragraph."
	got := deepseekMarkdown(raw)
	want := "# Release\n\nA generic `Vec<u8>` buffer & an &amp; escape.\n\nFinal paragraph."
	if got != want {
		t.Fatalf("deepseekMarkdown() = %q, want %q", got, want)
	}

	long := strings.Repeat("word ", 5000)
	if bounded := deepseekMarkdown(long); len([]rune(bounded)) != maxPageRunes {
		t.Fatalf("page text is %d runes, want it bounded by the crawler's page limit", len([]rune(bounded)))
	}
}

// TestDeepSeekRowsSeparatesSnippetFromPageText pins which text is collapsed and
// which keeps its structure.
func TestDeepSeekRowsSeparatesSnippetFromPageText(t *testing.T) {
	content := strings.Repeat("Paragraph one of the page.\n\n", 12)
	rows, pages := deepseekRows([]deepseekSearchHit{{
		Title:   "Release",
		URL:     "https://a.example/1",
		Summary: "A summary\nspanning  &amp;  two lines.",
		Content: content,
	}})
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].Snippet != "A summary spanning & two lines." {
		t.Fatalf("snippet = %q, want one collapsed line", rows[0].Snippet)
	}
	if len(pages) != 1 {
		t.Fatalf("pages = %d, want the content indexed", len(pages))
	}
	if !strings.Contains(pages[0].Text, "Paragraph one of the page.\n\n") {
		t.Fatalf("page text lost its paragraph breaks: %q", truncate(pages[0].Text, 120))
	}
}

func TestDeepSeekAfterDateCoversEveryWindow(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cases := map[Freshness]string{
		FreshnessDay:   "2026-02-28",
		FreshnessWeek:  "2026-02-22",
		FreshnessMonth: "2026-01-30",
		FreshnessYear:  "2025-03-01",
		"":             "",
	}
	for freshness, want := range cases {
		if got := deepseekAfterDate(freshness, now); got != want {
			t.Fatalf("deepseekAfterDate(%q) = %q, want %q", freshness, got, want)
		}
	}
}

func TestDeepSeekFitTextCutsAtWordBoundaries(t *testing.T) {
	cases := []struct {
		text   string
		budget int
		want   string
	}{
		{"short", 10, "short"},
		{"one two three four", 13, "one two three"},
		{"one two three four", 7, "one two"},
		{"one two three", 3, "one"},
		{"one two three", 0, ""},
		{"-12345678", 5, "-1234"},
	}
	for _, testCase := range cases {
		if got := deepseekFitText(testCase.text, testCase.budget); got != testCase.want {
			t.Fatalf("deepseekFitText(%q, %d) = %q, want %q", testCase.text, testCase.budget, got, testCase.want)
		}
	}
}

// TestDeepSeekRowsUsesSummaryThenSnippet pins which of the two summary fields
// becomes the display snippet, and what becomes an indexable page.
func TestDeepSeekRowsUsesSummaryThenSnippet(t *testing.T) {
	rows, pages := deepseekRows([]deepseekSearchHit{
		{Title: "Both", URL: "https://a.example/1", Snippet: "short", Summary: "the long model-readable form"},
		{Title: "Only short", URL: "https://a.example/2", Snippet: "the display form", Summary: "   "},
		{Title: "Neither", URL: "https://a.example/3", Content: strings.Repeat("thin page. ", 40)},
		{Title: "Tiny content", URL: "https://a.example/4", Content: "not enough to index"},
	})
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want the hits mapped one to one", len(rows))
	}
	if rows[0].Snippet != "the long model-readable form" {
		t.Fatalf("rows[0].Snippet = %q, want the summary", rows[0].Snippet)
	}
	if rows[1].Snippet != "the display form" {
		t.Fatalf("rows[1].Snippet = %q, want the snippet as the fallback", rows[1].Snippet)
	}
	if rows[2].Snippet != "" {
		t.Fatalf("rows[2].Snippet = %q, want empty", rows[2].Snippet)
	}
	if len(pages) != 1 || pages[0].URL != "https://a.example/3" {
		t.Fatalf("pages = %#v, want only the hit with indexable content", pages)
	}
}

// TestRetainedPagesKeepsOnlyShownResults stops the index being filled with
// pages behind hits that normalization dropped.
func TestRetainedPagesKeepsOnlyShownResults(t *testing.T) {
	pages := []Page{
		{URL: "https://a.example/kept"},
		{URL: "https://a.example/dropped"},
	}
	kept := []Result{{URL: "https://www.a.example/kept?ref=x"}}
	retained := retainedPages(pages, kept)
	if len(retained) != 1 || retained[0].URL != "https://a.example/kept" {
		t.Fatalf("retained = %#v, want only the surviving hit's page", retained)
	}
	if retainedPages(pages, nil) != nil {
		t.Fatal("no surviving results means no pages")
	}
}
