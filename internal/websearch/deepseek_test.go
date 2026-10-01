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

	"src.solsynth.dev/sosys/persona/internal/config"
)

const deepseekSearchResponse = `{
  "id": "msg_1",
  "type": "message",
  "role": "assistant",
  "content": [
    {"type": "text", "text": "I'll search for that."},
    {"type": "server_tool_use", "id": "srvtoolu_1", "name": "web_search", "input": {"query": "postgres 18 async io"}},
    {"type": "web_search_tool_result", "tool_use_id": "srvtoolu_1", "content": [
      {"type": "web_search_result", "url": "https://www.postgresql.org/docs/current/release-18.html",
       "title": "PostgreSQL 18 release notes", "page_age": "September 25, 2025"},
      {"type": "web_search_result", "url": "https://example.com/postgres-18-async", "title": ""}
    ]},
    {"type": "text", "text": "PostgreSQL 18 adds asynchronous I/O.",
     "citations": [
       {"type": "web_search_result_location", "url": "https://www.postgresql.org/docs/current/release-18.html",
        "title": "PostgreSQL 18 release notes",
        "cited_text": "An asynchronous I/O (AIO) subsystem improves the performance of sequential scans."}
     ]}
  ],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 3386, "output_tokens": 578, "cache_read_input_tokens": 256,
            "cache_creation_input_tokens": 64, "server_tool_use": {"web_search_requests": 1}}
}`

func TestDeepSeekEngineRunsServerSideSearch(t *testing.T) {
	var (
		mu       sync.Mutex
		captured *http.Request
		body     map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		captured = r.Clone(context.Background())
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, deepseekSearchResponse)
	}))
	t.Cleanup(server.Close)

	// BaseURL is the provider entry's chat base; the engine reaches the
	// Anthropic-compatible search endpoint next to it.
	engine, err := newEngine(config.WebSearchEngineConfig{
		Type: "deepseek", APIKey: "sk-test", BaseURL: server.URL, Provider: "deepseek", Price: "1",
	}, config.WebSearchConfig{})
	if err != nil {
		t.Fatalf("newEngine() error = %v", err)
	}

	response, err := engine.Search(context.Background(), Query{
		Text:    "postgres 18 async io",
		Limit:   5,
		Domains: []string{"https://www.postgresql.org/"},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if captured.URL.Path != "/anthropic/v1/messages" {
		t.Fatalf("path = %q, want /anthropic/v1/messages", captured.URL.Path)
	}
	if captured.Header.Get("x-api-key") != "sk-test" || captured.Header.Get("anthropic-version") == "" {
		t.Fatalf("unexpected headers: %#v", captured.Header)
	}
	// The model is fixed: it only decides when to search, and this is the one
	// DeepSeek documents for search.
	if body["model"] != deepseekModel || body["system"] == "" {
		t.Fatalf("unexpected request body: %#v", body)
	}
	if thinking, ok := body["thinking"].(map[string]any); !ok || thinking["type"] != "disabled" {
		t.Fatalf("thinking must be disabled for a search backend: %#v", body["thinking"])
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v, want the web search tool", body["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["type"] != "web_search_20250305" || tool["name"] != "web_search" {
		t.Fatalf("unexpected tool definition: %#v", tool)
	}
	if tool["max_uses"] != float64(1) {
		t.Fatalf("max_uses = %#v, want 1", tool["max_uses"])
	}
	// DeepSeek ignores the tool's domain filter, so the engine must not send
	// one and claim the restriction was applied.
	if _, ok := tool["allowed_domains"]; ok {
		t.Fatalf("allowed_domains must not be sent: %#v", tool)
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %#v, want one user turn", body["messages"])
	}
	if turn, ok := messages[0].(map[string]any); !ok || turn["role"] != "user" {
		t.Fatalf("unexpected user turn: %#v", messages[0])
	}

	// The result without a title is dropped; the other carries the model's
	// citation as its snippet, which is all the API exposes.
	if len(response.Results) != 1 {
		t.Fatalf("got %d results, want 1: %#v", len(response.Results), response.Results)
	}
	hit := response.Results[0]
	if hit.Provider != "deepseek" || hit.Title != "PostgreSQL 18 release notes" {
		t.Fatalf("unexpected result: %#v", hit)
	}
	if !strings.Contains(hit.Snippet, "asynchronous I/O") {
		t.Fatalf("snippet = %q, want the cited text", hit.Snippet)
	}

	// The provider's accounting is what the caller is billed for: both cache
	// reads and fresh input count as input tokens.
	usage := response.Usage
	if usage == nil {
		t.Fatal("a metered engine must report what the query spent")
	}
	if usage.Provider != "deepseek" || usage.Model != deepseekModel {
		t.Fatalf("usage must name the pricing entry: %#v", usage)
	}
	if usage.InputTokens != 3386+256+64 || usage.OutputTokens != 578 || usage.Searches != 1 {
		t.Fatalf("unexpected usage: %#v", usage)
	}
	if !engine.(MeteredEngine).Metered() {
		t.Fatal("a token-spending engine must report that it meters")
	}
}

func TestDeepSeekEngineRestrictsToRequestedDomains(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anthropic/v1/messages" {
			t.Errorf("path = %q, want /anthropic/v1/messages", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"content":[{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[
			{"type":"web_search_result","url":"https://www.postgresql.org/docs/current/release-18.html","title":"PostgreSQL 18 release notes"},
			{"type":"web_search_result","url":"https://blog.example.com/docs/current/release-18.html","title":"PostgreSQL 18 roundup"}]}],
			"stop_reason":"end_turn"}`)
	}))
	t.Cleanup(server.Close)

	engine, err := newEngine(config.WebSearchEngineConfig{Type: "deepseek", APIKey: "sk-test", BaseURL: server.URL, Price: "1"}, config.WebSearchConfig{})
	if err != nil {
		t.Fatalf("newEngine() error = %v", err)
	}

	response, err := engine.Search(context.Background(), Query{
		Text:    "postgres 18 release",
		Domains: []string{"postgresql.org"},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].URL != "https://www.postgresql.org/docs/current/release-18.html" {
		t.Fatalf("off-domain hits leaked into the results: %#v", response.Results)
	}

	// A query no hit can satisfy is an engine failure, not an empty result set:
	// the caller asked for a domain the engine cannot search within.
	_, err = engine.Search(context.Background(), Query{Text: "postgres 18 release", Domains: []string{"sqlite.org"}})
	if err == nil || !strings.Contains(err.Error(), "sqlite.org") {
		t.Fatalf("error = %v, want a domain mismatch", err)
	}
}

func TestWebSearchEngineDomainMatching(t *testing.T) {
	cases := map[string]struct {
		url    string
		domain string
		want   bool
	}{
		"same host":               {"https://postgresql.org/docs", "postgresql.org", true},
		"www prefix":              {"https://www.postgresql.org/docs", "postgresql.org", true},
		"subdomain":               {"https://docs.postgresql.org/18", "postgresql.org", true},
		"suffix without boundary": {"https://notpostgresql.org/docs", "postgresql.org", false},
		"other host":              {"https://example.com/docs", "postgresql.org", false},
		"path prefix matches":     {"https://postgresql.org/docs/18", "postgresql.org/docs", true},
		"path prefix misses":      {"https://postgresql.org/about", "postgresql.org/docs", false},
		"relative url":            {"/docs", "postgresql.org", false},
	}
	for name, testCase := range cases {
		if got := urlInDomain(testCase.url, testCase.domain); got != testCase.want {
			t.Fatalf("%s: urlInDomain(%q, %q) = %v, want %v", name, testCase.url, testCase.domain, got, testCase.want)
		}
	}
}

func TestDeepSeekEngineReportsSearchFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"content":[{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1",
			"content":{"type":"web_search_tool_result_error","error_code":"too_many_requests"}}],
			"stop_reason":"end_turn"}`)
	}))
	t.Cleanup(server.Close)

	engine, err := newEngine(config.WebSearchEngineConfig{Type: "deepseek", APIKey: "sk-test", BaseURL: server.URL, Price: "1"}, config.WebSearchConfig{})
	if err != nil {
		t.Fatalf("newEngine() error = %v", err)
	}
	_, err = engine.Search(context.Background(), Query{Text: "postgres 18 async io"})
	if err == nil || !strings.Contains(err.Error(), "too_many_requests") {
		t.Fatalf("error = %v, want the search error code", err)
	}
}

func TestDeepSeekEngineResumesPausedSearch(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		mu.Lock()
		requests = append(requests, body)
		attempt := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			io.WriteString(w, `{"content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"postgres 18"}}],
				"stop_reason":"pause_turn"}`)
			return
		}
		io.WriteString(w, `{"content":[{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[
			{"type":"web_search_result","url":"https://www.postgresql.org/docs/current/release-18.html","title":"PostgreSQL 18 release notes"}]}],
			"stop_reason":"end_turn"}`)
	}))
	t.Cleanup(server.Close)

	engine, err := newEngine(config.WebSearchEngineConfig{Type: "deepseek", APIKey: "sk-test", BaseURL: server.URL, Price: "1"}, config.WebSearchConfig{})
	if err != nil {
		t.Fatalf("newEngine() error = %v", err)
	}
	response, err := engine.Search(context.Background(), Query{Text: "postgres 18 async io"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].Title != "PostgreSQL 18 release notes" {
		t.Fatalf("unexpected results: %#v", response.Results)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2", len(requests))
	}
	messages, ok := requests[1]["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("second request messages = %#v, want the assistant turn replayed", requests[1]["messages"])
	}
	if turn, ok := messages[1].(map[string]any); !ok || turn["role"] != "assistant" {
		t.Fatalf("second request did not replay the assistant turn: %#v", messages[1])
	}
}

func TestDeepSeekEngineRequiresAPIKey(t *testing.T) {
	if _, err := newEngine(config.WebSearchEngineConfig{Type: "deepseek", Price: "1"}, config.WebSearchConfig{}); err == nil {
		t.Fatal("expected an error when an API engine has no key")
	}
}

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
}

func TestDeepSeekEngineDerivesAnthropicEndpoint(t *testing.T) {
	cases := map[string][2]string{
		"chat base":              {"https://api.deepseek.com", "https://api.deepseek.com/anthropic"},
		"trailing slash":         {"https://api.deepseek.com/", "https://api.deepseek.com/anthropic"},
		"already anthropic":      {"https://api.deepseek.com/anthropic", "https://api.deepseek.com/anthropic"},
		"proxy with a path":      {"https://proxy.example/deepseek", "https://proxy.example/deepseek/anthropic"},
		"provider without base":  {"", deepseekDefaultBaseURL},
		"whitespace around base": {"  https://api.deepseek.com  ", "https://api.deepseek.com/anthropic"},
	}
	for name, testCase := range cases {
		if got := deepseekAnthropicBase(testCase[0]); got != testCase[1] {
			t.Fatalf("%s: deepseekAnthropicBase(%q) = %q, want %q", name, testCase[0], got, testCase[1])
		}
	}
}
