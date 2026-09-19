package websearch

import (
	"fmt"
	"html"
	"net/url"
	"strings"
)

// trackingParams are per-visit markers that identify a referrer rather than a
// document, so two hits differing only in these are the same page.
var trackingParams = map[string]bool{
	"fbclid":  true,
	"gclid":   true,
	"msclkid": true,
	"mc_eid":  true,
	"spm":     true,
	"ref":     true,
	"ref_src": true,
}

// mergeResults interleaves provider result lists by rank so no single provider
// dominates the merged list, drops duplicate URLs, and truncates to limit.
func mergeResults(perProvider [][]Result, limit int) []Result {
	seen := make(map[string]bool)
	merged := make([]Result, 0, limit)
	for rank := 0; ; rank++ {
		exhausted := true
		for _, results := range perProvider {
			if rank >= len(results) {
				continue
			}
			exhausted = false
			result := results[rank]
			key := normalizeURL(result.URL)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, result)
			if len(merged) >= limit {
				return merged
			}
		}
		if exhausted {
			return merged
		}
	}
}

// normalizeURL reduces a URL to a dedupe key: tracking parameters and
// fragments are dropped, host casing and the www. prefix are normalized.
func normalizeURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return strings.ToLower(trimmed)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.TrimPrefix(strings.ToLower(parsed.Host), "www.")
	parsed.Fragment = ""
	if parsed.Path == "/" {
		parsed.Path = ""
	}
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if trackingParams[lower] || strings.HasPrefix(lower, "utm_") {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// cleanText strips markup from provider snippets. Brave returns highlighted
// HTML fragments and some engines HTML-escape plain text.
func cleanText(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(trimmed))
	inTag := false
	for _, r := range trimmed {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
			builder.WriteByte(' ')
		case !inTag:
			builder.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(html.UnescapeString(builder.String())), " ")
}

// buildQueryText folds a domain filter into the query text for engines that
// expose no dedicated parameter.
func buildQueryText(query Query) string {
	if len(query.Domains) == 0 {
		return query.Text
	}
	clauses := make([]string, 0, len(query.Domains))
	for _, domain := range query.Domains {
		clauses = append(clauses, "site:"+domain)
	}
	if len(clauses) == 1 {
		return query.Text + " " + clauses[0]
	}
	return query.Text + " (" + strings.Join(clauses, " OR ") + ")"
}

func normalizeDomains(domains []string) []string {
	if len(domains) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(domains))
	result := make([]string, 0, len(domains))
	for _, domain := range domains {
		trimmed := strings.TrimSpace(domain)
		trimmed = strings.TrimPrefix(strings.TrimPrefix(trimmed, "https://"), "http://")
		trimmed = strings.TrimPrefix(trimmed, "www.")
		trimmed = strings.Trim(trimmed, "/")
		trimmed = strings.ToLower(trimmed)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		result = append(result, trimmed)
	}
	return result
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

// relevanceTokens splits a query into the terms worth judging relevance with:
// at least four characters and containing a letter. Short fragments ("io") and
// bare numbers ("18") match far too much text to prove anything, so a query
// made only of those is not judged at all.
func relevanceTokens(text string) []string {
	seen := make(map[string]bool)
	tokens := make([]string, 0, 8)
	current := make([]rune, 0, 16)
	letter := false
	flush := func() {
		if len(current) >= 4 && letter {
			token := string(current)
			if !seen[token] && len(tokens) < 8 {
				seen[token] = true
				tokens = append(tokens, token)
			}
		}
		current = current[:0]
		letter = false
	}
	for _, r := range strings.ToLower(text) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 127 {
			current = append(current, r)
			if r < '0' || r > '9' {
				letter = true
			}
			continue
		}
		flush()
	}
	flush()
	return tokens
}

// relevanceGuard reports whether an engine answered the question it was asked.
//
// Scraped engines can reply with HTTP 200 and a result page about something
// else entirely: Bing serves other topics for longer queries with no bot
// marker, captcha, or error status. Presenting those rows as ranked results
// would look like a successful search that answers the wrong question, so an
// engine whose result set mostly mentions none of the query terms is reported
// as a failure instead. At least half the rows must mention a query term, which
// tolerates a few off-topic rows in an otherwise good result set.
func relevanceGuard(rows []Result, query Query) error {
	if len(rows) == 0 {
		return nil
	}
	tokens := relevanceTokens(query.Text)
	if len(tokens) == 0 {
		return nil
	}
	mentioned := 0
	for _, row := range rows {
		haystack := strings.ToLower(row.Title + " " + row.Snippet + " " + row.URL)
		for _, token := range tokens {
			if strings.Contains(haystack, token) {
				mentioned++
				break
			}
		}
	}
	if mentioned*2 >= len(rows) {
		return nil
	}
	return fmt.Errorf(
		"%d of %d results mention none of the query terms (%s); first result was %q from %s",
		len(rows)-mentioned, len(rows), strings.Join(tokens, ", "), truncate(rows[0].Title, 60), truncate(rows[0].URL, 80),
	)
}

func cacheKey(query Query, engines []Engine) string {
	names := make([]string, 0, len(engines))
	for _, engine := range engines {
		names = append(names, engine.Name())
	}
	return fmt.Sprintf(
		"%s|%d|%s|%s|%s|%s",
		strings.Join(strings.Fields(strings.ToLower(query.Text)), " "),
		query.Limit,
		query.Freshness,
		strings.Join(query.Domains, ","),
		query.Language,
		strings.Join(names, ","),
	)
}
