package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"src.solsynth.dev/sosys/persona/internal/config"
)

const testUserAgent = "PersonalityCoreBot/1.0 (+https://solar.network)"

func testCrawler(t *testing.T, perHostDelay time.Duration) *pageCrawler {
	t.Helper()
	return newPageCrawler(config.WebSearchCrawlConfig{
		Enabled:          true,
		MaxPagesPerQuery: 3,
		PageTimeout:      5 * time.Second,
		PerHostDelay:     perHostDelay,
		MaxPageBytes:     1 << 20,
	}, testUserAgent, 5*time.Second)
}

// articleServer serves the article fixture at /post and robots.txt on demand.
func articleServer(t *testing.T, robots string, robotsStatus int) (*httptest.Server, *int32) {
	t.Helper()
	var pageHits int32
	body, err := os.ReadFile(filepath.Join("testdata", "article.html"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			if robotsStatus == http.StatusOK {
				w.Header().Set("Content-Type", "text/plain")
				w.Write([]byte(robots))
				return
			}
			http.Error(w, "not found", robotsStatus)
		case "/post":
			atomic.AddInt32(&pageHits, 1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(body)
		case "/private":
			atomic.AddInt32(&pageHits, 1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(body)
		case "/paper":
			w.Header().Set("Content-Type", "application/pdf")
			w.Write([]byte("%PDF-1.4"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &pageHits
}

func TestParseRobotsKeepsOnlyApplicableGroups(t *testing.T) {
	entry := parseRobots([]byte(`
# example policy
User-agent: *
Disallow: /private
Allow: /private/public
Crawl-delay: 2

User-agent: otherbot
Disallow: /
`), testUserAgent)

	if entry.crawlDelay != 2*time.Second {
		t.Fatalf("crawlDelay = %v, want 2s", entry.crawlDelay)
	}
	paths := make([]string, 0, len(entry.rules))
	for _, rule := range entry.rules {
		paths = append(paths, rule.path)
	}
	if len(entry.rules) != 2 {
		t.Fatalf("rules = %#v, want only the wildcard group's two rules", entry.rules)
	}
	for _, path := range paths {
		if path == "/" {
			t.Fatal("a group for another user agent must be ignored")
		}
	}
}

func TestRobotsPolicyDecidesAccess(t *testing.T) {
	cache := newRobotsCache()
	entry := parseRobots([]byte("User-agent: *\nDisallow: /private\nAllow: /private/public\n"), testUserAgent)
	cache.store("https://example.com", entry)

	cases := map[string]bool{
		"https://example.com/private/secret":     false,
		"https://example.com/private/public/doc": true,
		"https://example.com/blog":               true,
	}
	for raw, want := range cases {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		allowed, _, err := cache.allowed(context.Background(), nil, testUserAgent, parsed)
		if err != nil {
			t.Fatalf("allowed(%s) error = %v", raw, err)
		}
		if allowed != want {
			t.Fatalf("allowed(%s) = %v, want %v", raw, allowed, want)
		}
	}
}

func TestFetchRefusesRobotsDisallowedPath(t *testing.T) {
	server, pageHits := articleServer(t, "User-agent: *\nDisallow: /private\n", http.StatusOK)
	crawler := testCrawler(t, 0)

	_, err := crawler.fetch(context.Background(), server.URL+"/private")
	if err == nil || !strings.Contains(err.Error(), "robots.txt") {
		t.Fatalf("error = %v, want a robots.txt refusal", err)
	}
	if hits := atomic.LoadInt32(pageHits); hits != 0 {
		t.Fatalf("disallowed page was requested %d times", hits)
	}
}

func TestFetchExtractsReadableText(t *testing.T) {
	server, _ := articleServer(t, "", http.StatusNotFound)
	crawler := testCrawler(t, 0)

	page, err := crawler.fetch(context.Background(), server.URL+"/post")
	if err != nil {
		t.Fatalf("fetch() error = %v", err)
	}
	if page.Title != "Upgrading to PostgreSQL 18" {
		t.Fatalf("title = %q", page.Title)
	}
	if !strings.Contains(page.Text, "asynchronous I/O subsystem") {
		t.Fatalf("article text missing: %q", page.Text)
	}
	for _, unwanted := range []string{"Copyright notice", "Site header", "window.analytics"} {
		if strings.Contains(page.Text, unwanted) {
			t.Fatalf("chrome or script text leaked into the article: %q", page.Text)
		}
	}
	if page.FetchedAt.IsZero() {
		t.Fatal("FetchedAt must be set")
	}
}

func TestFetchRejectsUnsupportedResponses(t *testing.T) {
	server, _ := articleServer(t, "", http.StatusNotFound)
	crawler := testCrawler(t, 0)

	if _, err := crawler.fetch(context.Background(), server.URL+"/paper"); err == nil || !strings.Contains(err.Error(), "content type") {
		t.Fatalf("error = %v, want a content type refusal", err)
	}
	if _, err := crawler.fetch(context.Background(), server.URL+"/missing"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %v, want a status refusal", err)
	}
	if _, err := crawler.fetch(context.Background(), "ftp://example.com/file"); err == nil {
		t.Fatal("expected a scheme refusal")
	}
}

func TestEnrichReplacesThinSnippetsWithPageText(t *testing.T) {
	server, _ := articleServer(t, "", http.StatusNotFound)
	crawler := testCrawler(t, 0)

	results := []Result{
		{Title: "Postgres 18", URL: server.URL + "/post", Snippet: "thin", Provider: "duckduckgo"},
		{Title: "Missing", URL: server.URL + "/missing", Snippet: "thin", Provider: "bing"},
	}
	enriched, pages := crawler.enrich(context.Background(), results)

	if len(pages) != 1 {
		t.Fatalf("got %d crawled pages, want 1: %#v", len(pages), pages)
	}
	if !strings.Contains(enriched[0].Snippet, "asynchronous I/O subsystem") {
		t.Fatalf("snippet was not replaced with page text: %q", enriched[0].Snippet)
	}
	if enriched[1].Snippet != "thin" {
		t.Fatalf("failed fetches must keep the engine snippet, got %q", enriched[1].Snippet)
	}
	if enriched[0].URL != results[0].URL || enriched[0].Title != results[0].Title {
		t.Fatalf("enrichment must not rewrite result identity: %#v", enriched[0])
	}
}

func TestEnrichLeavesFullSnippetsAlone(t *testing.T) {
	server, pageHits := articleServer(t, "", http.StatusNotFound)
	crawler := testCrawler(t, 0)

	longSnippet := strings.Repeat("detailed engine snippet ", 12)
	results := []Result{{Title: "Postgres 18", URL: server.URL + "/post", Snippet: longSnippet}}
	enriched, pages := crawler.enrich(context.Background(), results)

	if len(pages) != 0 || enriched[0].Snippet != longSnippet {
		t.Fatalf("a result with a full snippet must not be crawled: %#v", enriched)
	}
	if hits := atomic.LoadInt32(pageHits); hits != 0 {
		t.Fatalf("page was fetched %d times for a result that needed no enrichment", hits)
	}
}

func TestSearchCrawlsThinResultsIntoStore(t *testing.T) {
	server, _ := articleServer(t, "", http.StatusNotFound)
	engine := &stubEngine{name: "stub", results: []Result{
		{Title: "Postgres 18", URL: server.URL + "/post", Snippet: "thin", Provider: "stub"},
	}}
	store := &stubStore{}
	searcher := newTestSearcher(0, engine)
	searcher.store = store
	searcher.crawler = testCrawler(t, 0)

	response, err := searcher.Search(context.Background(), Query{Text: "postgres 18"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if response.PagesCrawled != 1 {
		t.Fatalf("PagesCrawled = %d, want 1", response.PagesCrawled)
	}
	if store.upserts != 1 || len(store.pages) != 1 {
		t.Fatalf("crawled pages were not stored: upserts=%d pages=%d", store.upserts, len(store.pages))
	}
	if !strings.Contains(store.pages[0].Text, "asynchronous I/O subsystem") {
		t.Fatalf("stored page text looks wrong: %q", store.pages[0].Text)
	}
	if !strings.Contains(response.Results[0].Snippet, "asynchronous I/O subsystem") {
		t.Fatalf("response snippet was not enriched: %q", response.Results[0].Snippet)
	}
}
