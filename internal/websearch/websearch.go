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
}

// Response is the normalized result set for one query.
type Response struct {
	Query   string         `json:"query"`
	Results []Result       `json:"results"`
	Engines []EngineReport `json:"engines,omitempty"`
	Cached  bool           `json:"cached"`
	// Fallback marks a response served from the local page index because every
	// live engine failed.
	Fallback bool `json:"fallback,omitempty"`
	// PagesCrawled counts pages fetched into the local index for this query.
	PagesCrawled int `json:"pages_crawled,omitempty"`
}

// Engine is one directly queried search engine.
type Engine interface {
	Name() string
	Search(ctx context.Context, query Query) ([]Result, error)
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
	engines      []Engine
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
	searcher := &Searcher{
		engines:      engines,
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

// EngineNames returns the configured engine ids in query order.
func (s *Searcher) EngineNames() []string {
	names := make([]string, 0, len(s.engines))
	for _, engine := range s.engines {
		names = append(names, engine.Name())
	}
	return names
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

	key := cacheKey(query, s.engines)
	if cached, ok := s.cache.get(key); ok {
		cached.Cached = true
		return &cached, nil
	}

	outcomes := s.fanOut(ctx, query)
	perEngine := make([][]Result, 0, len(outcomes))
	reports := make([]EngineReport, 0, len(outcomes))
	failures := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		report := EngineReport{Name: outcome.name, Results: len(outcome.results)}
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
	response := &Response{Query: query.Text, Results: results, Engines: reports}

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
	err     error
}

func (s *Searcher) fanOut(ctx context.Context, query Query) []outcome {
	outcomes := make([]outcome, len(s.engines))
	var wg sync.WaitGroup
	for i, engine := range s.engines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := engine.Search(ctx, query)
			outcomes[i] = outcome{name: engine.Name(), results: results, err: err}
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
