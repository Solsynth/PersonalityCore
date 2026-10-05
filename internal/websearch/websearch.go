// Package websearch is a self-contained web search engine: it queries public
// search engines directly over HTTP, merges their results, and can crawl the
// pages it discovers into a local index.
//
// No third-party search API, aggregator, or hosted index is involved. Engines
// are scraped with the same HTTP requests a browser would make; a page store
// keeps extracted text so repeated or engine-less queries stay answerable.
package websearch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"src.solsynth.dev/sosys/persona/internal/config"
)

// Freshness is the coarse recency window shared by every engine.
type Freshness string

const (
	FreshnessDay   Freshness = "day"
	FreshnessWeek  Freshness = "week"
	FreshnessMonth Freshness = "month"
	FreshnessYear  Freshness = "year"
)

// Engine query modes. ModePrefer keeps a paid API from being billed on every
// query by only falling through to the remaining engines when it answers
// nothing.
const (
	ModeParallel = "parallel"
	ModePrefer   = "prefer"
)

// ErrNotConfigured is returned when the feature has no usable engines.
var ErrNotConfigured = errors.New("web search is not configured")

// ErrInvalidQuery is returned for caller-side request problems.
var ErrInvalidQuery = errors.New("invalid web search query")

// Query is one normalized search request. Limit is clamped by the searcher;
// Freshness values other than the Freshness constants are rejected.
type Query struct {
	Text      string
	Limit     int
	Freshness Freshness
	Domains   []string
	Language  string
	// PreferredEngine restricts the query to one configured engine id, which is
	// how a caller keeps a search on the provider it chose. Empty queries every
	// engine the server is configured with. An unknown id falls back to the
	// full set rather than failing the search.
	PreferredEngine string
}

// EngineInfo describes one configured engine for a caller that chooses between
// them, so the choice can be made on what each costs.
type EngineInfo struct {
	ID string `json:"id"`
	// Price is what one query through this engine costs the caller, in the
	// server's billing currency. Empty when the engine is free.
	Price string `json:"price,omitempty"`
	// Metered marks an engine that also spends provider tokens per query,
	// which the caller is billed for on top of any configured price.
	Metered bool `json:"metered"`
	// Free marks an engine that costs the caller nothing: no price and no
	// provider tokens.
	Free bool `json:"free"`
}

// Usage is what one engine's query spent on a metered provider, in that
// provider's own units. Provider and Model name the entry the caller prices it
// with, so the tokens are billed like the model the search called.
type Usage struct {
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	// Searches counts the provider-side searches this query ran. Providers bill
	// them separately from the tokens; the operator's per-query price is where
	// that fee is recovered.
	Searches int `json:"searches,omitempty"`
}

// Result is one normalized search hit.
type Result struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
	Snippet  string `json:"snippet,omitempty"`
	Provider string `json:"provider"`
}

// EngineReport describes how one engine contributed to a response. Error is set
// when the engine failed; the other engines' results are still used.
type EngineReport struct {
	Name    string `json:"name"`
	Results int    `json:"results"`
	Error   string `json:"error,omitempty"`
	// Usage is what this engine spent on its provider for the query. Engines
	// that run without a provider leave it nil.
	Usage *Usage `json:"usage,omitempty"`
}

// Charge kinds: what an amount covers.
const (
	// ChargeKindQuery is the engine's configured price for answering one query.
	ChargeKindQuery = "query"
	// ChargeKindUsage is the model tokens a metered engine spent answering it,
	// priced from the provider's own model pricing.
	ChargeKindUsage = "usage"
)

// Charge is what one engine cost this search. A search billed through several
// engines, or one that both states a price and spends tokens, reports one entry
// each.
type Charge struct {
	Engine string `json:"engine"`
	Kind   string `json:"kind"`
	Amount string `json:"amount"`
}

// Response is the normalized result set for one query.
type Response struct {
	Query   string         `json:"query"`
	Results []Result       `json:"results"`
	Engines []EngineReport `json:"engines,omitempty"`
	Cached  bool           `json:"cached"`
	// Charges lists the engines this search is billed for, omitted when every
	// engine that answered is free and when the answer came from the cache or
	// the local index.
	Charges []Charge `json:"charges,omitempty"`
	// Fallback marks a response served from the local page index because every
	// live engine failed.
	Fallback bool `json:"fallback,omitempty"`
	// PagesCrawled counts pages fetched into the local index for this query.
	PagesCrawled int `json:"pages_crawled,omitempty"`
}

// EngineResponse is one engine's answer to a query: the hits, and what the query
// spent on a metered provider.
type EngineResponse struct {
	Results []Result
	Usage   *Usage
}

// MeteredEngine is an engine whose queries spend provider tokens on top of
// whatever price the operator configured, which is what makes a caller billable
// even when no engine states a price.
type MeteredEngine interface {
	Metered() bool
}

// Engine is one directly queried search engine.
type Engine interface {
	Name() string
	Search(ctx context.Context, query Query) (EngineResponse, error)
}

// Page is one crawled document.
type Page struct {
	URL       string
	Title     string
	Text      string
	FetchedAt time.Time
}

// PageStore persists crawled pages and answers queries from them.
type PageStore interface {
	UpsertPages(ctx context.Context, pages []Page) error
	SearchPages(ctx context.Context, query string, limit int) ([]Result, error)
}

// Searcher queries every configured engine in parallel, merges the results, and
// optionally crawls the pages it surfaces. It is safe for concurrent use.
type Searcher struct {
	engines []Engine
	mode    string
	// prices holds the per-query price of every engine that charges for one,
	// keyed by engine id. Engines absent from the map are free.
	prices map[string]string
	// metered holds the engines that spend provider tokens per query, keyed by
	// engine id; those cost the caller golds even when no engine states a
	// price. It is a set rather than a flag so a caller's preference can be
	// answered about the engine it chose instead of about the whole server.
	metered      map[string]bool
	defaultLimit int
	maxLimit     int
	language     string
	cache        *cache
	crawler      *pageCrawler
	store        PageStore
}

// New builds a searcher from configuration. It returns ErrNotConfigured when
// the feature is disabled, which callers treat as "this server has no web
// search" rather than as a transient failure. store may be nil, which disables
// the local index while leaving live engine search intact.
func New(cfg config.WebSearchConfig, store PageStore) (*Searcher, error) {
	if !cfg.Enabled {
		return nil, ErrNotConfigured
	}
	engines := make([]Engine, 0, len(cfg.Engines))
	for _, engineCfg := range cfg.Engines {
		engine, err := newEngine(engineCfg, cfg)
		if err != nil {
			return nil, err
		}
		engines = append(engines, engine)
	}
	if len(engines) == 0 {
		return nil, fmt.Errorf("%w: no engines configured", ErrNotConfigured)
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode != ModePrefer {
		mode = ModeParallel
	}
	prices := make(map[string]string, len(cfg.Engines))
	for i, engineCfg := range cfg.Engines {
		// Keyed by the engine's own name rather than the configured id: an
		// engine whose id was left out answers to its type, and a price keyed
		// by anything else would never be charged.
		if amount := strings.TrimSpace(engineCfg.Price); amount != "" && amount != "0" {
			prices[engines[i].Name()] = amount
		}
	}
	metered := map[string]bool{}
	for _, engine := range engines {
		if reporter, ok := engine.(MeteredEngine); ok && reporter.Metered() {
			metered[engine.Name()] = true
		}
	}
	searcher := &Searcher{
		engines:      engines,
		mode:         mode,
		metered:      metered,
		prices:       prices,
		defaultLimit: cfg.DefaultLimit,
		maxLimit:     cfg.MaxLimit,
		language:     strings.TrimSpace(cfg.Language),
		cache:        newCache(cfg.CacheTTL),
		store:        store,
	}
	if cfg.Crawl.Enabled {
		searcher.crawler = newPageCrawler(cfg.Crawl, cfg.UserAgent, cfg.Timeout)
	}
	return searcher, nil
}

// Metered reports whether a search spends provider tokens, which the caller is
// billed for at the provider's model pricing. When preferred names a configured
// engine, only that engine is considered: a preference is how a caller keeps a
// metered engine out of its bill, so the answer must be about the engine that
// will actually answer.
func (s *Searcher) Metered(preferred string) bool {
	if s == nil {
		return false
	}
	for _, engine := range s.enginesFor(preferred) {
		if s.metered[engine.Name()] {
			return true
		}
	}
	return false
}

// EngineNames returns the configured engine ids in query order.
func (s *Searcher) EngineNames() []string {
	names := make([]string, 0, len(s.engines))
	for _, engine := range s.engines {
		names = append(names, engine.Name())
	}
	return names
}

// Catalog lists the configured engines with what each costs, so a caller can
// choose the one its searches run on. Prices are the configured per-query
// amount in the server's billing currency; the caller attaches the currency
// name, which this package has no notion of.
func (s *Searcher) Catalog() []EngineInfo {
	if s == nil {
		return nil
	}
	out := make([]EngineInfo, 0, len(s.engines))
	for _, engine := range s.engines {
		id := engine.Name()
		price := strings.TrimSpace(s.prices[id])
		metered := s.metered[id]
		out = append(out, EngineInfo{ID: id, Price: price, Metered: metered, Free: price == "" && !metered})
	}
	return out
}

// Billed reports whether a search charges per query. When preferred names a
// configured engine, only that engine's price counts, for the same reason
// Metered narrows: a free engine the caller chose must not require a payment
// wallet because some other engine on the server is priced.
func (s *Searcher) Billed(preferred string) bool {
	if s == nil {
		return false
	}
	for _, engine := range s.enginesFor(preferred) {
		if amount, ok := s.prices[engine.Name()]; ok && amount != "" && amount != "0" {
			return true
		}
	}
	return false
}

// enginesFor narrows the query set to the caller's preferred engine. An empty
// or unknown preference keeps the full configured set, so a stale preference
// degrades to the server default instead of breaking search.
func (s *Searcher) enginesFor(preferred string) []Engine {
	preferred = strings.TrimSpace(preferred)
	if preferred == "" {
		return s.engines
	}
	for _, engine := range s.engines {
		if engine.Name() == preferred {
			return []Engine{engine}
		}
	}
	return s.engines
}

// Search runs one query. An engine failure is reported per engine and does not
// fail the whole search unless every engine failed and the local index has
// nothing to answer with.
func (s *Searcher) Search(ctx context.Context, query Query) (*Response, error) {
	query.Text = strings.TrimSpace(query.Text)
	if query.Text == "" {
		return nil, fmt.Errorf("%w: query is required", ErrInvalidQuery)
	}
	if query.Freshness != "" && !validFreshness(query.Freshness) {
		return nil, fmt.Errorf("%w: unsupported freshness %q", ErrInvalidQuery, query.Freshness)
	}
	query.Limit = s.clampLimit(query.Limit)
	query.Domains = normalizeDomains(query.Domains)
	query.Language = strings.TrimSpace(query.Language)
	if query.Language == "" {
		query.Language = s.language
	}

	engines := s.enginesFor(query.PreferredEngine)
	key := cacheKey(query, engines)
	if cached, ok := s.cache.get(key); ok {
		cached.Cached = true
		// A cached answer made no upstream call, so it is never billed, and the
		// provider usage behind the stored answer belongs to the query that was
		// cached rather than to this one.
		cached.Charges = nil
		cached.Engines = withoutUsage(cached.Engines)
		return &cached, nil
	}

	outcomes := s.query(ctx, query, engines)
	perEngine := make([][]Result, 0, len(outcomes))
	reports := make([]EngineReport, 0, len(outcomes))
	failures := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		report := EngineReport{Name: outcome.name, Results: len(outcome.results), Usage: outcome.usage}
		if outcome.err != nil {
			report.Error = outcome.err.Error()
			failures = append(failures, outcome.name+": "+outcome.err.Error())
		} else {
			perEngine = append(perEngine, outcome.results)
		}
		reports = append(reports, report)
	}

	if len(perEngine) == 0 {
		// Every engine is down or blocking us. The pages crawled for earlier
		// queries are still searchable, so answer from them instead of failing.
		fallback, indexErr := s.searchIndex(ctx, query)
		if fallback != nil {
			fallback.Engines = reports
			s.cache.set(key, *fallback)
			return fallback, nil
		}
		failure := fmt.Errorf("all web search engines failed: %s", strings.Join(failures, "; "))
		if indexErr != nil {
			// Keep the engine diagnostics: the index is a fallback, not the
			// primary source, so its failure must not hide why search failed.
			return nil, fmt.Errorf("%w; local index unavailable: %w", failure, indexErr)
		}
		return nil, failure
	}

	results := mergeResults(perEngine, query.Limit)
	response := &Response{Query: query.Text, Results: results, Engines: reports, Charges: s.chargesFor(outcomes)}

	if s.crawler != nil && len(results) > 0 {
		crawled, pages := s.crawler.enrich(ctx, results)
		if len(pages) > 0 && s.store != nil {
			if err := s.store.UpsertPages(ctx, pages); err != nil {
				logCrawlError(err)
			}
		}
		response.Results = crawled
		response.PagesCrawled = len(pages)
	}

	s.cache.set(key, *response)
	return response, nil
}

// searchIndex answers a query from the local page index. It returns nil when
// there is no store or nothing matched, so the caller can report engine errors.
func (s *Searcher) searchIndex(ctx context.Context, query Query) (*Response, error) {
	if s.store == nil {
		return nil, nil
	}
	hits, err := s.store.SearchPages(ctx, query.Text, query.Limit)
	if err != nil {
		return nil, fmt.Errorf("query local index: %w", err)
	}
	if len(hits) == 0 {
		return nil, nil
	}
	return &Response{Query: query.Text, Results: hits, Fallback: true}, nil
}

type outcome struct {
	name    string
	results []Result
	usage   *Usage
	err     error
}

// chargesFor lists what this search owes: every engine that answered without
// error and is configured with a price. Engines that failed or were never asked
// are not charged.
func (s *Searcher) chargesFor(outcomes []outcome) []Charge {
	var charges []Charge
	for _, outcome := range outcomes {
		if outcome.err != nil {
			continue
		}
		if amount, ok := s.prices[outcome.name]; ok {
			charges = append(charges, Charge{Engine: outcome.name, Kind: ChargeKindQuery, Amount: amount})
		}
	}
	return charges
}

// withoutUsage drops the provider usage from a response served from the cache.
// The reports are copied, so the cached entry keeps its own and two concurrent
// cache hits never write to the same report.
func withoutUsage(reports []EngineReport) []EngineReport {
	if len(reports) == 0 {
		return reports
	}
	copied := make([]EngineReport, len(reports))
	copy(copied, reports)
	for i := range copied {
		copied[i].Usage = nil
	}
	return copied
}

// query runs the given engines according to the configured mode.
func (s *Searcher) query(ctx context.Context, query Query, engines []Engine) []outcome {
	if s.mode == ModePrefer {
		return s.queryInOrder(ctx, query, engines)
	}
	return s.fanOut(ctx, query, engines)
}

// queryInOrder asks each engine in configured order and stops at the first one
// that answers. Engines that were never asked are absent from the outcome list,
// so a response only reports the engines that actually ran.
func (s *Searcher) queryInOrder(ctx context.Context, query Query, engines []Engine) []outcome {
	outcomes := make([]outcome, 0, len(engines))
	for _, engine := range engines {
		response, err := engine.Search(ctx, query)
		outcomes = append(outcomes, outcome{name: engine.Name(), results: response.Results, usage: response.Usage, err: err})
		if err == nil && len(response.Results) > 0 {
			break
		}
	}
	return outcomes
}

func (s *Searcher) fanOut(ctx context.Context, query Query, engines []Engine) []outcome {
	outcomes := make([]outcome, len(engines))
	var wg sync.WaitGroup
	for i, engine := range engines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := engine.Search(ctx, query)
			outcomes[i] = outcome{name: engine.Name(), results: response.Results, usage: response.Usage, err: err}
		}()
	}
	wg.Wait()
	return outcomes
}

func (s *Searcher) clampLimit(limit int) int {
	if limit <= 0 {
		limit = s.defaultLimit
	}
	if limit < 1 {
		limit = defaultResultLimit
	}
	if s.maxLimit > 0 && limit > s.maxLimit {
		limit = s.maxLimit
	}
	if limit > hardResultLimit {
		limit = hardResultLimit
	}
	return limit
}

func validFreshness(freshness Freshness) bool {
	switch freshness {
	case FreshnessDay, FreshnessWeek, FreshnessMonth, FreshnessYear:
		return true
	default:
		return false
	}
}
