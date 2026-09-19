package websearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"src.solsynth.dev/sosys/persona/internal/config"
)

func TestTavilyEngineMapsRequestAndResponse(t *testing.T) {
	var (
		captured *http.Request
		body     map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Clone(context.Background())
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[
			{"title":"PostgreSQL 18 AIO","url":"https://www.postgresql.org/docs/current/release-18.html",
			 "content":"An asynchronous I/O (AIO) subsystem that can improve performance of sequential scans.","score":0.93},
			{"title":"","url":"https://example.com/no-title"}
		]}`)
	}))
	t.Cleanup(server.Close)

	engine, err := newEngine(config.WebSearchEngineConfig{
		Type: "tavily", APIKey: "tvly-key", BaseURL: server.URL, Price: "0.5",
	}, config.WebSearchConfig{})
	if err != nil {
		t.Fatalf("newEngine() error = %v", err)
	}

	results, err := engine.Search(context.Background(), Query{
		Text:      "postgres 18 async io",
		Limit:     3,
		Freshness: FreshnessWeek,
		Domains:   []string{"postgresql.org"},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if got := captured.Header.Get("Authorization"); got != "Bearer tvly-key" {
		t.Fatalf("Authorization = %q, want a bearer token", got)
	}
	if body["api_key"] != "tvly-key" || body["query"] != "postgres 18 async io" {
		t.Fatalf("unexpected request body: %#v", body)
	}
	if body["max_results"] != float64(3) || body["time_range"] != "week" || body["search_depth"] != "basic" {
		t.Fatalf("unexpected request body: %#v", body)
	}
	if domains, ok := body["include_domains"].([]any); !ok || len(domains) != 1 || domains[0] != "postgresql.org" {
		t.Fatalf("include_domains = %#v", body["include_domains"])
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1: %#v", len(results), results)
	}
	if results[0].Provider != "tavily" || !strings.Contains(results[0].Snippet, "asynchronous I/O") {
		t.Fatalf("unexpected result: %#v", results[0])
	}
}

func TestTavilyEngineRequiresAPIKey(t *testing.T) {
	if _, err := newEngine(config.WebSearchEngineConfig{Type: "tavily", Price: "1"}, config.WebSearchConfig{}); err == nil {
		t.Fatal("expected an error when an API engine has no key")
	}
}

func TestTavilyEngineReportsAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"detail":"rate limit exceeded"}`)
	}))
	t.Cleanup(server.Close)

	engine, err := newEngine(config.WebSearchEngineConfig{Type: "tavily", APIKey: "k", BaseURL: server.URL, Price: "1"}, config.WebSearchConfig{})
	if err != nil {
		t.Fatalf("newEngine() error = %v", err)
	}
	_, err = engine.Search(context.Background(), Query{Text: "query"})
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("error = %v, want a rate limit error", err)
	}
}
