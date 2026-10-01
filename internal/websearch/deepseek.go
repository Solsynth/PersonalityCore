package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// deepseekDefaultBaseURL is DeepSeek's Anthropic-compatible endpoint, used
	// when the provider entry has no baseUrl. The engine appends /v1/messages,
	// the same path the Anthropic SDK builds from this base.
	deepseekDefaultBaseURL = "https://api.deepseek.com/anthropic"
	// deepseekAnthropicSuffix is what the provider's chat base is extended with
	// to reach the Anthropic-compatible endpoint.
	deepseekAnthropicSuffix = "/anthropic"
	// deepseekModel is the model this engine calls. The search runs on
	// DeepSeek's side and this engine discards the model's answer, so the model
	// only decides when to search: deepseek-flash is the one DeepSeek
	// documents for search and the cheapest of them, though the API runs the
	// tool on its larger models too.
	deepseekModel = "deepseek-flash"
	// deepseekAnthropicVersion is the Anthropic API version the compatible
	// endpoint expects.
	deepseekAnthropicVersion = "2023-06-01"
	// deepseekWebSearchTool is the server-side web search tool Anthropic
	// defines. DeepSeek executes it; the request only carries the definition.
	deepseekWebSearchTool = "web_search_20250305"
	// deepseekMaxSearches caps the searches one query may trigger. A search
	// backend is asked for results, not for research, so one search keeps both
	// latency and the provider's per-search charge bounded.
	deepseekMaxSearches = 1
	// deepseekMaxTokens bounds the model's own answer, which the engine drops.
	// The search result blocks it needs are produced before that answer.
	deepseekMaxTokens = 1024
	// deepseekMinTimeout replaces the shared per-engine timeout when it is left
	// at its 15s default: a server-side search plus the model turn around it
	// routinely takes longer, and a search backend that times out every query
	// is worse than a slow one. An explicitly longer timeout is honoured.
	deepseekMinTimeout = 60 * time.Second
	// deepseekMaxContinuations caps pause_turn continuations, the server-side
	// loop Anthropic uses for long-running searches.
	deepseekMaxContinuations = 2
	// deepseekSystemPrompt steers the model into searching. Web search tools
	// are model-triggered, and a query that looks answerable from memory would
	// otherwise come back with no results at all.
	deepseekSystemPrompt = "You are a web search backend. Always call the web_search tool for the request; never answer from memory."
)

// deepseekEngine queries DeepSeek's Anthropic-compatible Messages API, which
// runs Anthropic's server-side web search tool on DeepSeek's infrastructure.
//
// It is the API-backed option that needs no separate search vendor: one
// provider account answers from any egress and supplies both the search and
// the summarization, billed per model token and per search. Its key and
// endpoint come from the [[providers]] entry named by the engine's `provider`,
// so a DeepSeek account is configured once. DeepSeek's own Responses API
// ignores built-in web search tools, so this engine talks to the
// Anthropic-format endpoint instead.
//
// Results arrive as the search hits themselves — title and URL, plus a page age
// and encrypted content this engine has no use for — and any citations the
// model attached to its answer. A citation's cited_text is the only snippet text
// the API exposes and it is often absent, so hits usually reach the caller
// without a snippet until the crawler fills it from the page. A domain-scoped
// query is filtered here because DeepSeek accepts the web search tool's
// allowed_domains field but ignores it.
// deepseekAnthropicBase turns the provider entry's base URL into the endpoint
// this engine calls. A [[providers]] entry points at DeepSeek's chat API
// (https://api.deepseek.com), while the server-side web search tool is only
// served by the Anthropic-compatible endpoint beside it.
func deepseekAnthropicBase(providerBase string) string {
	base := strings.TrimRight(strings.TrimSpace(providerBase), "/")
	switch {
	case base == "":
		return deepseekDefaultBaseURL
	case strings.HasSuffix(base, deepseekAnthropicSuffix):
		return base
	default:
		return base + deepseekAnthropicSuffix
	}
}

type deepseekEngine struct {
	id       string
	provider string
	apiKey   string
	baseURL  string
	client   *http.Client
}

// Metered reports that this engine spends provider tokens per query: the search
// runs on DeepSeek's side, and the provider bills the input it assembles and
// the answer it writes even though the engine discards that answer.
func (e *deepseekEngine) Metered() bool { return true }

func (e *deepseekEngine) Name() string { return e.id }

// deepseekRequest is the subset of the Messages API this engine sets.
type deepseekRequest struct {
	Model     string            `json:"model"`
	MaxTokens int               `json:"max_tokens"`
	Thinking  deepseekThinking  `json:"thinking"`
	System    string            `json:"system"`
	Messages  []deepseekMessage `json:"messages"`
	Tools     []deepseekWebTool `json:"tools"`
}

// deepseekThinking disables the reasoning pass. A search backend pays for the
// chain of thought without ever using it.
type deepseekThinking struct {
	Type string `json:"type"`
}

type deepseekMessage struct {
	Role string `json:"role"`
	// Content is the raw JSON of either a content block array or a string, so
	// a paused turn can be replayed back to the API unchanged.
	Content json.RawMessage `json:"content"`
}

type deepseekWebTool struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	MaxUses int    `json:"max_uses,omitempty"`
}

type deepseekResponse struct {
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      deepseekUsage     `json:"usage"`
}

// deepseekUsage is the provider's accounting for one request. Cache reads are
// input the provider served from its own prompt cache: they are counted with
// the fresh input because a pricing entry carries one input rate.
type deepseekUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	ServerToolUse            struct {
		WebSearchRequests int `json:"web_search_requests"`
	} `json:"server_tool_use"`
}

func (u deepseekUsage) promptTokens() int {
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

func (u *deepseekUsage) add(other deepseekUsage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheReadInputTokens += other.CacheReadInputTokens
	u.CacheCreationInputTokens += other.CacheCreationInputTokens
	u.ServerToolUse.WebSearchRequests += other.ServerToolUse.WebSearchRequests
}

// deepseekTextBlock is a model-authored block. Its citations carry the snippet
// text the search result blocks themselves do not.
type deepseekTextBlock struct {
	Citations []struct {
		URL       string `json:"url"`
		CitedText string `json:"cited_text"`
	} `json:"citations"`
}

type deepseekResultBlock struct {
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

type deepseekSearchResult struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type deepseekSearchFailure struct {
	ErrorCode string `json:"error_code"`
}

func (e *deepseekEngine) Search(ctx context.Context, query Query) (EngineResponse, error) {
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": query.Text}})
	if err != nil {
		return EngineResponse{}, err
	}
	request := deepseekRequest{
		Model:     deepseekModel,
		MaxTokens: deepseekMaxTokens,
		Thinking:  deepseekThinking{Type: "disabled"},
		System:    deepseekSystemPrompt,
		Messages:  []deepseekMessage{{Role: "user", Content: content}},
		Tools: []deepseekWebTool{{
			Type:    deepseekWebSearchTool,
			Name:    "web_search",
			MaxUses: deepseekMaxSearches,
		}},
	}

	// Every request the query needs is billed, so the accounting accumulates
	// over a paused turn and its continuations.
	var spent deepseekUsage
	for attempt := 0; ; attempt++ {
		response, err := e.post(ctx, request)
		if err != nil {
			return EngineResponse{}, err
		}
		spent.add(response.Usage)
		rows, diagnostic := deepseekRows(response.Content)
		if len(rows) > 0 {
			if len(query.Domains) > 0 {
				domains := normalizeDomains(query.Domains)
				rows = restrictToDomains(rows, domains)
				if len(rows) == 0 {
					return EngineResponse{}, fmt.Errorf("deepseek returned nothing inside the requested domains (%s)", strings.Join(domains, ", "))
				}
			}
			collected, err := collect(e.id, query, rows)
			if err != nil {
				return EngineResponse{}, err
			}
			return EngineResponse{Results: collected, Usage: e.usage(spent)}, nil
		}
		// A paused turn stopped before the search finished. Replaying the
		// assistant turn unchanged resumes it.
		if response.StopReason != "pause_turn" || attempt >= deepseekMaxContinuations {
			if diagnostic != "" {
				return EngineResponse{}, fmt.Errorf("deepseek web search failed: %s", diagnostic)
			}
			return EngineResponse{}, nil
		}
		content, err := json.Marshal(response.Content)
		if err != nil {
			return EngineResponse{}, err
		}
		request.Messages = append(request.Messages, deepseekMessage{Role: "assistant", Content: content})
	}
}

// usage reports what the query spent on the provider, or nil when nothing was
// billed for it.
func (e *deepseekEngine) usage(spent deepseekUsage) *Usage {
	prompt := spent.promptTokens()
	if prompt == 0 && spent.OutputTokens == 0 && spent.ServerToolUse.WebSearchRequests == 0 {
		return nil
	}
	return &Usage{
		Provider:     e.provider,
		Model:        deepseekModel,
		InputTokens:  prompt,
		OutputTokens: spent.OutputTokens,
		Searches:     spent.ServerToolUse.WebSearchRequests,
	}
}

// post sends one Messages API request and decodes the response.
func (e *deepseekEngine) post(ctx context.Context, body deepseekRequest) (*deepseekResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequest(http.MethodPost, e.baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-key", e.apiKey)
	request.Header.Set("anthropic-version", deepseekAnthropicVersion)

	var response deepseekResponse
	if err := doJSON(ctx, e.client, request, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// restrictToDomains keeps only the hits served from one of the domains.
//
// The web search tool's own allowed_domains field is accepted but ignored by
// DeepSeek — a probe returns hits from unrelated hosts — so a caller that asks
// for domains is filtered here instead of being handed a set that silently
// ignores the constraint.
func restrictToDomains(rows []Result, domains []string) []Result {
	kept := make([]Result, 0, len(rows))
	for _, row := range rows {
		for _, domain := range domains {
			if urlInDomain(row.URL, domain) {
				kept = append(kept, row)
				break
			}
		}
	}
	return kept
}

// urlInDomain reports whether rawURL is served from domain, which may carry a
// path prefix and covers its subdomains.
func urlInDomain(rawURL, domain string) bool {
	host, path, _ := strings.Cut(strings.ToLower(strings.TrimSpace(domain)), "/")
	if host == "" {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return false
	}
	served := strings.TrimPrefix(strings.ToLower(parsed.Host), "www.")
	if served != host && !strings.HasSuffix(served, "."+host) {
		return false
	}
	if path == "" {
		return true
	}
	return strings.HasPrefix(strings.TrimPrefix(parsed.Path, "/"), path)
}

// deepseekRows extracts the search hits from a response. Search failures are
// reported by an error object where results would be, and citations are folded
// into the hits they point at: diagnostic is non-empty when the search itself
// failed, and is only meaningful when no hit was found.
func deepseekRows(blocks []json.RawMessage) ([]Result, string) {
	// Citations are carried by the model's answer, which follows the search
	// result blocks it cites, so snippets are attached after the whole response
	// has been read.
	var (
		rows       []Result
		citations  = make(map[string]string)
		diagnostic string
	)
	for _, raw := range blocks {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			continue
		}
		switch header.Type {
		case "text":
			var block deepseekTextBlock
			if err := json.Unmarshal(raw, &block); err != nil {
				continue
			}
			for _, citation := range block.Citations {
				key := normalizeURL(citation.URL)
				if key == "" || strings.TrimSpace(citation.CitedText) == "" {
					continue
				}
				if _, seen := citations[key]; !seen {
					citations[key] = citation.CitedText
				}
			}
		case "web_search_tool_result":
			var block deepseekResultBlock
			if err := json.Unmarshal(raw, &block); err != nil {
				continue
			}
			trimmed := bytes.TrimSpace(block.Content)
			if len(trimmed) == 0 {
				continue
			}
			if trimmed[0] == '{' {
				var failure deepseekSearchFailure
				if err := json.Unmarshal(trimmed, &failure); err == nil && failure.ErrorCode != "" {
					if diagnostic == "" {
						diagnostic = failure.ErrorCode
					}
				}
				continue
			}
			var hits []deepseekSearchResult
			if err := json.Unmarshal(trimmed, &hits); err != nil {
				continue
			}
			for _, hit := range hits {
				if strings.TrimSpace(hit.URL) == "" {
					continue
				}
				rows = append(rows, Result{Title: hit.Title, URL: hit.URL})
			}
		}
	}
	// The search hits carry no snippet text of their own, so the only text
	// available is what the model cited; the crawler replaces it with the page
	// itself when crawling is on.
	for index := range rows {
		if text := citations[normalizeURL(rows[index].URL)]; text != "" {
			rows[index].Snippet = excerptText(text, exaSnippetCharacters)
		}
	}
	return rows, diagnostic
}
