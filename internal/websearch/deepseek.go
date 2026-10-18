package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"src.solsynth.dev/sosys/persona/internal/logging"
)

const (
	// deepseekDefaultBaseURL is DeepSeek's API root, used when the provider
	// entry carries no baseUrl. The tool endpoints live directly under it.
	deepseekDefaultBaseURL = "https://api.deepseek.com"
	deepseekSearchPath     = "/v1/tools/search"
	deepseekFetchPath      = "/v1/tools/fetch"
	// deepseekAnthropicSuffix is an endpoint shape older web-search configs
	// pointed at, when this engine drove the Anthropic-compatible Messages API
	// beside the chat base. Nothing there is used now, so the suffix is trimmed
	// off the configured base.
	deepseekAnthropicSuffix = "/anthropic"
	// deepseekMaxResponseBytes bounds what this engine reads, above the shared
	// engine bound: it always asks the provider for page content, so a reply
	// carries whole documents rather than a list of results. The result cap
	// below keeps that bound reachable in practice.
	deepseekMaxResponseBytes = 16 << 20
	// deepseekMaxQueryCharacters is the provider's hard cap on `query` and on
	// `ori_question`: a longer string is refused with a 400.
	deepseekMaxQueryCharacters = 128
	// deepseekMaxCount is the largest `count` the search API accepts.
	deepseekMaxCount = 50
	// deepseekContentResults caps the results this engine asks for. The API
	// takes up to 50, but every reply here carries page content, and 50 whole
	// documents would overrun the bound on a response. Fewer readable results is
	// the better trade for a search backend whose hits are meant to be read, and
	// `count` is not a price: the provider bills one request whatever it returns.
	deepseekContentResults = 15
	// deepseekMinTimeout replaces the shared per-engine timeout when it is left
	// at its 15s default: one request reads several whole pages on the
	// provider's side, and a search backend that times out every query is worse
	// than a slow one. An explicitly longer timeout is honoured.
	deepseekMinTimeout = 30 * time.Second
	// deepseekMaxURLCharacters is the provider's cap on a fetched URL.
	deepseekMaxURLCharacters = 8192
	// deepseekDomainOverfetch multiplies the requested limit when the caller
	// restricted the search to domains: the engine checks the hits against those
	// domains itself, after the provider's own `site:` clause. Asking for more
	// results costs nothing, because the API bills per request.
	deepseekDomainOverfetch = 3
)

// deepseekEngine queries DeepSeek's Search and Fetch APIs, its in-house search
// engine reached over plain JSON.
//
// It is the API-backed engine that needs no separate search vendor: one
// DeepSeek account answers from any egress. It also returns real text — a
// display snippet, a model-readable summary, and the page content itself — so
// results arrive answerable and indexable without the crawler reading each
// page, which is what this engine's earlier model-backed shape could not do.
//
// DeepSeek bills both endpoints per successful request rather than by tokens, so
// the engine is priced like `exa` and `tavily`: one configured `price` per
// query, and no model spend to report.
//
// Its key and endpoint come from the [[providers]] entry named by the engine's
// `provider`, so a DeepSeek account is configured once.
//
// The provider understands the `site:`, `lang:` and `after:` operators, so a
// caller's domains, language, and recency window go into the query rather than
// being applied afterwards. The 128-character cap forces a choice on a long
// query, and a clause that no longer fits is dropped from the request yet still
// enforced on the hits: a restriction the caller asked for is never quietly
// abandoned.
type deepseekEngine struct {
	id      string
	apiKey  string
	baseURL string
	client  *http.Client
}

func (e *deepseekEngine) Name() string { return e.id }

// deepseekSearchRequest is the provider's search body.
type deepseekSearchRequest struct {
	Query string `json:"query"`
	Count int    `json:"count,omitempty"`
	// OriginalQuestion carries the query as the caller asked it, alongside the
	// composed one: the provider says it improves query understanding.
	OriginalQuestion string `json:"ori_question,omitempty"`
	// NeedContent asks for the page text in the same reply, which is what makes
	// the results indexable without a second request.
	NeedContent bool `json:"need_content"`
}

type deepseekSearchResponse struct {
	Results []deepseekSearchHit `json:"results"`
}

// deepseekSearchHit is one ranked result. rerank_score and authority describe
// how the provider ranked it; the list arrives ordered, so neither is acted on.
type deepseekSearchHit struct {
	RerankScore float64 `json:"rerank_score"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	Content     string  `json:"content"`
	SiteName    string  `json:"site_name"`
	SiteIcon    string  `json:"site_icon"`
	Date        string  `json:"date"`
	Authority   int     `json:"authority"`
}

func (e *deepseekEngine) Search(ctx context.Context, query Query) (EngineResponse, error) {
	text := deepseekSearchQuery(query, time.Now())
	if text == "" {
		return EngineResponse{}, fmt.Errorf("%w: deepseek needs at least one search term", ErrInvalidQuery)
	}
	restricted := len(query.Domains) > 0
	request := deepseekSearchRequest{
		Query:       text,
		Count:       deepseekCount(query, restricted),
		NeedContent: true,
	}
	if original := strings.TrimSpace(query.Text); original != text {
		request.OriginalQuestion = deepseekFitText(original, deepseekMaxQueryCharacters)
	}

	var response deepseekSearchResponse
	if err := e.post(ctx, deepseekSearchPath, request, &response); err != nil {
		return EngineResponse{}, err
	}

	rows, pages := deepseekRows(response.Results)
	if restricted && len(rows) > 0 {
		rows = restrictToDomains(rows, query.Domains)
		if len(rows) == 0 {
			return EngineResponse{}, fmt.Errorf("deepseek returned nothing inside the requested domains (%s)", strings.Join(query.Domains, ", "))
		}
	}
	collected, err := collect(e.id, query, rows)
	if err != nil {
		return EngineResponse{}, err
	}
	return EngineResponse{Results: collected, Pages: retainedPages(pages, collected)}, nil
}

// deepseekSearchQuery composes the provider's query: the search terms plus the
// operators the caller's request maps onto, within the provider's
// 128-character cap.
//
// A clause is sent only while the terms still fit beside it, in priority order:
// the domain restriction first, then language, then the recency window. That
// order matters because the engine can enforce a domain restriction itself
// afterwards and cannot recover dropped terms, so a long query keeps its terms
// and loses the clause rather than being truncated to make room for a filter
// that gets applied twice over.
func deepseekSearchQuery(query Query, now time.Time) string {
	terms := strings.Join(strings.Fields(query.Text), " ")
	if terms == "" {
		return ""
	}
	budget := deepseekMaxQueryCharacters
	termCost := len([]rune(terms))

	var clauses []string
	add := func(clause string) bool {
		// One space joins every clause, so a clause costs its own length plus
		// the separator.
		cost := len([]rune(clause)) + 1
		if cost+termCost > budget {
			return false
		}
		clauses = append(clauses, clause)
		budget -= cost
		return true
	}

	if len(query.Domains) > 0 {
		site := deepseekSiteClause(query.Domains)
		if !add(site) {
			logDroppedClause("domain", site)
		}
	}
	if language := deepseekLanguage(query.Language); language != "" {
		if !add("lang:" + language) {
			logDroppedClause("language", language)
		}
	}
	if after := deepseekAfterDate(query.Freshness, now); after != "" {
		if !add("after:" + after) {
			logDroppedClause("recency", after)
		}
	}

	// A clause that was sent still counts against the cap, so the terms give way
	// to whatever room is left.
	terms = deepseekFitText(terms, budget)
	if terms == "" {
		return ""
	}
	if len(clauses) == 0 {
		return terms
	}
	return terms + " " + strings.Join(clauses, " ")
}

// deepseekSiteClause is the provider's multi-site form: separate `site:`
// clauses joined with OR.
func deepseekSiteClause(domains []string) string {
	clauses := make([]string, 0, len(domains))
	for _, domain := range domains {
		clauses = append(clauses, "site:"+domain)
	}
	return strings.Join(clauses, " OR ")
}

func logDroppedClause(kind, value string) {
	logging.Log.Debug().Str("kind", kind).Str("value", value).
		Msg("web search dropped deepseek operator clause to fit the query limit")
}

// deepseekCount is the `count` to ask for. A domain-restricted search asks for
// more than the caller wants, because the engine filters the hits afterwards.
func deepseekCount(query Query, overfetch bool) int {
	limit := query.Limit
	if limit <= 0 {
		limit = defaultResultLimit
	}
	if overfetch {
		limit *= deepseekDomainOverfetch
	}
	if limit > deepseekContentResults {
		limit = deepseekContentResults
	}
	if limit > deepseekMaxCount {
		limit = deepseekMaxCount
	}
	return limit
}

// deepseekLanguage reduces a language hint to the code the `lang:` operator
// takes: a bare subtag, with the region dropped, or nothing at all.
func deepseekLanguage(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	if language == "" {
		return ""
	}
	language, _, _ = strings.Cut(language, "-")
	language, _, _ = strings.Cut(language, "_")
	for _, r := range language {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return ""
		}
	}
	return language
}

// deepseekAfterDate maps the shared freshness window onto the `after:` date the
// provider accepts, as a calendar floor in UTC.
func deepseekAfterDate(freshness Freshness, now time.Time) string {
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
	return now.Add(-window).UTC().Format("2006-01-02")
}

// deepseekRows maps the provider's hits onto results, and onto indexable pages
// when the reply carried page text.
//
// `summary` is the long form written for a model to read, so it is what the
// snippet becomes; the shorter `snippet` only fills in when there is no
// summary. A hit with no title keeps the site it came from, because a titled
// row is what survives normalization.
func deepseekRows(hits []deepseekSearchHit) ([]Result, []Page) {
	rows := make([]Result, 0, len(hits))
	pages := make([]Page, 0, len(hits))
	fetchedAt := time.Now().UTC()
	for _, hit := range hits {
		title := cleanText(hit.Title)
		if title == "" {
			title = cleanText(hit.SiteName)
		}
		if title == "" {
			title = urlHost(hit.URL)
		}
		snippet := hit.Summary
		if strings.TrimSpace(snippet) == "" {
			snippet = hit.Snippet
		}
		rows = append(rows, Result{
			Title:   title,
			URL:     hit.URL,
			Snippet: excerptText(cleanText(snippet), exaSnippetCharacters),
		})
		if content := deepseekMarkdown(hit.Content); len([]rune(content)) >= minPageRunes {
			pages = append(pages, Page{
				URL:       strings.TrimSpace(hit.URL),
				Title:     title,
				Text:      content,
				FetchedAt: fetchedAt,
			})
		}
	}
	return rows, pages
}

// deepseekMarkdown keeps the provider's page text as written. It arrives as
// Markdown, so the generic display cleanup does not apply: stripping tag-like
// spans would eat code such as `Vec<u8>`, and folding line breaks would flatten
// a structured document into one line. Only line endings and runs of blank lines
// are normalized, and a page is bounded by the same limit the crawler applies to
// text it parses itself.
func deepseekMarkdown(raw string) string {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	text = strings.TrimSpace(text)
	if runes := []rune(text); len(runes) > maxPageRunes {
		text = string(runes[:maxPageRunes])
	}
	return text
}

// retainedPages keeps only the pages whose result survived normalization, so
// the local index is not filled with pages the caller was never shown.
func retainedPages(pages []Page, kept []Result) []Page {
	if len(pages) == 0 || len(kept) == 0 {
		return nil
	}
	surviving := make(map[string]bool, len(kept))
	for _, result := range kept {
		if key := normalizeURL(result.URL); key != "" {
			surviving[key] = true
		}
	}
	retained := make([]Page, 0, len(pages))
	for _, page := range pages {
		if surviving[normalizeURL(page.URL)] {
			retained = append(retained, page)
		}
	}
	return retained
}

// ReadPage fetches one page through DeepSeek's Fetch API and returns its text,
// which the provider already delivers as Markdown.
//
// The page is read from DeepSeek's egress, which is what lets a datacenter
// deployment reach sites that answer a direct request with a bot wall or a
// JavaScript shell, and no HTML parsing is needed here. The crawler uses it in
// preference to its own fetch; a failure falls back to a direct read, since a
// page the provider refuses is more often one it blocks than one that does not
// exist.
func (e *deepseekEngine) ReadPage(ctx context.Context, target string) (Page, error) {
	if err := deepseekCheckFetchURL(target); err != nil {
		return Page{}, err
	}
	var response struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := e.post(ctx, deepseekFetchPath, map[string]string{"url": strings.TrimSpace(target)}, &response); err != nil {
		return Page{}, err
	}
	text := deepseekMarkdown(response.Content)
	if len([]rune(text)) < minPageRunes {
		return Page{}, fmt.Errorf("deepseek fetch returned no usable content")
	}
	return Page{
		URL:       strings.TrimSpace(target),
		Title:     cleanText(response.Title),
		Text:      text,
		FetchedAt: time.Now().UTC(),
	}, nil
}

// deepseekCheckFetchURL rejects what the provider would only reject later, so a
// doomed request is never sent.
func deepseekCheckFetchURL(target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("empty url")
	}
	if len([]rune(target)) > deepseekMaxURLCharacters {
		return fmt.Errorf("url longer than %d characters", deepseekMaxURLCharacters)
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("url has no host")
	}
	if parsed.User != nil {
		return fmt.Errorf("url must not carry credentials")
	}
	return nil
}

// urlHost is the host a page lives on, without a scheme or a www. prefix.
func urlHost(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(parsed.Host), "www.")
}

// deepseekAPIRoot reduces a provider entry's base URL to DeepSeek's API root,
// under which the tool endpoints live.
//
// A [[providers]] entry points at the chat API, and deployments write it either
// bare (https://api.deepseek.com) or with the version segment an OpenAI client
// wants (https://api.deepseek.com/v1); configs written for this engine's older
// model-backed shape point at the Anthropic endpoint beside it. All three reach
// the same root.
func deepseekAPIRoot(providerBase string) string {
	base := strings.TrimRight(strings.TrimSpace(providerBase), "/")
	base = strings.TrimRight(strings.TrimSuffix(base, deepseekAnthropicSuffix), "/")
	base = strings.TrimRight(strings.TrimSuffix(base, "/v1"), "/")
	if base == "" {
		return deepseekDefaultBaseURL
	}
	return base
}

// post sends one request to the DeepSeek tool API and decodes a successful body
// into out.
func (e *deepseekEngine) post(ctx context.Context, path string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, e.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request = request.WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+e.apiKey)

	response, err := e.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, deepseekMaxResponseBytes))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("deepseek returned %s", deepseekFailure(response.Status, raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// deepseekFailure explains a refused request in the provider's own words.
//
// DeepSeek answers with the OpenAI error envelope, whose message says what was
// wrong with this particular request; the status then decides what the caller
// can do about it, because the provider documents which failures are worth
// retrying.
func deepseekFailure(status string, body []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	detail := ""
	if json.Unmarshal(body, &envelope) == nil {
		detail = strings.Join(strings.Fields(envelope.Error.Message), " ")
	}
	if detail == "" {
		detail = truncate(strings.TrimSpace(string(body)), 200)
	}
	if detail != "" {
		detail = ": " + detail
	}
	return status + detail + deepseekRetryHint(deepseekStatusCode(status))
}

// deepseekRetryHint is the advice for the statuses DeepSeek says are worth
// retrying, and for the one that only clears when the account is topped up.
func deepseekRetryHint(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return " (the DeepSeek apiKey is missing or invalid)"
	case http.StatusPaymentRequired:
		return " (the DeepSeek account has no balance; top it up before retrying)"
	case http.StatusForbidden:
		return " (rejected by the provider's fetch policy)"
	case http.StatusNotFound:
		return " (the page carried no usable content)"
	case http.StatusAccepted, http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusGatewayTimeout:
		return " (rate limited or timed out; retry later)"
	}
	if status >= 500 {
		return " (provider error; retry later)"
	}
	return ""
}

// deepseekStatusCode reduces an http.Response.Status line to its number,
// tolerating a reason phrase that is not the canonical one.
func deepseekStatusCode(status string) int {
	digits := status
	if space := strings.IndexByte(digits, ' '); space > 0 {
		digits = digits[:space]
	}
	code, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return code
}

// restrictToDomains keeps only the hits served from one of the domains.
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
//
// The provider's `site:` clause is the filter the query asks for; this is the
// same constraint checked on the way back, so a clause dropped by the
// 128-character cap still holds and a caller is never handed results from
// outside the domains it restricted.
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

// deepseekFitText cuts text to a rune budget, preferring a word boundary.
func deepseekFitText(text string, budget int) string {
	if budget <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= budget {
		return text
	}
	window := string(runes[:budget])
	// The window is already cut at a word end when the next rune is a space;
	// cutting inside a word steps back to the last boundary rather than keeping
	// a half-word a search engine would not match.
	if runes[budget] != ' ' {
		if space := strings.LastIndexByte(window, ' '); space > 0 {
			window = window[:space]
		}
	}
	return strings.TrimSpace(window)
}
