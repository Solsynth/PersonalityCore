package websearch

import (
	"bytes"
	"encoding/base64"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// ─── DOM helpers ───────────────────────────────────────────────────
//
// Search result pages are parsed with golang.org/x/net/html and matched by tag
// and class token instead of a CSS/XPath engine, which keeps the dependency
// list unchanged.

func parseHTML(body []byte) *html.Node {
	node, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	return node
}

// findAll returns every element node matching the predicate, in document order.
func findAll(root *html.Node, match func(*html.Node) bool) []*html.Node {
	var found []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && match(node) {
			found = append(found, node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	if root != nil {
		walk(root)
	}
	return found
}

// findByClass returns the first element with the given tag carrying the given
// class token. An empty class matches on the tag alone.
func findByClass(root *html.Node, tag, class string) *html.Node {
	found := findAll(root, func(node *html.Node) bool {
		if tag != "" && node.Data != tag {
			return false
		}
		return class == "" || hasClass(node, class)
	})
	if len(found) == 0 {
		return nil
	}
	return found[0]
}

// findByClassSubstring returns the first element whose class attribute contains
// the given substring. Search engines add and drop modifier classes between
// releases, so substring matching survives more markup churn than exact tokens.
func findByClassSubstring(root *html.Node, tag, classSubstring string) *html.Node {
	found := findAll(root, func(node *html.Node) bool {
		return (tag == "" || node.Data == tag) && classContains(node, classSubstring)
	})
	if len(found) == 0 {
		return nil
	}
	return found[0]
}

func hasClass(node *html.Node, class string) bool {
	for _, token := range strings.Fields(attr(node, "class")) {
		if token == class {
			return true
		}
	}
	return false
}

func classContains(node *html.Node, substring string) bool {
	return strings.Contains(attr(node, "class"), substring)
}

func attr(node *html.Node, key string) string {
	if node == nil {
		return ""
	}
	for _, attribute := range node.Attr {
		if attribute.Key == key {
			return attribute.Val
		}
	}
	return ""
}

// elementText returns the collapsed text content of a node, ignoring script and
// style bodies.
func elementText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.ElementNode && (current.Data == "script" || current.Data == "style") {
			return
		}
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
			builder.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return cleanText(builder.String())
}

// firstLink returns the first anchor at or below node.
func firstLink(node *html.Node) *html.Node {
	if node == nil {
		return nil
	}
	if node.Type == html.ElementNode && node.Data == "a" {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if link := firstLink(child); link != nil {
			return link
		}
	}
	return nil
}

// textWithoutTitle returns the element text with the title's own text removed,
// which is how snippets are separated from titles inside one result block.
func textWithoutTitle(block, title *html.Node) string {
	if block == nil {
		return ""
	}
	titleText := elementText(title)
	snippet := elementText(block)
	if titleText == "" {
		return snippet
	}
	return strings.TrimSpace(strings.TrimPrefix(snippet, titleText))
}

// ─── URL helpers ───────────────────────────────────────────────────

// absoluteURL resolves href against base and unwraps engine redirect wrappers.
func absoluteURL(base *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	parsed, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if base != nil {
		parsed = base.ResolveReference(parsed)
	}
	if parsed.Host == "" {
		return ""
	}
	return unwrapRedirect(parsed)
}

// unwrapRedirect returns the destination behind a click tracker. DuckDuckGo,
// Google, and Bing all wrap outbound links; a wrapped URL would otherwise be
// stored, deduplicated, and crawled as if it were the target page.
//
// The `/url?q=` and `/ck/a?u=` shapes are matched on any host because the same
// wrappers appear behind proxies and mirrors, and a wrapped link is meant to
// resolve to its target anyway.
func unwrapRedirect(parsed *url.URL) string {
	host := strings.ToLower(parsed.Host)
	path := strings.ToLower(parsed.Path)
	values := parsed.Query()

	switch {
	case strings.HasSuffix(host, "duckduckgo.com") && path == "/l/":
		if target := values.Get("uddg"); target != "" {
			return target
		}
	case path == "/url" || path == "/search/url":
		for _, key := range []string{"q", "url"} {
			if target := values.Get(key); strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
				return target
			}
		}
	case strings.HasPrefix(path, "/ck/a"):
		if encoded := values.Get("u"); encoded != "" {
			if target := decodeBingTarget(encoded); target != "" {
				return target
			}
		}
	}
	return parsed.String()
}

// decodeBingTarget decodes Bing's base64url click-tracker payload, which is
// prefixed with a version marker such as "a1".
func decodeBingTarget(encoded string) string {
	if len(encoded) > 2 && (strings.HasPrefix(encoded, "a1") || strings.HasPrefix(encoded, "a2")) {
		encoded = encoded[2:]
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(encoded)
		if err != nil {
			return ""
		}
	}
	target := strings.TrimSpace(string(decoded))
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return target
	}
	return ""
}

// skipHosts are engine-internal or non-answer hosts that never belong in
// results even when an engine returns them.
var skipHosts = []string{
	"duckduckgo.com", "google.com", "googleusercontent.com", "gstatic.com",
	"bing.com", "microsoft.com", "msn.com",
}

func isSkippableURL(raw string) bool {
	if raw == "" {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return true
	}
	host := strings.ToLower(parsed.Host)
	for _, skipHost := range skipHosts {
		if host == skipHost || strings.HasSuffix(host, "."+skipHost) {
			return true
		}
	}
	return false
}

// blockMarkers are strings that indicate an engine served a bot wall, a consent
// interstitial, or a JavaScript shell instead of results. Reporting these as an
// engine error beats returning a silent empty result set.
var blockMarkers = []string{
	"captcha",
	"unusual traffic",
	"verify you are human",
	"just a moment",
	"enable javascript",
	"enablejs",
	"access denied",
	"are you a robot",
	"not a robot",
	"complete the following challenge",
}

func detectBlock(body []byte) string {
	lowered := strings.ToLower(string(body))
	for _, marker := range blockMarkers {
		if strings.Contains(lowered, marker) {
			return marker
		}
	}
	return ""
}
