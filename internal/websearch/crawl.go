package websearch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"src.solsynth.dev/sosys/persona/internal/config"
	"src.solsynth.dev/sosys/persona/internal/logging"
)

const (
	// snippetTargetRunes is the snippet length above which a result is left
	// alone: crawling is only worth a request when the engine gave us little.
	snippetTargetRunes = 160
	// maxPageRunes bounds the text kept from one crawled page.
	maxPageRunes = 20000
	// minPageRunes drops pages whose extracted text carries no answer.
	minPageRunes = 200
	// robotsTTL is how long a parsed robots.txt policy is reused.
	robotsTTL = time.Hour
	// maxRobotsBytes bounds how much of a robots.txt file is read.
	maxRobotsBytes = 256 << 10
)

// pageCrawler fetches the open-web pages surfaced by live engines, extracts
// their readable text, and respects robots.txt plus a per-host delay.
type pageCrawler struct {
	client       *http.Client
	userAgent    string
	maxPages     int
	maxBytes     int64
	perHostDelay time.Duration
	robots       *robotsCache

	mu        sync.Mutex
	lastFetch map[string]time.Time
}

func newPageCrawler(cfg config.WebSearchCrawlConfig, userAgent string, fallbackTimeout time.Duration) *pageCrawler {
	timeout := cfg.PageTimeout
	if timeout <= 0 {
		timeout = fallbackTimeout
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	maxPages := cfg.MaxPagesPerQuery
	if maxPages <= 0 {
		maxPages = 3
	}
	if maxPages > 10 {
		maxPages = 10
	}
	maxBytes := cfg.MaxPageBytes
	if maxBytes <= 0 {
		maxBytes = 2 << 20
	}
	delay := cfg.PerHostDelay
	if delay < 0 {
		delay = 0
	}
	return &pageCrawler{
		client:       &http.Client{Timeout: timeout},
		userAgent:    userAgent,
		maxPages:     maxPages,
		maxBytes:     maxBytes,
		perHostDelay: delay,
		robots:       newRobotsCache(),
		lastFetch:    make(map[string]time.Time),
	}
}

// enrich fetches the top-ranked results that carry a thin snippet, replaces
// those snippets with extracted page text, and returns the pages that were
// successfully read. Fetch failures are best-effort: the live engine results
// stay usable and the failure is logged.
func (c *pageCrawler) enrich(ctx context.Context, results []Result) ([]Result, []Page) {
	candidates := make([]int, 0, c.maxPages)
	for index, result := range results {
		if len(candidates) >= c.maxPages {
			break
		}
		if len([]rune(result.Snippet)) < snippetTargetRunes {
			candidates = append(candidates, index)
		}
	}
	if len(candidates) == 0 {
		return results, nil
	}

	enriched := append([]Result(nil), results...)
	pages := make([]Page, 0, len(candidates))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, index := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			page, err := c.fetch(ctx, enriched[index].URL)
			if err != nil {
				logging.Log.Debug().Err(err).Str("url", enriched[index].URL).Msg("web search crawl skipped page")
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if excerpt := excerptText(page.Text, snippetTargetRunes); len(excerpt) > len([]rune(enriched[index].Snippet)) {
				enriched[index].Snippet = excerpt
			}
			pages = append(pages, page)
		}()
	}
	wg.Wait()

	return enriched, pages
}

// fetch reads one page and returns its extracted text.
func (c *pageCrawler) fetch(ctx context.Context, target string) (Page, error) {
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil {
		return Page{}, fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Page{}, fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}

	allowed, crawlDelay, err := c.robots.allowed(ctx, c.client, c.userAgent, parsed)
	if err != nil {
		return Page{}, err
	}
	if !allowed {
		return Page{}, fmt.Errorf("robots.txt disallows %s", parsed.Path)
	}
	if err := c.waitForHost(ctx, parsed.Host, crawlDelay); err != nil {
		return Page{}, err
	}

	request, err := http.NewRequest(http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Page{}, err
	}
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
	response, err := c.client.Do(request.WithContext(ctx))
	if err != nil {
		return Page{}, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return Page{}, fmt.Errorf("page returned %s", response.Status)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.Contains(strings.ToLower(contentType), "html") {
		return Page{}, fmt.Errorf("unsupported content type %q", contentType)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, c.maxBytes))
	if err != nil {
		return Page{}, fmt.Errorf("read page: %w", err)
	}

	title, text := extractArticle(body)
	if len([]rune(text)) < minPageRunes {
		return Page{}, fmt.Errorf("page carried no readable text")
	}
	return Page{
		URL:       parsed.String(),
		Title:     title,
		Text:      text,
		FetchedAt: time.Now().UTC(),
	}, nil
}

// waitForHost serializes requests per host so a burst of results from one site
// does not turn into a burst of requests to that site.
func (c *pageCrawler) waitForHost(ctx context.Context, host string, crawlDelay time.Duration) error {
	delay := c.perHostDelay
	if crawlDelay > delay {
		delay = crawlDelay
	}
	if delay <= 0 {
		return nil
	}

	c.mu.Lock()
	now := time.Now()
	slot := now
	if last, ok := c.lastFetch[host]; ok && last.After(now) {
		slot = last
	}
	c.lastFetch[host] = slot.Add(delay)
	wait := slot.Sub(now)
	c.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ─── Readable text extraction ──────────────────────────────────────

// dropTags carry navigation, chrome, or code rather than article text.
var dropTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"svg": true, "iframe": true, "form": true, "nav": true, "header": true,
	"footer": true, "aside": true, "button": true, "select": true,
}

// extractArticle returns a page title and its readable body text.
func extractArticle(body []byte) (string, string) {
	doc := parseHTML(body)
	if doc == nil {
		return "", ""
	}
	stripTags(doc)

	title := ""
	if node := findByTag(doc, "title"); node != nil {
		title = elementText(node)
	}
	if title == "" {
		title = metaContent(doc, "og:title")
	}

	text := ""
	for _, tag := range []string{"article", "main"} {
		if node := findByTag(doc, tag); node != nil {
			candidate := elementText(node)
			if len([]rune(candidate)) > len([]rune(text)) {
				text = candidate
			}
		}
	}
	if bodyNode := findByTag(doc, "body"); bodyNode != nil {
		if candidate := elementText(bodyNode); len([]rune(candidate)) > len([]rune(text)) && len([]rune(text)) < minPageRunes {
			text = candidate
		}
	}
	if len([]rune(text)) > maxPageRunes {
		text = string([]rune(text)[:maxPageRunes])
	}
	return title, strings.TrimSpace(text)
}

func stripTags(node *html.Node) {
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		if child.Type == html.ElementNode && dropTags[child.Data] {
			node.RemoveChild(child)
		} else {
			stripTags(child)
		}
		child = next
	}
}

func findByTag(root *html.Node, tag string) *html.Node {
	found := findAll(root, func(node *html.Node) bool { return node.Data == tag })
	if len(found) == 0 {
		return nil
	}
	return found[0]
}

func metaContent(root *html.Node, property string) string {
	for _, node := range findAll(root, func(node *html.Node) bool {
		return node.Data == "meta" && strings.EqualFold(attr(node, "property"), property)
	}) {
		if content := strings.TrimSpace(attr(node, "content")); content != "" {
			return cleanText(content)
		}
	}
	return ""
}

// excerptText returns the leading text of a page, trimmed to a word boundary.
func excerptText(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	window := string(runes[:limit])
	if space := strings.LastIndexByte(window, ' '); space > limit/2 {
		window = window[:space]
	}
	return strings.TrimSpace(window) + "…"
}

func logCrawlError(err error) {
	logging.Log.Debug().Err(err).Msg("web search index write failed")
}

// ─── robots.txt ────────────────────────────────────────────────────

type robotsCache struct {
	mu      sync.Mutex
	entries map[string]robotsEntry
}

type robotsEntry struct {
	fetchedAt  time.Time
	rules      []robotsRule
	crawlDelay time.Duration
}

type robotsRule struct {
	allow bool
	path  string
}

func newRobotsCache() *robotsCache {
	return &robotsCache{entries: make(map[string]robotsEntry)}
}

// allowed reports whether the user agent may fetch target, plus any crawl delay
// the site asks for. An unreachable or missing robots.txt allows the fetch, as
// the convention requires.
func (c *robotsCache) allowed(ctx context.Context, client *http.Client, userAgent string, target *url.URL) (bool, time.Duration, error) {
	origin := target.Scheme + "://" + target.Host
	entry, ok := c.lookup(origin)
	if !ok {
		entry = fetchRobots(ctx, client, userAgent, origin)
		c.store(origin, entry)
	}
	if len(entry.rules) == 0 {
		return true, entry.crawlDelay, nil
	}

	path := target.EscapedPath()
	if path == "" {
		path = "/"
	}
	best := robotsRule{allow: true}
	for _, rule := range entry.rules {
		if !strings.HasPrefix(path, rule.path) {
			continue
		}
		if len(rule.path) > len(best.path) {
			best = rule
		}
	}
	return best.allow, entry.crawlDelay, nil
}

func (c *robotsCache) lookup(origin string) (robotsEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[origin]
	if !ok || time.Since(entry.fetchedAt) > robotsTTL {
		return robotsEntry{}, false
	}
	return entry, true
}

func (c *robotsCache) store(origin string, entry robotsEntry) {
	entry.fetchedAt = time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[origin] = entry
}

func fetchRobots(ctx context.Context, client *http.Client, userAgent, origin string) robotsEntry {
	request, err := http.NewRequest(http.MethodGet, origin+"/robots.txt", nil)
	if err != nil {
		return robotsEntry{}
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := client.Do(request.WithContext(ctx))
	if err != nil {
		return robotsEntry{}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return robotsEntry{}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRobotsBytes))
	if err != nil {
		return robotsEntry{}
	}
	return parseRobots(body, userAgent)
}

// parseRobots keeps the rules of every group that applies to userAgent: the
// wildcard group and any group naming a token found in the user agent string.
func parseRobots(body []byte, userAgent string) robotsEntry {
	entry := robotsEntry{}
	applies := false
	groupHasRules := false

	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if line == "" {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)

		switch key {
		case "user-agent":
			// A new user-agent line after rules starts a new group.
			if groupHasRules {
				applies = false
				groupHasRules = false
			}
			if matchesUserAgent(value, userAgent) {
				applies = true
			}
		case "disallow", "allow":
			if !applies {
				continue
			}
			groupHasRules = true
			path := strings.TrimSpace(value)
			if key == "disallow" && path == "" {
				continue
			}
			entry.rules = append(entry.rules, robotsRule{allow: key == "allow", path: path})
		case "crawl-delay":
			if !applies {
				continue
			}
			if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds > 0 {
				entry.crawlDelay = time.Duration(seconds * float64(time.Second))
			}
		}
	}
	return entry
}

func matchesUserAgent(token, userAgent string) bool {
	token = strings.ToLower(strings.TrimSpace(token))
	if token == "" {
		return false
	}
	if token == "*" {
		return true
	}
	return strings.Contains(strings.ToLower(userAgent), token)
}
