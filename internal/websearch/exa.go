package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// exaSnippetCharacters is how much page text Exa is asked to return per
	// result. It gives the agent real content instead of a one-line summary, and
	// keeps the per-query cost bounded.
	exaSnippetCharacters = 800
)

// exaEngine queries Exa's search API.
//
// Scraped engines are the default because they need no account, but they answer
// a datacenter egress with bot walls and unrelated result pages. Exa is an
// API-backed engine for exactly those deployments: it answers any egress, at the
// cost of an API key and a per-query charge. Put it first with
// `webSearch.mode = "prefer"` so scraped engines only run when it returns
// nothing, and the paid engine is not billed twice per query.
type exaEngine struct {
	id      string
	apiKey  string
	baseURL string
	client  *http.Client
}

func (e *exaEngine) Name() string { return e.id }

func (e *exaEngine) Search(ctx context.Context, query Query) ([]Result, error) {
	body := map[string]any{
		"query":      query.Text,
		"numResults": query.Limit,
		"type":       "auto",
		"contents": map[string]any{
			"text": map[string]any{"maxCharacters": exaSnippetCharacters},
		},
	}
	if len(query.Domains) > 0 {
		body["includeDomains"] = query.Domains
	}
	if startDate := exaPublishedAfter(query.Freshness, time.Now()); startDate != "" {
		body["startPublishedDate"] = startDate
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
	request.Header.Set("x-api-key", e.apiKey)

	var response struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Text          string `json:"text"`
			PublishedDate string `json:"publishedDate"`
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
			Snippet: excerptText(cleanText(item.Text), exaSnippetCharacters),
		})
	}
	return collect(e.id, query, rows)
}

// exaPublishedAfter maps the shared freshness window onto the ISO timestamp Exa
// accepts as a publication-date floor.
func exaPublishedAfter(freshness Freshness, now time.Time) string {
	var window time.Duration
	switch freshness {
	case FreshnessDay:
		window = 24 * time.Hour
	case FreshnessWeek:
		window = 7 * 24 * time.Hour
	case FreshnessMonth:
		window = 30 * 24 * time.Hour
	case FreshnessYear:
		window = 365 * 24 * time.Hour
	default:
		return ""
	}
	return now.Add(-window).UTC().Format(time.RFC3339)
}

// doJSON executes request and decodes a successful JSON body into out.
func doJSON(ctx context.Context, client *http.Client, request *http.Request, out any) error {
	request = request.WithContext(ctx)
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("engine returned %s%s: %s", response.Status, throttleHint(response.StatusCode), truncate(strings.TrimSpace(string(body)), 200))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
