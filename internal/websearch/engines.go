package websearch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"src.solsynth.dev/sosys/persona/internal/config"
)

const (
	// defaultResultLimit applies when a caller sends no limit.
	defaultResultLimit = 5
	// hardResultLimit caps engine requests regardless of configuration.
	hardResultLimit = 20
	defaultTimeout  = 15 * time.Second
	// maxResponseBytes bounds how much of an engine response is read.
	maxResponseBytes = 4 << 20
	// defaultUserAgent is the browser identity used for engine requests. Search
	// engines serve a bot wall to obviously automated clients.
	defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"
)

// newEngine builds one engine from configuration and validates the parts that
// are specific to its type.
func newEngine(cfg config.WebSearchEngineConfig, root config.WebSearchConfig) (Engine, error) {
	engineType := strings.ToLower(strings.TrimSpace(cfg.Type))
	id := strings.TrimSpace(cfg.ID)
	if id == "" {
		id = engineType
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = root.Timeout
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	userAgent := strings.TrimSpace(root.UserAgent)
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	language := strings.TrimSpace(root.Language)
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	region := strings.TrimSpace(cfg.Region)

	client := &http.Client{Timeout: timeout}

	switch engineType {
	case "duckduckgo":
		if base == "" {
			base = "https://html.duckduckgo.com"
		}
		return &duckDuckGoEngine{id: id, baseURL: base, region: region, userAgent: userAgent, language: language, client: client}, nil
	case "bing":
		if base == "" {
			base = "https://www.bing.com"
		}
		return &bingEngine{id: id, baseURL: base, region: region, userAgent: userAgent, language: language, client: client}, nil
	case "google":
		if base == "" {
			base = "https://www.google.com"
		}
		return &googleEngine{id: id, baseURL: base, region: region, userAgent: userAgent, language: language, client: client}, nil
	case "tavily":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, fmt.Errorf("web search engine %q requires apiKey", id)
		}
		baseURL := strings.TrimSpace(cfg.BaseURL)
		if baseURL == "" {
			baseURL = "https://api.tavily.com/search"
		}
		return &tavilyEngine{id: id, apiKey: strings.TrimSpace(cfg.APIKey), baseURL: baseURL, client: client}, nil
	case "exa":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, fmt.Errorf("web search engine %q requires apiKey", id)
		}
		baseURL := strings.TrimSpace(cfg.BaseURL)
		if baseURL == "" {
			baseURL = "https://api.exa.ai/search"
		}
		return &exaEngine{id: id, apiKey: strings.TrimSpace(cfg.APIKey), baseURL: baseURL, client: client}, nil
	default:
		return nil, fmt.Errorf("web search engine %q uses unsupported type %q", id, cfg.Type)
	}
}

// fetchHTML performs one browser-shaped GET and returns the response body.
// Redirects are followed: Bing answers a bare query with a 302 before serving
// results. Only 200 carries results; engines answer a throttled or challenged
// request with other statuses (DuckDuckGo replies 202 with an anomaly page), so
// those are reported as engine failures instead of an empty result set.
func fetchHTML(ctx context.Context, client *http.Client, endpoint *url.URL, userAgent, language string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	request.Header.Set("Accept-Language", acceptLanguage(language))

	response, err := client.Do(request.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("engine returned %s%s", response.Status, throttleHint(response.StatusCode))
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("engine returned an empty response")
	}
	return body, nil
}

// throttleHint explains the statuses engines use for rate limiting or bot
// challenges, which a caller should treat as "slow down", not "no results".
func throttleHint(status int) string {
	switch status {
	case http.StatusAccepted, http.StatusForbidden, http.StatusTooManyRequests:
		return " (rate limited or challenged; retry later)"
	default:
		return ""
	}
}

func acceptLanguage(language string) string {
	language = strings.TrimSpace(language)
	if language == "" {
		return "en-US,en;q=0.9"
	}
	if strings.Contains(language, "-") && !strings.Contains(language, ";") {
		return language + "," + strings.SplitN(language, "-", 2)[0] + ";q=0.9"
	}
	return language
}

// collect normalizes engine output: it drops unusable rows, removes duplicates
// the engine itself returned, honours the requested limit, and rejects a result
// set that shares no term with the query.
func collect(engineID string, query Query, rows []Result) ([]Result, error) {
	limit := query.Limit
	seen := make(map[string]bool, len(rows))
	collected := make([]Result, 0, len(rows))
	for _, row := range rows {
		row.Title = cleanText(row.Title)
		row.URL = strings.TrimSpace(row.URL)
		row.Snippet = cleanText(row.Snippet)
		if row.Title == "" || isSkippableURL(row.URL) {
			continue
		}
		key := normalizeURL(row.URL)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		row.Provider = engineID
		collected = append(collected, row)
		if len(collected) >= limit {
			break
		}
	}
	if len(collected) > 0 {
		if err := relevanceGuard(collected, query); err != nil {
			return nil, fmt.Errorf("engine returned results unrelated to the query: %w", err)
		}
	}
	return collected, nil
}

// emptyOrBlocked reports why an engine produced no rows: a bot wall, a consent
// interstitial, or a JavaScript shell is an error, while a genuinely empty
// result set is not.
func emptyOrBlocked(body []byte) error {
	if marker := detectBlock(body); marker != "" {
		return fmt.Errorf("engine served a bot wall (%s)", marker)
	}
	return nil
}

// ─── DuckDuckGo ────────────────────────────────────────────────────

type duckDuckGoEngine struct {
	id        string
	baseURL   string
	region    string
	userAgent string
	language  string
	client    *http.Client
}

func (e *duckDuckGoEngine) Name() string { return e.id }

func (e *duckDuckGoEngine) Search(ctx context.Context, query Query) ([]Result, error) {
	endpoint, err := url.Parse(e.baseURL + "/html/")
	if err != nil {
		return nil, fmt.Errorf("invalid duckduckgo base url: %w", err)
	}
	values := endpoint.Query()
	values.Set("q", buildQueryText(query))
	values.Set("kl", e.region)
	if dateFilter := duckDuckGoDateFilter(query.Freshness); dateFilter != "" {
		values.Set("df", dateFilter)
	}
	endpoint.RawQuery = values.Encode()

	body, err := fetchHTML(ctx, e.client, endpoint, e.userAgent, e.language)
	if err != nil {
		return nil, err
	}
	doc := parseHTML(body)
	if doc == nil {
		return nil, fmt.Errorf("decode response")
	}

	var rows []Result
	for _, block := range findAll(doc, func(node *html.Node) bool {
		return classContains(node, "result__body") || classContains(node, "results_links")
	}) {
		title := findByClassSubstring(block, "a", "result__a")
		if title == nil {
			continue
		}
		snippet := findByClassSubstring(block, "", "result__snippet")
		rows = append(rows, Result{
			Title:   elementText(title),
			URL:     absoluteURL(endpoint, attr(title, "href")),
			Snippet: elementText(snippet),
		})
	}
	if len(rows) == 0 {
		if err := emptyOrBlocked(body); err != nil {
			return nil, err
		}
	}
	collected, err := collect(e.id, query, rows)
	if err != nil {
		return nil, err
	}
	return collected, nil
}

// duckDuckGoDateFilter maps the shared freshness window onto DuckDuckGo's
// `df` parameter.
func duckDuckGoDateFilter(freshness Freshness) string {
	switch freshness {
	case FreshnessDay:
		return "d"
	case FreshnessWeek:
		return "w"
	case FreshnessMonth:
		return "m"
	case FreshnessYear:
		return "y"
	default:
		return ""
	}
}

// ─── Bing ──────────────────────────────────────────────────────────

type bingEngine struct {
	id        string
	baseURL   string
	region    string
	userAgent string
	language  string
	client    *http.Client
}

func (e *bingEngine) Name() string { return e.id }

func (e *bingEngine) Search(ctx context.Context, query Query) ([]Result, error) {
	endpoint, err := url.Parse(e.baseURL + "/search")
	if err != nil {
		return nil, fmt.Errorf("invalid bing base url: %w", err)
	}
	values := endpoint.Query()
	values.Set("q", buildQueryText(query))
	values.Set("count", strconv.Itoa(query.Limit))
	if e.region != "" {
		values.Set("mkt", e.region)
	}
	if e.language != "" {
		values.Set("setlang", e.language)
	}
	if filter := bingDateFilter(query.Freshness); filter != "" {
		values.Set("filters", filter)
	}
	endpoint.RawQuery = values.Encode()

	body, err := fetchHTML(ctx, e.client, endpoint, e.userAgent, e.language)
	if err != nil {
		return nil, err
	}
	doc := parseHTML(body)
	if doc == nil {
		return nil, fmt.Errorf("decode response")
	}

	var rows []Result
	for _, block := range findAll(doc, func(node *html.Node) bool { return hasClass(node, "b_algo") }) {
		heading := findByClass(block, "h2", "")
		link := firstLink(heading)
		if link == nil {
			continue
		}
		snippet := findByClass(block, "p", "")
		rows = append(rows, Result{
			Title:   elementText(link),
			URL:     absoluteURL(endpoint, attr(link, "href")),
			Snippet: elementText(snippet),
		})
	}
	if len(rows) == 0 {
		if err := emptyOrBlocked(body); err != nil {
			return nil, err
		}
	}
	collected, err := collect(e.id, query, rows)
	if err != nil {
		return nil, err
	}
	return collected, nil
}

// bingDateFilter maps the shared freshness window onto the filter Bing's own
// date-range UI produces. ez1 is the last 24 hours, ez2 the last week, ez3 the
// last month, and ez5 the last year.
func bingDateFilter(freshness Freshness) string {
	code := ""
	switch freshness {
	case FreshnessDay:
		code = "ez1"
	case FreshnessWeek:
		code = "ez2"
	case FreshnessMonth:
		code = "ez3"
	case FreshnessYear:
		code = "ez5"
	}
	if code == "" {
		return ""
	}
	return `ex1:"` + code + `"`
}

// ─── Google ────────────────────────────────────────────────────────

type googleEngine struct {
	id        string
	baseURL   string
	region    string
	userAgent string
	language  string
	client    *http.Client
}

func (e *googleEngine) Name() string { return e.id }

// Search scrapes the classic result page. Google serves a JavaScript shell or a
// consent interstitial to many server-side clients, in which case Search
// reports an engine error rather than an empty result set. Google is therefore
// not part of the default engine set: enable it only where the egress is known
// to receive real result pages.
func (e *googleEngine) Search(ctx context.Context, query Query) ([]Result, error) {
	endpoint, err := url.Parse(e.baseURL + "/search")
	if err != nil {
		return nil, fmt.Errorf("invalid google base url: %w", err)
	}
	values := endpoint.Query()
	values.Set("q", buildQueryText(query))
	values.Set("num", strconv.Itoa(query.Limit))
	if e.language != "" {
		values.Set("hl", e.language)
	}
	if e.region != "" {
		values.Set("gl", strings.ToLower(strings.SplitN(e.region, "-", 2)[0]))
	}
	if window := googleDateFilter(query.Freshness); window != "" {
		values.Set("tbs", "qdr:"+window)
	}
	endpoint.RawQuery = values.Encode()

	body, err := fetchHTML(ctx, e.client, endpoint, e.userAgent, e.language)
	if err != nil {
		return nil, err
	}
	doc := parseHTML(body)
	if doc == nil {
		return nil, fmt.Errorf("decode response")
	}

	// Google has no stable result container class. Titles are still marked up as
	// headings inside the result anchor, so anchors are matched on that instead.
	var rows []Result
	for _, link := range findAll(doc, func(node *html.Node) bool {
		if node.Data != "a" {
			return false
		}
		return findByClass(node, "h3", "") != nil
	}) {
		heading := findByClass(link, "h3", "")
		rows = append(rows, Result{
			Title:   elementText(heading),
			URL:     absoluteURL(endpoint, attr(link, "href")),
			Snippet: googleSnippet(link, heading),
		})
	}
	if len(rows) == 0 {
		if err := emptyOrBlocked(body); err != nil {
			return nil, err
		}
	}
	collected, err := collect(e.id, query, rows)
	if err != nil {
		return nil, err
	}
	return collected, nil
}

// googleSnippet looks for the descriptive text that Google renders next to a
// result. It walks up from the title anchor and takes the first ancestor block
// that carries text beyond the title itself, which is where the snippet lives
// across Google's markup variants.
func googleSnippet(link, heading *html.Node) string {
	ancestor := link.Parent
	for depth := 0; depth < 4 && ancestor != nil; depth++ {
		if text := textWithoutTitle(ancestor, heading); len(text) >= 40 {
			return text
		}
		ancestor = ancestor.Parent
	}
	return ""
}

// googleDateFilter maps the shared freshness window onto Google's `tbs` query
// date range.
func googleDateFilter(freshness Freshness) string {
	switch freshness {
	case FreshnessDay:
		return "d"
	case FreshnessWeek:
		return "w"
	case FreshnessMonth:
		return "m"
	case FreshnessYear:
		return "y"
	default:
		return ""
	}
}
