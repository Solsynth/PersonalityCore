package websearch

import (
	"encoding/base64"
	"net/url"
	"testing"
)

func TestAbsoluteURLUnwrapsEngineRedirects(t *testing.T) {
	base, err := url.Parse("https://html.duckduckgo.com/html/")
	if err != nil {
		t.Fatal(err)
	}
	bingPayload := "a1" + base64.RawURLEncoding.EncodeToString([]byte("https://example.com/bing-target"))

	cases := map[string]struct {
		href string
		want string
	}{
		"duckduckgo wrapper": {
			href: "//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fddg-target&rut=abc",
			want: "https://example.com/ddg-target",
		},
		"google wrapper": {
			href: "https://www.google.com/url?q=https://example.com/google-target&sa=U",
			want: "https://example.com/google-target",
		},
		"bing wrapper": {
			href: "https://www.bing.com/ck/a?!&&p=xyz&u=" + bingPayload,
			want: "https://example.com/bing-target",
		},
		"protocol relative": {
			href: "//example.com/protocol-relative",
			want: "https://example.com/protocol-relative",
		},
		"relative": {
			href: "/docs/page",
			want: "https://html.duckduckgo.com/docs/page",
		},
		"plain": {
			href: "https://example.com/plain",
			want: "https://example.com/plain",
		},
		"empty": {href: "", want: ""},
	}

	for name, tc := range cases {
		if got := absoluteURL(base, tc.href); got != tc.want {
			t.Fatalf("%s: absoluteURL(%q) = %q, want %q", name, tc.href, got, tc.want)
		}
	}
}

func TestIsSkippableURL(t *testing.T) {
	skippable := []string{
		"",
		"javascript:void(0)",
		"https://duckduckgo.com/y.js",
		"https://www.google.com/preferences",
		"https://support.microsoft.com/help",
		"not a url",
	}
	for _, raw := range skippable {
		if !isSkippableURL(raw) {
			t.Fatalf("isSkippableURL(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{
		"https://go.dev/doc/go1.26",
		"https://blog.example.com/post",
	} {
		if isSkippableURL(raw) {
			t.Fatalf("isSkippableURL(%q) = true, want false", raw)
		}
	}
}

func TestDetectBlock(t *testing.T) {
	if detectBlock([]byte("<html><body>Normal results page</body></html>")) != "" {
		t.Fatal("a normal page must not be reported as a bot wall")
	}
	for _, body := range []string{
		"<div>Our systems have detected unusual traffic from your network</div>",
		"<p>Please complete the following challenge to confirm this search was made by a human.</p>",
		"<meta content=\"0;url=/httpservice/retry/enablejs\">",
	} {
		if detectBlock([]byte(body)) == "" {
			t.Fatalf("expected a block marker in %q", body)
		}
	}
}

func TestBuildQueryTextFoldsDomains(t *testing.T) {
	cases := map[string]struct {
		domains []string
		want    string
	}{
		"none": {want: "postgres 18"},
		"one":  {domains: []string{"postgresql.org"}, want: "postgres 18 site:postgresql.org"},
		"two": {
			domains: []string{"postgresql.org", "go.dev"},
			want:    "postgres 18 (site:postgresql.org OR site:go.dev)",
		},
	}
	for name, tc := range cases {
		query := Query{Text: "postgres 18", Domains: tc.domains}
		if got := buildQueryText(query); got != tc.want {
			t.Fatalf("%s: buildQueryText() = %q, want %q", name, got, tc.want)
		}
	}
}

func TestNormalizeURLIgnoresTrackingAndCase(t *testing.T) {
	cases := map[string]string{
		"https://www.Example.com/Post?utm_source=x&id=7#frag": "https://example.com/Post?id=7",
		"https://example.com/":                                "https://example.com",
		"https://example.com/Post?id=7":                       "https://example.com/Post?id=7",
	}
	for raw, want := range cases {
		if got := normalizeURL(raw); got != want {
			t.Fatalf("normalizeURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestExcerptTextTrimsToWordBoundary(t *testing.T) {
	text := "The quick brown fox jumps over the lazy dog and keeps running for a while afterwards."
	got := excerptText(text, 30)
	if len([]rune(got)) > 31 {
		t.Fatalf("excerpt too long: %q", got)
	}
	if got[len(got)-len("…"):] != "…" {
		t.Fatalf("truncated excerpt must be marked: %q", got)
	}
	if short := excerptText("short text", 50); short != "short text" {
		t.Fatalf("excerptText() = %q, want the text unchanged", short)
	}
}
