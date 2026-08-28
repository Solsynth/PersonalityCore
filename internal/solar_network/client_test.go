package solar_network

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// testHandler records what the server received and returns a fixed response.
type testHandler struct {
	method string
	path   string
	query  url.Values
	body   string
	header http.Header
	t      *testing.T
}

func (h *testHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.method = r.Method
	h.path = r.URL.Path
	h.query = r.URL.Query()
	h.header = r.Header
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		h.body = string(b)
	}
	w.Header().Set("Content-Type", "application/json")
}

func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	ts := httptest.NewServer(handler)
	c := NewClient(ts.URL, "test-token")
	return ts, c
}

// ── Drive ────────────────────────────────────────────────────────────────

func TestListMyFiles(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		w.Header().Set("X-Total", "42")
		fmt.Fprint(w, `[{"id":"f1","name":"readme.txt"}]`)
	})
	defer ts.Close()

	ctx := context.Background()
	q := url.Values{"take": {"10"}, "offset": {"5"}}
	items, total, err := c.ListMyFiles(ctx, q)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.method != "GET" {
		t.Errorf("method = %q, want GET", h.method)
	}
	if h.path != "/drive/files/me" {
		t.Errorf("path = %q, want /drive/files/me", h.path)
	}
	if h.query.Get("take") != "10" || h.query.Get("offset") != "5" {
		t.Errorf("query = %v, want take=10 offset=5", h.query)
	}
	if h.header.Get("Authorization") != "Bearer test-token" {
		t.Errorf("Authorization = %q, want Bearer test-token", h.header.Get("Authorization"))
	}
	if len(items) != 1 || items[0]["id"] != "f1" {
		t.Errorf("items = %v, want [{id:f1}]", items)
	}
	if total != 42 {
		t.Errorf("total = %d, want 42", total)
	}
}

func TestGetFile(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		fmt.Fprint(w, `{"id":"f1","name":"readme.txt","size":123}`)
	})
	defer ts.Close()

	ctx := context.Background()
	info, err := c.GetFile(ctx, "f1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.path != "/drive/files/f1/info" {
		t.Errorf("path = %q, want /drive/files/f1/info", h.path)
	}
	if info["id"] != "f1" {
		t.Errorf("id = %v, want f1", info["id"])
	}
}

func TestCreateFolder(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		fmt.Fprint(w, `{"id":"d1","name":"docs"}`)
	})
	defer ts.Close()

	ctx := context.Background()
	out, err := c.CreateFolder(ctx, "docs", "parent1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.method != "POST" {
		t.Errorf("method = %q, want POST", h.method)
	}
	if h.path != "/drive/files/folders" {
		t.Errorf("path = %q, want /drive/files/folders", h.path)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(h.body), &body); err != nil {
		t.Fatalf("body unmarshal: %v", err)
	}
	if body["name"] != "docs" {
		t.Errorf("body.name = %v, want docs", body["name"])
	}
	if body["parent_id"] != "parent1" {
		t.Errorf("body.parent_id = %v, want parent1", body["parent_id"])
	}
	if out["id"] != "d1" {
		t.Errorf("out.id = %v, want d1", out["id"])
	}
}

func TestCreateFolderNoParent(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		fmt.Fprint(w, `{"id":"d1"}`)
	})
	defer ts.Close()

	ctx := context.Background()
	_, err := c.CreateFolder(ctx, "docs", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(h.body), &body); err != nil {
		t.Fatalf("body unmarshal: %v", err)
	}
	if _, ok := body["parent_id"]; ok {
		t.Error("body should not contain parent_id when empty")
	}
}

func TestUploadTextFile(t *testing.T) {
	var receivedFile []byte
	var receivedFields map[string]string
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Record request metadata (but don't consume body via h.ServeHTTP)
		h.method = r.Method
		h.path = r.URL.Path
		h.query = r.URL.Query()
		h.header = r.Header
		// Parse multipart form
		mediatype, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediatype != "multipart/form-data" {
			t.Errorf("Content-Type = %q, want multipart/form-data", r.Header.Get("Content-Type"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		receivedFields = map[string]string{}
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() == "file" {
				receivedFile, _ = io.ReadAll(p)
			} else {
				b, _ := io.ReadAll(p)
				receivedFields[p.FormName()] = string(b)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"f2","name":"test.txt"}`)
	})
	defer ts.Close()

	ctx := context.Background()
	content := []byte("hello world")
	out, err := c.UploadTextFile(ctx, "test.txt", "p1", content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.method != "POST" {
		t.Errorf("method = %q, want POST", h.method)
	}
	if h.path != "/drive/files/upload/direct" {
		t.Errorf("path = %q, want /drive/files/upload/direct", h.path)
	}
	if string(receivedFile) != "hello world" {
		t.Errorf("file content = %q, want 'hello world'", string(receivedFile))
	}
	if receivedFields["name"] != "test.txt" {
		t.Errorf("field name = %q, want test.txt", receivedFields["name"])
	}
	if receivedFields["parent_id"] != "p1" {
		t.Errorf("field parent_id = %q, want p1", receivedFields["parent_id"])
	}
	if out["id"] != "f2" {
		t.Errorf("out.id = %v, want f2", out["id"])
	}
}

func TestRecycleFile(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
	})
	defer ts.Close()

	ctx := context.Background()
	err := c.RecycleFile(ctx, "f1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.method != "POST" {
		t.Errorf("method = %q, want POST", h.method)
	}
	if h.path != "/drive/files/f1/recycle" {
		t.Errorf("path = %q, want /drive/files/f1/recycle", h.path)
	}
}

func TestRestoreFile(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
	})
	defer ts.Close()

	ctx := context.Background()
	err := c.RestoreFile(ctx, "f1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.method != "POST" {
		t.Errorf("method = %q, want POST", h.method)
	}
	if h.path != "/drive/files/f1/restore" {
		t.Errorf("path = %q, want /drive/files/f1/restore", h.path)
	}
}

func TestListRecycleBin(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		w.Header().Set("X-Total", "3")
		fmt.Fprint(w, `[{"id":"r1"}]`)
	})
	defer ts.Close()

	ctx := context.Background()
	items, total, err := c.ListRecycleBin(ctx, 10, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.path != "/drive/files/me" {
		t.Errorf("path = %q, want /drive/files/me", h.path)
	}
	if h.query.Get("recycled") != "true" {
		t.Errorf("query.recycled = %q, want true", h.query.Get("recycled"))
	}
	if h.query.Get("take") != "10" {
		t.Errorf("query.take = %q, want 10", h.query.Get("take"))
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(items) != 1 {
		t.Errorf("items len = %d, want 1", len(items))
	}
}

func TestGetStorageQuota(t *testing.T) {
	h := &testHandler{t: t}
	callCount := 0
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		callCount++
		if r.URL.Path == "/drive/billing/quota" {
			fmt.Fprint(w, `{"based_quota":1000,"extra_quota":500,"total_quota":1500}`)
		} else if r.URL.Path == "/drive/billing/usage" {
			fmt.Fprint(w, `{"used_quota":200,"total_file_count":10,"total_usage_bytes":50000}`)
		}
	})
	defer ts.Close()

	ctx := context.Background()
	out, err := c.GetStorageQuota(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 2 {
		t.Errorf("call count = %d, want 2", callCount)
	}
	if out["based_quota"] != float64(1000) {
		t.Errorf("based_quota = %v, want 1000", out["based_quota"])
	}
	if out["total_usage_bytes"] != float64(50000) {
		t.Errorf("total_usage_bytes = %v, want 50000", out["total_usage_bytes"])
	}
	if out["total_quota"] != float64(1500) {
		t.Errorf("total_quota = %v, want 1500", out["total_quota"])
	}
}

// ── Wallet ───────────────────────────────────────────────────────────────

func TestListWallets(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		fmt.Fprint(w, `[{"id":"w1","name":"Main"}]`)
	})
	defer ts.Close()

	ctx := context.Background()
	wallets, err := c.ListWallets(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.method != "GET" {
		t.Errorf("method = %q, want GET", h.method)
	}
	if h.path != "/wallet/wallets/all" {
		t.Errorf("path = %q, want /wallet/wallets/all", h.path)
	}
	if len(wallets) != 1 || wallets[0]["id"] != "w1" {
		t.Errorf("wallets = %v, want [{id:w1}]", wallets)
	}
}

func TestListOrders(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		w.Header().Set("X-Total", "15")
		fmt.Fprint(w, `[{"id":"o1"}]`)
	})
	defer ts.Close()

	ctx := context.Background()
	items, total, err := c.ListOrders(ctx, 5, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.path != "/wallet/orders/mine" {
		t.Errorf("path = %q, want /wallet/orders/mine", h.path)
	}
	if h.query.Get("take") != "5" {
		t.Errorf("query.take = %q, want 5", h.query.Get("take"))
	}
	if total != 15 {
		t.Errorf("total = %d, want 15", total)
	}
	if len(items) != 1 {
		t.Errorf("items len = %d, want 1", len(items))
	}
}

// ── Ring / Metoer ────────────────────────────────────────────────────────

func TestListNotifications(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		w.Header().Set("X-Total", "8")
		fmt.Fprint(w, `[{"id":"n1"}]`)
	})
	defer ts.Close()

	ctx := context.Background()
	items, total, err := c.ListNotifications(ctx, 10, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.method != "GET" {
		t.Errorf("method = %q, want GET", h.method)
	}
	if h.path != "/ring/notifications" {
		t.Errorf("path = %q, want /ring/notifications", h.path)
	}
	if total != 8 {
		t.Errorf("total = %d, want 8", total)
	}
	if len(items) != 1 {
		t.Errorf("items len = %d, want 1", len(items))
	}
}

func TestUnreadNotificationCount(t *testing.T) {
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"count":7}`)
	})
	defer ts.Close()

	ctx := context.Background()
	count, err := c.UnreadNotificationCount(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 7 {
		t.Errorf("count = %d, want 7", count)
	}
}

func TestMarkAllNotificationsRead(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
	})
	defer ts.Close()

	ctx := context.Background()
	err := c.MarkAllNotificationsRead(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.method != "POST" {
		t.Errorf("method = %q, want POST", h.method)
	}
	if h.path != "/ring/notifications/all/read" {
		t.Errorf("path = %q, want /ring/notifications/all/read", h.path)
	}
}

// ── Sphere ───────────────────────────────────────────────────────────────

func TestReadWebpage(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		fmt.Fprint(w, `{"title":"Example","url":"https://example.com"}`)
	})
	defer ts.Close()

	ctx := context.Background()
	out, err := c.ReadWebpage(ctx, "https://example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.method != "GET" {
		t.Errorf("method = %q, want GET", h.method)
	}
	if h.path != "/sphere/api/scrap/link" {
		t.Errorf("path = %q, want /sphere/api/scrap/link", h.path)
	}
	if h.query.Get("url") != "https://example.com" {
		t.Errorf("query.url = %q, want https://example.com", h.query.Get("url"))
	}
	if out["title"] != "Example" {
		t.Errorf("title = %v, want Example", out["title"])
	}
}

func TestListStickers(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		w.Header().Set("X-Total", "5")
		fmt.Fprint(w, `[{"id":"s1"}]`)
	})
	defer ts.Close()

	ctx := context.Background()
	items, total, err := c.ListStickers(ctx, 20, 0, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.path != "/sphere/api/stickers" {
		t.Errorf("path = %q, want /sphere/api/stickers", h.path)
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(items) != 1 {
		t.Errorf("items len = %d, want 1", len(items))
	}
}

func TestListStickersWithQuery(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		w.Header().Set("X-Total", "2")
		fmt.Fprint(w, `[{"id":"s2"}]`)
	})
	defer ts.Close()

	ctx := context.Background()
	items, total, err := c.ListStickers(ctx, 10, 0, "cat", "usage")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.query.Get("query") != "cat" {
		t.Errorf("query = %q, want cat", h.query.Get("query"))
	}
	if h.query.Get("order") != "usage" {
		t.Errorf("order = %q, want usage", h.query.Get("order"))
	}
	if total != 2 {
		t.Errorf("total = %d, want 2", total)
	}
	if len(items) != 1 {
		t.Errorf("items len = %d, want 1", len(items))
	}
}

func TestSearchStickers(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		w.Header().Set("X-Total", "3")
		fmt.Fprint(w, `[{"id":"s3","slug":"happy"}]`)
	})
	defer ts.Close()

	ctx := context.Background()
	items, total, err := c.SearchStickers(ctx, "happy", 10, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.path != "/sphere/api/stickers/search" {
		t.Errorf("path = %q, want /sphere/api/stickers/search", h.path)
	}
	if h.query.Get("query") != "happy" {
		t.Errorf("query = %q, want happy", h.query.Get("query"))
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(items) != 1 {
		t.Errorf("items len = %d, want 1", len(items))
	}
}

func TestGetPackStickers(t *testing.T) {
	callCount := 0
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		callCount++
		if callCount == 1 {
			// First call: GET /stickers/{id} (pack info)
			fmt.Fprint(w, `{"id":"pack-123","prefix":"cat","name":"Cats"}`)
		} else {
			// Second call: GET /stickers/{id}/content (stickers)
			fmt.Fprint(w, `[{"id":"stk1","slug":"happy"},{"id":"stk2","slug":"wave"}]`)
		}
	})
	defer ts.Close()

	ctx := context.Background()
	items, err := c.GetPackStickers(ctx, "pack-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	if items[0]["identifier"] != "cat+happy" {
		t.Errorf("items[0].identifier = %q, want cat+happy", items[0]["identifier"])
	}
	if items[1]["identifier"] != "cat+wave" {
		t.Errorf("items[1].identifier = %q, want cat+wave", items[1]["identifier"])
	}
}

func TestListSurveys(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		w.Header().Set("X-Total", "2")
		fmt.Fprint(w, `[{"id":"sv1"}]`)
	})
	defer ts.Close()

	ctx := context.Background()
	items, total, err := c.ListSurveys(ctx, 20, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.path != "/sphere/api/surveys/me" {
		t.Errorf("path = %q, want /sphere/api/surveys/me", h.path)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2", total)
	}
	if len(items) != 1 {
		t.Errorf("items len = %d, want 1", len(items))
	}
}

// ── Passport ─────────────────────────────────────────────────────────────

func TestGetMyLeveling(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		w.Header().Set("X-Total", "1")
		fmt.Fprint(w, `[{"level":5,"xp":1200}]`)
	})
	defer ts.Close()

	ctx := context.Background()
	items, total, err := c.GetMyLeveling(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.path != "/stargate/accounts/me/leveling" {
		t.Errorf("path = %q, want /stargate/accounts/me/leveling", h.path)
	}
	if h.query.Get("take") != "20" {
		t.Errorf("query.take = %q, want 20", h.query.Get("take"))
	}
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if len(items) != 1 || items[0]["level"] != float64(5) {
		t.Errorf("items = %v, want [{level:5}]", items)
	}
}

// ── Stargate ─────────────────────────────────────────────────────────────

func TestListRelationships(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		fmt.Fprint(w, `[{"id":"r1","name":"friend1"}]`)
	})
	defer ts.Close()

	ctx := context.Background()
	rels, err := c.ListRelationships(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.method != "GET" {
		t.Errorf("method = %q, want GET", h.method)
	}
	if h.path != "/stargate/relationships" {
		t.Errorf("path = %q, want /stargate/relationships", h.path)
	}
	if len(rels) != 1 || rels[0]["id"] != "r1" {
		t.Errorf("rels = %v, want [{id:r1}]", rels)
	}
}

func TestFollowAccount(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
	})
	defer ts.Close()

	ctx := context.Background()
	err := c.FollowAccount(ctx, "acc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.method != "POST" {
		t.Errorf("method = %q, want POST", h.method)
	}
	if h.path != "/stargate/relationships/acc123/friends" {
		t.Errorf("path = %q, want /stargate/relationships/acc123/friends", h.path)
	}
}

func TestUnfollowAccount(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
	})
	defer ts.Close()

	ctx := context.Background()
	err := c.UnfollowAccount(ctx, "acc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.method != "DELETE" {
		t.Errorf("method = %q, want DELETE", h.method)
	}
	if h.path != "/stargate/relationships/acc123" {
		t.Errorf("path = %q, want /stargate/relationships/acc123", h.path)
	}
}

func TestSearchAccounts(t *testing.T) {
	h := &testHandler{t: t}
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		fmt.Fprint(w, `[{"id":"a1","name":"alice"}]`)
	})
	defer ts.Close()

	ctx := context.Background()
	items, total, err := c.SearchAccounts(ctx, "alice", 10, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.method != "GET" {
		t.Errorf("method = %q, want GET", h.method)
	}
	if h.path != "/stargate/accounts/search" {
		t.Errorf("path = %q, want /stargate/accounts/search", h.path)
	}
	if h.query.Get("query") != "alice" {
		t.Errorf("query.query = %q, want alice", h.query.Get("query"))
	}
	if h.query.Get("take") != "10" {
		t.Errorf("query.take = %q, want 10", h.query.Get("take"))
	}
	// SearchAccounts uses len(items) as total
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if len(items) != 1 {
		t.Errorf("items len = %d, want 1", len(items))
	}
}

// ── Auth header passthrough ─────────────────────────────────────────────

func TestAuthorizationHeader(t *testing.T) {
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		fmt.Fprintf(w, `{"auth":"%s"}`, auth)
	})
	defer ts.Close()

	ctx := context.Background()
	var out map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/test", nil, nil, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["auth"] != "Bearer test-token" {
		t.Errorf("auth header = %v, want Bearer test-token", out["auth"])
	}
}

func TestAuthorizationHeaderMultipart(t *testing.T) {
	ts, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		fmt.Fprintf(w, `{"auth":"%s"}`, auth)
	})
	defer ts.Close()

	ctx := context.Background()
	var out map[string]any
	err := c.doMultipart(ctx, http.MethodPost, "/upload", map[string]string{"k": "v"}, "file", "test.txt", []byte("data"), &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["auth"] != "Bearer test-token" {
		t.Errorf("auth header = %v, want Bearer test-token", out["auth"])
	}
}

// ── NewClientWithHTTP ────────────────────────────────────────────────────

func TestNewClientWithHTTP(t *testing.T) {
	hc := &http.Client{}
	c := NewClientWithHTTP("https://example.com/api", "my-token", hc)
	if c.baseURL != "https://example.com/api" {
		t.Errorf("baseURL = %q, want https://example.com/api", c.baseURL)
	}
	if c.accessToken != "my-token" {
		t.Errorf("accessToken = %q, want my-token", c.accessToken)
	}
	if c.httpClient != hc {
		t.Error("httpClient should be the supplied client")
	}
}

func TestNewClientWithHTTPTrailingSlash(t *testing.T) {
	c := NewClientWithHTTP("https://example.com/api/", "tok", nil)
	if strings.HasSuffix(c.baseURL, "/") {
		t.Errorf("baseURL should not have trailing slash, got %q", c.baseURL)
	}
}

// ── Error handling ───────────────────────────────────────────────────────

func TestServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"internal"}`)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "tok")
	ctx := context.Background()
	_, _, err := c.ListMyFiles(ctx, nil)
	if err == nil {
		t.Fatal("expected error for 500 status")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, should contain 500", err.Error())
	}
}

func TestMultipartServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"bad request"}`)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "tok")
	ctx := context.Background()
	_, err := c.UploadTextFile(ctx, "f.txt", "", []byte("data"))
	if err == nil {
		t.Fatal("expected error for 400 status")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error = %q, should contain 400", err.Error())
	}
}

// ── Empty body handling ──────────────────────────────────────────────────

func TestRecycleFileNoBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// No body
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "tok")
	ctx := context.Background()
	err := c.RecycleFile(ctx, "f1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
