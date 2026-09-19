package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

// tavilyEngine queries Tavily's search API.
//
// Like Exa it is API-backed: it answers from any egress, which is what a
// datacenter deployment needs once scraped engines start challenging it, and it
// is billed per query through the configured engine price.
type tavilyEngine struct {
	id      string
	apiKey  string
	baseURL string
	client  *http.Client
}

func (e *tavilyEngine) Name() string { return e.id }

func (e *tavilyEngine) Search(ctx context.Context, query Query) ([]Result, error) {
	body := map[string]any{
		"query":        query.Text,
		"max_results":  query.Limit,
		"search_depth": "basic",
		"topic":        "general",
		// The key is sent in the body as well as the header: Tavily accepts
		// either, and self-hosted or proxied endpoints in the wild implement
		// only one of them.
		"api_key": e.apiKey,
	}
	if len(query.Domains) > 0 {
		body["include_domains"] = query.Domains
	}
	if window := tavilyTimeRange(query.Freshness); window != "" {
		body["time_range"] = window
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequest(http.MethodPost, e.baseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+e.apiKey)

	var response struct {
		Results []struct {
			Title         string  `json:"title"`
			URL           string  `json:"url"`
			Content       string  `json:"content"`
			Score         float64 `json:"score"`
			PublishedDate string  `json:"published_date"`
		} `json:"results"`
	}
	if err := doJSON(ctx, e.client, request, &response); err != nil {
		return nil, err
	}

	rows := make([]Result, 0, len(response.Results))
	for _, item := range response.Results {
		rows = append(rows, Result{
			Title:   item.Title,
			URL:     item.URL,
			Snippet: excerptText(cleanText(item.Content), exaSnippetCharacters),
		})
	}
	return collect(e.id, query, rows)
}

// tavilyTimeRange maps the shared freshness window onto Tavily's time range.
func tavilyTimeRange(freshness Freshness) string {
	switch freshness {
	case FreshnessDay, FreshnessWeek, FreshnessMonth, FreshnessYear:
		return string(freshness)
	default:
		return ""
	}
}
