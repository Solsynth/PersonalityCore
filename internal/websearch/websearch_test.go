package websearch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"src.solsynth.dev/sosys/persona/internal/config"
)

type stubEngine struct {
	name    string
	results []Result
	err     error

	mu        sync.Mutex
	calls     int
	lastQuery Query
}

func (e *stubEngine) Name() string { return e.name }

func (e *stubEngine) Search(_ context.Context, query Query) ([]Result, error) {
	e.mu.Lock()
	e.calls++
	e.lastQuery = query
	e.mu.Unlock()
	if e.err != nil {
		return nil, e.err
	}
	return e.results, nil
}

func (e *stubEngine) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func (e *stubEngine) query() Query {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastQuery
}

type stubStore struct {
	pages   []Page
	hits    []Result
	upserts int
	err     error
}

func (s *stubStore) UpsertPages(_ context.Context, pages []Page) error {
	s.upserts++
	if s.err != nil {
		return s.err
	}
	s.pages = append(s.pages, pages...)
	return nil
}

func (s *stubStore) SearchPages(_ context.Context, _ string, _ int) ([]Result, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.hits, nil
}

func newTestSearcher(ttl time.Duration, engines ...Engine) *Searcher {
	return &Searcher{
		engines:      engines,
		defaultLimit: 5,
		maxLimit:     10,
		cache:        newCache(ttl),
	}
}

func result(engine, url string) Result {
	return Result{Title: "title " + url, URL: url, Provider: engine}
}

func TestSearchInterleavesEnginesAndDropsDuplicates(t *testing.T) {
	first := &stubEngine{name: "first", results: []Result{
		result("first", "https://a.example/1"),
		result("first", "https://a.example/2"),
		result("first", "https://a.example/3"),
	}}
	second := &stubEngine{name: "second", results: []Result{
		result("second", "https://a.example/2"),
		result("second", "https://b.example/1"),
	}}
	searcher := newTestSearcher(0, first, second)

	response, err := searcher.Search(context.Background(), Query{Text: "query", Limit: 4})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	// Rank order alternates engines, and the duplicate URL from the first
	// engine is skipped rather than repeated.
	want := []string{
		"https://a.example/1",
		"https://a.example/2",
		"https://b.example/1",
		"https://a.example/3",
	}
	if len(response.Results) != len(want) {
		t.Fatalf("got %d results, want %d: %#v", len(response.Results), len(want), response.Results)
	}
	for i, url := range want {
		if response.Results[i].URL != url {
			t.Fatalf("result %d = %q, want %q", i, response.Results[i].URL, url)
		}
	}
	if len(response.Engines) != 2 || response.Engines[0].Name != "first" || response.Engines[1].Results != 2 {
		t.Fatalf("unexpected engine reports: %#v", response.Engines)
	}
}

func TestSearchTreatsTrackingParametersAsSameResult(t *testing.T) {
	first := &stubEngine{name: "first", results: []Result{
		result("first", "https://www.example.com/post?utm_source=newsletter&id=7#section"),
	}}
	second := &stubEngine{name: "second", results: []Result{
		result("second", "https://example.com/post?id=7"),
	}}
	searcher := newTestSearcher(0, first, second)

	response, err := searcher.Search(context.Background(), Query{Text: "query"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("got %d results, want 1: %#v", len(response.Results), response.Results)
	}
	if response.Results[0].Provider != "first" {
		t.Fatalf("expected the first engine's URL to win, got %#v", response.Results[0])
	}
}

func TestSearchServesRepeatedQueriesFromCache(t *testing.T) {
	engine := &stubEngine{name: "only", results: []Result{result("only", "https://example.com/1")}}
	searcher := newTestSearcher(time.Minute, engine)

	first, err := searcher.Search(context.Background(), Query{Text: "go generics"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if first.Cached {
		t.Fatal("first search must not be reported as cached")
	}

	second, err := searcher.Search(context.Background(), Query{Text: "go   generics"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !second.Cached {
		t.Fatal("expected the repeat query to hit the cache")
	}
	if engine.callCount() != 1 {
		t.Fatalf("engine called %d times, want 1", engine.callCount())
	}

	if _, err := searcher.Search(context.Background(), Query{Text: "rust traits"}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if engine.callCount() != 2 {
		t.Fatalf("engine called %d times, want 2", engine.callCount())
	}
}

func TestSearchKeepsWorkingWhenOneEngineFails(t *testing.T) {
	broken := &stubEngine{name: "broken", err: errors.New("engine served a bot wall (captcha)")}
	healthy := &stubEngine{name: "healthy", results: []Result{result("healthy", "https://example.com/1")}}
	searcher := newTestSearcher(0, broken, healthy)

	response, err := searcher.Search(context.Background(), Query{Text: "query"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(response.Results))
	}
	if response.Engines[0].Error == "" || response.Engines[1].Error != "" {
		t.Fatalf("unexpected engine reports: %#v", response.Engines)
	}
}

func TestSearchAnswersFromLocalIndexWhenEveryEngineFails(t *testing.T) {
	store := &stubStore{hits: []Result{{Title: "Stored page", URL: "https://example.com/stored", Provider: "index"}}}
	searcher := newTestSearcher(0,
		&stubEngine{name: "one", err: errors.New("boom")},
		&stubEngine{name: "two", err: errors.New("bang")},
	)
	searcher.store = store

	response, err := searcher.Search(context.Background(), Query{Text: "query"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !response.Fallback {
		t.Fatal("expected the response to be marked as an index fallback")
	}
	if len(response.Results) != 1 || response.Results[0].Provider != "index" {
		t.Fatalf("unexpected results: %#v", response.Results)
	}
	if len(response.Engines) != 2 || response.Engines[0].Error == "" {
		t.Fatalf("engine failures must still be reported: %#v", response.Engines)
	}
}

func TestSearchFailsWhenEveryEngineFailsAndIndexIsEmpty(t *testing.T) {
	searcher := newTestSearcher(0,
		&stubEngine{name: "one", err: errors.New("boom")},
		&stubEngine{name: "two", err: errors.New("bang")},
	)
	searcher.store = &stubStore{}

	_, err := searcher.Search(context.Background(), Query{Text: "query"})
	if err == nil {
		t.Fatal("expected an error when every engine fails and the index is empty")
	}
	for _, want := range []string{"one", "two", "boom", "bang"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestSearchRejectsInvalidRequests(t *testing.T) {
	searcher := newTestSearcher(0, &stubEngine{name: "only"})

	cases := map[string]Query{
		"empty query":   {Text: "   "},
		"bad freshness": {Text: "query", Freshness: "fortnight"},
	}
	for name, query := range cases {
		if _, err := searcher.Search(context.Background(), query); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("%s: error = %v, want ErrInvalidQuery", name, err)
		}
	}
}

func TestSearchClampsLimitAndNormalizesDomains(t *testing.T) {
	engine := &stubEngine{name: "only", results: []Result{result("only", "https://example.com/1")}}
	searcher := newTestSearcher(0, engine)

	if _, err := searcher.Search(context.Background(), Query{Text: "query", Limit: 0}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if got := engine.query().Limit; got != 5 {
		t.Fatalf("default limit = %d, want 5", got)
	}

	if _, err := searcher.Search(context.Background(), Query{Text: "query", Limit: 500}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if got := engine.query().Limit; got != 10 {
		t.Fatalf("clamped limit = %d, want 10", got)
	}

	if _, err := searcher.Search(context.Background(), Query{
		Text:    "query",
		Domains: []string{"https://www.Go.Dev/", "go.dev", ""},
	}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	domains := engine.query().Domains
	if len(domains) != 1 || domains[0] != "go.dev" {
		t.Fatalf("domains = %#v, want a single normalized entry", domains)
	}
}

func TestNewRequiresEnabledConfiguration(t *testing.T) {
	if _, err := New(config.WebSearchConfig{}, nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("error = %v, want ErrNotConfigured", err)
	}
	if _, err := New(config.WebSearchConfig{Enabled: true}, nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("error = %v, want ErrNotConfigured for an enabled config without engines", err)
	}
}

func TestNewRejectsUnknownEngineType(t *testing.T) {
	cfg := config.WebSearchConfig{
		Enabled: true,
		Engines: []config.WebSearchEngineConfig{{Type: "altavista"}},
	}
	if _, err := New(cfg, nil); err == nil {
		t.Fatal("expected an error for an unsupported engine type")
	}
}

func TestNewUsesConfiguredEngineSet(t *testing.T) {
	cfg := config.WebSearchConfig{
		Enabled: true,
		Engines: []config.WebSearchEngineConfig{
			{Type: "duckduckgo"},
			{ID: "bing-eu", Type: "bing", Region: "en-GB"},
		},
	}
	searcher, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	names := searcher.EngineNames()
	if len(names) != 2 || names[0] != "duckduckgo" || names[1] != "bing-eu" {
		t.Fatalf("engine names = %#v", names)
	}
}

func TestCleanTextStripsMarkup(t *testing.T) {
	cases := map[string]string{
		"<strong>Go</strong> generics &amp; more\n  done": "Go generics & more done",
		"plain text": "plain text",
		"":           "",
	}
	for input, want := range cases {
		if got := cleanText(input); got != want {
			t.Fatalf("cleanText(%q) = %q, want %q", input, got, want)
		}
	}
}
