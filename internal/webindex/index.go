// Package webindex keeps a local, persistent index of crawled web pages so the
// web search tool can still answer queries when the live engines block or fail.
//
// Pages are server-global (search results are not tied to an account) and are
// ranked in Go: the candidate filter uses only LIKE, so the same code runs on
// Postgres in production and sqlite in tests.
package webindex

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/persona/internal/database"
	"src.solsynth.dev/sosys/persona/internal/websearch"
)

const (
	// maxStoredRunes bounds the text kept per page. A crawled document is
	// mostly boilerplate past this point, and the tail would slow every scan.
	maxStoredRunes = 60000
	// candidateLimit bounds how many rows the database hands to the Go ranker.
	candidateLimit = 400
	// maxTokens bounds the query terms used for matching and scoring.
	maxTokens = 8
	// minTokenRunes drops one-character terms, which match nearly everything.
	minTokenRunes = 2
	// maxTokenOccurrences caps one term's contribution to a page score so
	// keyword stuffing cannot dominate ranking.
	maxTokenOccurrences = 10
	// titleWeight makes a title hit outrank a body-only hit for the same term.
	titleWeight = 3
	// snippetRunes is the target length of a returned snippet.
	snippetRunes = 300
	// ellipsis marks snippet text cut at either edge.
	ellipsis = "…"
	// indexProvider labels results that came from the local index.
	indexProvider = "index"

	defaultResultLimit = 5
	maxResultLimit     = 20
)

// Index implements websearch.PageStore.
var _ websearch.PageStore = (*Index)(nil)

// Index stores crawled pages and answers queries from the stored text.
type Index struct {
	db *database.DB
}

// New returns an index over db. A nil db returns a nil index, and every method
// tolerates a nil receiver: callers can pass the result straight to
// websearch.New without special-casing "this server keeps no local index".
func New(db *database.DB) *Index {
	if db == nil {
		return nil
	}
	return &Index{db: db}
}

// UpsertPages stores crawled pages, refreshing any page already indexed under
// the same URL. Pages without a URL or without text hold nothing searchable and
// are skipped.
func (i *Index) UpsertPages(ctx context.Context, pages []websearch.Page) error {
	if i == nil || len(pages) == 0 {
		return nil
	}

	records := make([]database.WebSearchPage, 0, len(pages))
	positions := make(map[string]int, len(pages))
	for _, page := range pages {
		rawURL := strings.TrimSpace(page.URL)
		text := strings.TrimSpace(page.Text)
		if rawURL == "" || text == "" {
			continue
		}
		fetchedAt := page.FetchedAt
		if fetchedAt.IsZero() {
			fetchedAt = time.Now()
		}
		record := database.WebSearchPage{
			ID:        ulid.Make().String(),
			URL:       rawURL,
			Host:      hostOf(rawURL),
			Title:     strings.TrimSpace(page.Title),
			Text:      truncateRunes(text, maxStoredRunes),
			FetchedAt: fetchedAt,
		}
		// One statement may not touch the same conflicting row twice on
		// Postgres, so a URL repeated inside a single batch keeps its last page.
		if at, seen := positions[rawURL]; seen {
			records[at] = record
			continue
		}
		positions[rawURL] = len(records)
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil
	}

	return i.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "url"}},
		DoUpdates: clause.AssignmentColumns([]string{"host", "title", "text", "fetched_at", "updated_at"}),
	}).Create(&records).Error
}

// SearchPages ranks stored pages against query and returns the best hits. An
// empty query, or a query whose terms match no stored page, yields no hits and
// no error, so the caller can fall back to the live engines quietly.
func (i *Index) SearchPages(ctx context.Context, query string, limit int) ([]websearch.Result, error) {
	if i == nil {
		return nil, nil
	}
	tokens := tokenize(query)
	if len(tokens) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultResultLimit
	}
	if limit > maxResultLimit {
		limit = maxResultLimit
	}

	condition, args := candidateFilter(tokens)
	var rows []database.WebSearchPage
	if err := i.db.WithContext(ctx).
		Model(&database.WebSearchPage{}).
		Where(condition, args...).
		Order("updated_at DESC").
		Limit(candidateLimit).
		Find(&rows).Error; err != nil {
		return nil, err
	}

	type ranked struct {
		page  database.WebSearchPage
		score int
	}
	candidates := make([]ranked, 0, len(rows))
	for _, row := range rows {
		title := strings.ToLower(row.Title)
		text := strings.ToLower(row.Text)
		score := 0
		for _, token := range tokens {
			score += titleWeight*countOccurrences(title, token) + countOccurrences(text, token)
		}
		if score == 0 {
			continue
		}
		candidates = append(candidates, ranked{page: row, score: score})
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	sort.SliceStable(candidates, func(a, b int) bool {
		left, right := candidates[a], candidates[b]
		if left.score != right.score {
			return left.score > right.score
		}
		if !left.page.FetchedAt.Equal(right.page.FetchedAt) {
			return left.page.FetchedAt.After(right.page.FetchedAt)
		}
		return left.page.URL < right.page.URL
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	results := make([]websearch.Result, 0, len(candidates))
	for _, candidate := range candidates {
		results = append(results, websearch.Result{
			Title:    candidate.page.Title,
			URL:      candidate.page.URL,
			Snippet:  makeSnippet(candidate.page.Text, tokens),
			Provider: indexProvider,
		})
	}
	return results, nil
}

// candidateFilter builds the LIKE filter for the query terms. Terms are
// lowercased alphanumeric runs, so they carry no wildcards; the explicit
// LOWER() keeps matching case-insensitive on Postgres, where LIKE is not.
func candidateFilter(tokens []string) (string, []any) {
	clauses := make([]string, 0, len(tokens))
	args := make([]any, 0, len(tokens)*2)
	for _, token := range tokens {
		clauses = append(clauses, "(LOWER(title) LIKE ? OR LOWER(text) LIKE ?)")
		pattern := "%" + token + "%"
		args = append(args, pattern, pattern)
	}
	return strings.Join(clauses, " OR "), args
}

// tokenize splits a query into lowercase alphanumeric terms, dropping terms too
// short to be meaningful and keeping the first maxTokens distinct ones.
func tokenize(query string) []string {
	tokens := make([]string, 0, maxTokens)
	seen := make(map[string]struct{}, maxTokens)
	var current strings.Builder

	flush := func() {
		token := current.String()
		current.Reset()
		if utf8.RuneCountInString(token) < minTokenRunes {
			return
		}
		if _, duplicate := seen[token]; duplicate {
			return
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}

	for _, r := range strings.ToLower(query) {
		if isTokenRune(r) {
			current.WriteRune(r)
			continue
		}
		flush()
		if len(tokens) == maxTokens {
			return tokens
		}
	}
	flush()
	if len(tokens) > maxTokens {
		tokens = tokens[:maxTokens]
	}
	return tokens
}

// isTokenRune reports whether r belongs to an [a-z0-9] token run. ASCII-only
// keeps the terms free of SQL wildcards.
func isTokenRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// countOccurrences counts token in lowered, capped so repeated keywords cannot
// flood a page score.
func countOccurrences(lowered, token string) int {
	if token == "" {
		return 0
	}
	count := strings.Count(lowered, token)
	if count > maxTokenOccurrences {
		return maxTokenOccurrences
	}
	return count
}

// makeSnippet returns a whitespace-collapsed window of text around the first
// query term it contains, marked with an ellipsis at any edge the window cuts.
// Text with no term occurrence falls back to the head of the text, which covers
// pages matched by their title alone.
func makeSnippet(text string, tokens []string) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}

	start, end := 0, len(runes)
	if len(runes) > snippetRunes {
		if at := firstTokenRuneIndex(text, tokens); at < 0 {
			end = snippetRunes
		} else {
			start = at - snippetRunes/2
			if start < 0 {
				start = 0
			}
			end = start + snippetRunes
			if end > len(runes) {
				end = len(runes)
				start = end - snippetRunes
			}
			start = trimToWordStart(runes, start, end)
			end = trimToWordEnd(runes, start, end)
		}
	}

	body := collapseWhitespace(string(runes[start:end]))
	var snippet strings.Builder
	if start > 0 {
		snippet.WriteString(ellipsis)
	}
	snippet.WriteString(body)
	if end < len(runes) {
		snippet.WriteString(ellipsis)
	}
	return snippet.String()
}

// firstTokenRuneIndex returns the rune offset of the earliest token occurrence
// in text, or -1 when no token appears. Case folding is rune-preserving, so the
// offset holds for the original text too.
func firstTokenRuneIndex(text string, tokens []string) int {
	lowered := strings.ToLower(text)
	best := -1
	for _, token := range tokens {
		if token == "" {
			continue
		}
		at := strings.Index(lowered, token)
		if at < 0 {
			continue
		}
		position := utf8.RuneCountInString(lowered[:at])
		if best < 0 || position < best {
			best = position
		}
	}
	return best
}

// trimToWordStart advances start past a partial leading word, unless doing so
// would consume the whole window.
func trimToWordStart(runes []rune, start, end int) int {
	if start <= 0 {
		return 0
	}
	for at := start; at < end; at++ {
		if unicode.IsSpace(runes[at]) {
			return at + 1
		}
	}
	return start
}

// trimToWordEnd retreats end to before a partial trailing word, unless doing so
// would consume the whole window.
func trimToWordEnd(runes []rune, start, end int) int {
	if end >= len(runes) {
		return len(runes)
	}
	for at := end - 1; at > start; at-- {
		if unicode.IsSpace(runes[at]) {
			return at
		}
	}
	return end
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// hostOf returns the host of rawURL without a leading www., which groups pages
// from one site under a single indexed host.
func hostOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	host = strings.TrimPrefix(host, "www.")
	return truncateRunes(host, 255)
}

// truncateRunes cuts s to at most limit runes without splitting one.
func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}
