package service

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/schema"

	"src.solsynth.dev/sosys/persona/internal/websearch"
)

// webSearchToolName is the model-facing tool. It is auto-loaded for agents that
// declare the web_search ability and cannot be activated by any other agent.
const webSearchToolName = "web_search"

// WebSearchEngine is the provider-agnostic search backend behind the tool and
// the HTTP endpoint. It is an interface so both surfaces stay testable without
// upstream calls.
type WebSearchEngine interface {
	Search(ctx context.Context, query websearch.Query) (*websearch.Response, error)
	// Billed reports whether any configured engine charges per query.
	Billed() bool
}

// WebSearchInput is the request shape shared by the tool and the HTTP endpoint.
type WebSearchInput struct {
	Query     string   `json:"query"`
	Limit     int      `json:"limit,omitempty"`
	Freshness string   `json:"freshness,omitempty"`
	Domains   []string `json:"domains,omitempty"`
	Language  string   `json:"language,omitempty"`
}

// SearchWeb runs one web search for accountID. It returns
// websearch.ErrNotConfigured when the server has no web search engines, and
// websearch.ErrInvalidQuery for caller-side request problems.
//
// Engines that charge per query are authorized before the search and charged in
// golds afterwards, so the account pays for API-backed search and cached or
// scraped answers stay free.
func (s *ConversationService) SearchWeb(ctx context.Context, accountID string, input WebSearchInput) (*websearch.Response, error) {
	if s.webSearch == nil {
		return nil, websearch.ErrNotConfigured
	}
	if s.webSearch.Billed() {
		if err := s.billing.AuthorizeAction(ctx, accountID); err != nil {
			return nil, err
		}
	}
	response, err := s.webSearch.Search(ctx, websearch.Query{
		Text:      strings.TrimSpace(input.Query),
		Limit:     input.Limit,
		Freshness: websearch.Freshness(strings.ToLower(strings.TrimSpace(input.Freshness))),
		Domains:   input.Domains,
		Language:  strings.TrimSpace(input.Language),
	})
	if err != nil {
		return nil, err
	}
	if err := s.chargeWebSearch(ctx, accountID, response); err != nil {
		return nil, err
	}
	return response, nil
}

// chargeWebSearch records what the search owes. A search that ran no charged
// engine carries no charges, which is how scraped and cached answers stay free.
func (s *ConversationService) chargeWebSearch(ctx context.Context, accountID string, response *websearch.Response) error {
	if s.billing == nil || response == nil {
		return nil
	}
	for _, charge := range response.Charges {
		if err := s.billing.ChargeAction(ctx, accountID, "web_search/"+charge.Engine, charge.Amount); err != nil {
			return err
		}
	}
	return nil
}

type webSearchToolInput struct {
	Query     string   `json:"query"`
	Limit     int      `json:"limit"`
	Freshness string   `json:"freshness"`
	Domains   []string `json:"domains"`
}

func (s *ConversationService) webSearchToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: webSearchToolName,
		Desc: "Search the public web. Returns ranked results with a title, URL, and snippet for each hit.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Type:     schema.String,
				Desc:     "The search query.",
				Required: true,
			},
			"limit": {
				Type:     schema.Integer,
				Desc:     "Maximum number of results to return.",
				Required: false,
			},
			"freshness": {
				Type:     schema.String,
				Desc:     "Restrict results to a recency window.",
				Enum:     []string{"day", "week", "month", "year"},
				Required: false,
			},
			"domains": {
				Type: schema.Array,
				ElemInfo: &schema.ParameterInfo{
					Type: schema.String,
				},
				Desc:     "Restrict results to these domains, for example example.com.",
				Required: false,
			},
		}),
	}
}

func isWebSearchToolName(name string) bool {
	return name == webSearchToolName
}

// executeWebSearchToolCall reports upstream failures to the model as tool
// output instead of aborting the run: a failed search is recoverable, the model
// can retry with another query.
func (s *ConversationService) executeWebSearchToolCall(ctx context.Context, accountID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input webSearchToolInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	response, err := s.SearchWeb(ctx, accountID, WebSearchInput{
		Query:     input.Query,
		Limit:     input.Limit,
		Freshness: input.Freshness,
		Domains:   input.Domains,
	})
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{
		"ok":      true,
		"query":   response.Query,
		"results": response.Results,
	})
}
