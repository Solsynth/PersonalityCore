package websearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"src.solsynth.dev/sosys/persona/internal/config"
)

func TestExaEngineMapsRequestAndResponse(t *testing.T) {
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
			 "text":"An asynchronous I/O (AIO) subsystem that can improve performance of sequential scans and vacuum.",
			 "publishedDate":"2026-09-01T00:00:00.000Z"},
			{"title":"","url":"https://example.com/empty-title"},
			{"title":"Engine link","url":"https://exa.ai/pricing"}
		]}`)
	}))
	t.Cleanup(server.Close)

	engine, err := newEngine(config.WebSearchEngineConfig{Type: "exa", APIKey: "exa-key", BaseURL: server.URL}, config.WebSearchConfig{})
	if err != nil {
		t.Fatalf("newEngine() error = %v", err)
	}

	results, err := engine.Search(context.Background(), Query{
		Text:      "postgres 18 async io",
		Limit:     4,
		Freshness: FreshnessWeek,
		Domains:   []string{"postgresql.org"},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if got := captured.Header.Get("x-api-key"); got != "exa-key" {
		t.Fatalf("x-api-key = %q, want the configured key", got)
	}
	if body["query"] != "postgres 18 async io" || body["numResults"] != float64(4) {
		t.Fatalf("unexpected request body: %#v", body)
	}
	if domains, ok := body["includeDomains"].([]any); !ok || len(domains) != 1 || domains[0] != "postgresql.org" {
		t.Fatalf("includeDomains = %#v", body["includeDomains"])
	}
	if _, ok := body["startPublishedDate"].(string); !ok {
		t.Fatalf("a freshness window must map to startPublishedDate, got %#v", body["startPublishedDate"])
	}
	contents, ok := body["contents"].(map[string]any)
	if !ok {
		t.Fatalf("contents missing from request: %#v", body)
	}
	if _, ok := contents["text"]; !ok {
		t.Fatalf("page text must be requested so results carry a snippet: %#v", contents)
	}

	// The result without a title and the engine's own link are dropped.
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1: %#v", len(results), results)
	}
	if results[0].Provider != "exa" || !strings.Contains(results[0].Snippet, "asynchronous I/O") {
		t.Fatalf("unexpected result: %#v", results[0])
	}
}

func TestExaEngineRequiresAPIKey(t *testing.T) {
	if _, err := newEngine(config.WebSearchEngineConfig{Type: "exa"}, config.WebSearchConfig{}); err == nil {
		t.Fatal("expected an error when an API engine has no key")
	}
}

func TestExaPublishedAfter(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	if got := exaPublishedAfter("", now); got != "" {
		t.Fatalf("no freshness must not set a date floor, got %q", got)
	}
	if got := exaPublishedAfter(FreshnessDay, now); got != "2026-09-18T12:00:00Z" {
		t.Fatalf("day floor = %q", got)
	}
	if got := exaPublishedAfter(FreshnessYear, now); got != "2025-09-19T12:00:00Z" {
		t.Fatalf("year floor = %q", got)
	}
}

func TestExaEngineReportsAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"invalid api key"}`)
	}))
	t.Cleanup(server.Close)

	engine, err := newEngine(config.WebSearchEngineConfig{Type: "exa", APIKey: "bad", BaseURL: server.URL}, config.WebSearchConfig{})
	if err != nil {
		t.Fatalf("newEngine() error = %v", err)
	}
	if _, err := engine.Search(context.Background(), Query{Text: "query"}); err == nil {
		t.Fatal("expected an error for a rejected key")
	}
}
