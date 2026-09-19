package webindex

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/persona/internal/database"
	"src.solsynth.dev/sosys/persona/internal/websearch"
)

func newTestIndex(t *testing.T) (*Index, *database.DB) {
	t.Helper()
	gormDB, err := gorm.Open(sqlite.Open("file:"+ulid.Make().String()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	db := &database.DB{DB: gormDB}
	if err := db.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	return New(db), db
}

func storedPages(t *testing.T, db *database.DB) []database.WebSearchPage {
	t.Helper()
	var rows []database.WebSearchPage
	if err := db.DB.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestUpsertPagesRefreshesExistingURL(t *testing.T) {
	index, db := newTestIndex(t)
	ctx := context.Background()
	const url = "https://example.com/article"

	if err := index.UpsertPages(ctx, []websearch.Page{{
		URL:   url,
		Title: "First title",
		Text:  "alpha beta gamma",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := index.UpsertPages(ctx, []websearch.Page{{
		URL:       url,
		Title:     "Second title",
		Text:      "delta epsilon",
		FetchedAt: time.Now().Add(time.Hour),
	}}); err != nil {
		t.Fatal(err)
	}

	rows := storedPages(t, db)
	if len(rows) != 1 {
		t.Fatalf("want 1 stored row after re-crawling one URL, got %d", len(rows))
	}
	if rows[0].Title != "Second title" {
		t.Fatalf("want refreshed title, got %q", rows[0].Title)
	}
	if !strings.Contains(rows[0].Text, "delta") || strings.Contains(rows[0].Text, "alpha") {
		t.Fatalf("want refreshed text, got %q", rows[0].Text)
	}

	hits, err := index.SearchPages(ctx, "alpha", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("stale text should not be searchable, got %d hits", len(hits))
	}
}

func TestTitleMatchOutranksBodyOnlyMatch(t *testing.T) {
	index, _ := newTestIndex(t)
	ctx := context.Background()

	if err := index.UpsertPages(ctx, []websearch.Page{
		{URL: "https://a.example/body", Title: "Cooking notes", Text: "quantum topics appear once in this body"},
		{URL: "https://b.example/title", Title: "Quantum handbook", Text: "unrelated prose about bread"},
	}); err != nil {
		t.Fatal(err)
	}

	hits, err := index.SearchPages(ctx, "quantum", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("want both pages ranked, got %d hits", len(hits))
	}
	if hits[0].URL != "https://b.example/title" {
		t.Fatalf("want the title match first, got %q", hits[0].URL)
	}
	if hits[0].Provider != "index" {
		t.Fatalf("want provider %q, got %q", "index", hits[0].Provider)
	}
}

func TestSearchPagesOnlyHitsPagesContainingAQueryToken(t *testing.T) {
	index, _ := newTestIndex(t)
	ctx := context.Background()

	if err := index.UpsertPages(ctx, []websearch.Page{{
		URL:   "https://example.com/moose",
		Title: "Northern wildlife",
		Text:  "a moose wanders through the birch forest",
	}}); err != nil {
		t.Fatal(err)
	}

	miss, err := index.SearchPages(ctx, "penguin", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(miss) != 0 {
		t.Fatalf("want no hits for an unmatched token, got %d", len(miss))
	}

	hit, err := index.SearchPages(ctx, "birch", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hit) != 1 || hit[0].URL != "https://example.com/moose" {
		t.Fatalf("want the body-only match, got %+v", hit)
	}
}

func TestSnippetContainsMatchedTermAndStaysBounded(t *testing.T) {
	index, _ := newTestIndex(t)
	ctx := context.Background()

	text := strings.Repeat("filler words for padding ", 60) + "the needle rests here" + strings.Repeat(" more filler words", 60)
	if err := index.UpsertPages(ctx, []websearch.Page{{
		URL:   "https://example.com/long",
		Title: "Long page",
		Text:  text,
	}}); err != nil {
		t.Fatal(err)
	}

	hits, err := index.SearchPages(ctx, "needle", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("want the long page, got %d hits", len(hits))
	}
	snippet := hits[0].Snippet
	if !strings.Contains(snippet, "needle") {
		t.Fatalf("snippet must contain the matched term, got %q", snippet)
	}
	if n := utf8.RuneCountInString(snippet); n > 400 {
		t.Fatalf("snippet must stay bounded, got %d runes", n)
	}
	if n := utf8.RuneCountInString(snippet); n >= utf8.RuneCountInString(text) {
		t.Fatalf("snippet must be a window of the page, got %d runes", n)
	}
	if !strings.Contains(snippet, ellipsis) {
		t.Fatalf("want a truncation marker, got %q", snippet)
	}
}

func TestSearchPagesRespectsLimitAndEmptyQuery(t *testing.T) {
	index, _ := newTestIndex(t)
	ctx := context.Background()

	pages := make([]websearch.Page, 0, 25)
	for n := 0; n < 25; n++ {
		pages = append(pages, websearch.Page{
			URL:   "https://example.com/shared/" + ulid.Make().String(),
			Title: "Shared topic",
			Text:  "shared body text",
		})
	}
	if err := index.UpsertPages(ctx, pages); err != nil {
		t.Fatal(err)
	}

	one, err := index.SearchPages(ctx, "shared", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 {
		t.Fatalf("want 1 hit for limit 1, got %d", len(one))
	}

	two, err := index.SearchPages(ctx, "shared", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(two) != 2 {
		t.Fatalf("want 2 hits for limit 2, got %d", len(two))
	}

	capped, err := index.SearchPages(ctx, "shared", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped) != maxResultLimit {
		t.Fatalf("want the hard cap of %d hits, got %d", maxResultLimit, len(capped))
	}

	for _, query := range []string{"", "   ", "!!!"} {
		empty, err := index.SearchPages(ctx, query, 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(empty) != 0 {
			t.Fatalf("want no hits for query %q, got %d", query, len(empty))
		}
	}
}

func TestUpsertPagesSkipsPagesWithoutURLOrText(t *testing.T) {
	index, db := newTestIndex(t)
	ctx := context.Background()

	if err := index.UpsertPages(ctx, []websearch.Page{
		{URL: "", Title: "No URL", Text: "orphan body"},
		{URL: "https://example.com/blank", Title: "No text", Text: "   "},
		{URL: "https://example.com/kept", Title: "Kept", Text: "real body"},
	}); err != nil {
		t.Fatal(err)
	}

	rows := storedPages(t, db)
	if len(rows) != 1 {
		t.Fatalf("want only the complete page stored, got %d rows", len(rows))
	}
	if rows[0].URL != "https://example.com/kept" {
		t.Fatalf("unexpected stored page %q", rows[0].URL)
	}
}

func TestUpsertPagesDerivesHostAndBoundsStoredText(t *testing.T) {
	index, db := newTestIndex(t)
	ctx := context.Background()

	if err := index.UpsertPages(ctx, []websearch.Page{{
		URL:   "https://www.Example.com/very/long",
		Title: "Huge",
		Text:  strings.Repeat("x", maxStoredRunes+5000),
	}}); err != nil {
		t.Fatal(err)
	}

	rows := storedPages(t, db)
	if len(rows) != 1 {
		t.Fatalf("want 1 stored row, got %d", len(rows))
	}
	if rows[0].Host != "example.com" {
		t.Fatalf("want host %q, got %q", "example.com", rows[0].Host)
	}
	if got := utf8.RuneCountInString(rows[0].Text); got != maxStoredRunes {
		t.Fatalf("want text truncated to %d runes, got %d", maxStoredRunes, got)
	}
}

func TestNilIndexIsInert(t *testing.T) {
	index := New(nil)
	if index != nil {
		t.Fatal("want nil index for a nil database")
	}
	if err := index.UpsertPages(context.Background(), []websearch.Page{{URL: "https://example.com", Text: "body"}}); err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	hits, err := index.SearchPages(context.Background(), "body", 5)
	if err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("want no hits, got %d", len(hits))
	}
}
