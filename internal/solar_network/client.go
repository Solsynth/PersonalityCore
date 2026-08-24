package solar_network

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL     string
	accessToken string
	httpClient  *http.Client
}

func NewClient(baseURL, accessToken string) *Client {
	return &Client{
		baseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		accessToken: strings.TrimSpace(accessToken),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// NewClientWithHTTP creates a Client with a caller-supplied HTTP client.
// This is used for user-scoped tools where a shared HTTP client is preferred.
func NewClientWithHTTP(baseURL, accessToken string, hc *http.Client) *Client {
	return &Client{
		baseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		accessToken: strings.TrimSpace(accessToken),
		httpClient:  hc,
	}
}

func (c *Client) ResolveAccountByName(ctx context.Context, accountName string) (*Account, error) {
	var out Account
	if err := c.doJSON(ctx, http.MethodGet, "/passport/accounts/"+url.PathEscape(strings.TrimSpace(accountName)), nil, nil, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.ID) == "" {
		return nil, fmt.Errorf("solar account lookup for %q returned empty id", accountName)
	}
	return &out, nil
}

func (c *Client) GetAccountByID(ctx context.Context, accountID string) (*Account, error) {
	var out Account
	if err := c.doJSON(ctx, http.MethodGet, "/passport/accounts/"+url.PathEscape(strings.TrimSpace(accountID)), nil, nil, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.ID) == "" {
		return nil, fmt.Errorf("solar account lookup for id %q returned empty id", accountID)
	}
	return &out, nil
}

func (c *Client) GetAccountProfile(ctx context.Context, accountName string) (AccountProfile, error) {
	out := AccountProfile{}
	if err := c.doJSON(ctx, http.MethodGet, "/passport/accounts/"+url.PathEscape(strings.TrimSpace(accountName)), nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateDirectMessage(ctx context.Context, targetAccountID string) (*ChatRoom, error) {
	body := map[string]any{
		"related_user_id": strings.TrimSpace(targetAccountID),
		"encryption_mode": 0,
	}
	var out ChatRoom
	if err := c.doJSON(ctx, http.MethodPost, "/messager/chat/direct", nil, body, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.ID) == "" {
		return nil, fmt.Errorf("solar direct message creation returned empty room id")
	}
	return &out, nil
}

func (c *Client) ListJoinedRooms(ctx context.Context) ([]ChatRoom, error) {
	body := map[string]any{
		"last_sync_timestamp": 0,
	}
	var out struct {
		Changes []struct {
			Type string    `json:"type"`
			Room *ChatRoom `json:"room"`
		} `json:"changes"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/messager/chat/rooms/sync", nil, body, &out); err != nil {
		return nil, err
	}
	rooms := make([]ChatRoom, 0, len(out.Changes))
	for _, change := range out.Changes {
		if change.Room == nil || strings.TrimSpace(change.Type) == "removed" {
			continue
		}
		rooms = append(rooms, *change.Room)
	}
	return rooms, nil
}

func (c *Client) FindExistingDirectMessage(ctx context.Context, targetAccountID string) (*ChatRoom, error) {
	rooms, err := c.ListJoinedRooms(ctx)
	if err != nil {
		return nil, err
	}
	targetAccountID = strings.TrimSpace(targetAccountID)
	for _, room := range rooms {
		if room.Type != 1 {
			continue
		}
		for _, member := range room.DirectMembers {
			if strings.TrimSpace(member.AccountID) == targetAccountID {
				roomCopy := room
				return &roomCopy, nil
			}
		}
	}
	return nil, nil
}

func (c *Client) SendMessage(ctx context.Context, roomID, content string) (*ChatMessage, error) {
	body := map[string]any{
		"content": strings.TrimSpace(content),
	}
	var out ChatMessage
	path := fmt.Sprintf("/messager/chat/%s/messages", url.PathEscape(strings.TrimSpace(roomID)))
	if err := c.doJSON(ctx, http.MethodPost, path, nil, body, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.ID) == "" {
		return nil, fmt.Errorf("solar message send returned empty message id")
	}
	return &out, nil
}

func (c *Client) ListMessages(ctx context.Context, roomID string, offset, take int) ([]ChatMessage, error) {
	if offset < 0 {
		offset = 0
	}
	if take < 1 {
		take = 50
	}
	query := url.Values{}
	query.Set("offset", fmt.Sprintf("%d", offset))
	query.Set("take", fmt.Sprintf("%d", take))

	var out []ChatMessage
	path := fmt.Sprintf("/messager/chat/%s/messages", url.PathEscape(strings.TrimSpace(roomID)))
	if err := c.doJSON(ctx, http.MethodGet, path, query, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetMessage(ctx context.Context, roomID, messageID string) (*ChatMessage, error) {
	var out ChatMessage
	path := fmt.Sprintf("/messager/chat/%s/messages/%s", url.PathEscape(strings.TrimSpace(roomID)), url.PathEscape(strings.TrimSpace(messageID)))
	if err := c.doJSON(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetPost(ctx context.Context, postID string) (Post, error) {
	out := Post{}
	if err := c.doJSON(ctx, http.MethodGet, "/sphere/posts/"+url.PathEscape(strings.TrimSpace(postID)), nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListPublisherPosts(ctx context.Context, accountName string, offset, take int) (*PaginatedPosts, error) {
	if offset < 0 {
		offset = 0
	}
	if take < 1 {
		take = 20
	}
	query := url.Values{}
	query.Set("offset", fmt.Sprintf("%d", offset))
	query.Set("take", fmt.Sprintf("%d", take))

	var out []Post
	headers, err := c.doJSONWithHeaders(ctx, http.MethodGet, "/sphere/publishers/"+url.PathEscape(strings.TrimSpace(accountName))+"/posts", query, nil, &out)
	if err != nil {
		return nil, err
	}
	return &PaginatedPosts{Items: out, Total: parseTotalHeader(headers)}, nil
}

func (c *Client) ListFeed(ctx context.Context, offset, take int, shuffle bool) (*PaginatedPosts, error) {
	if offset < 0 {
		offset = 0
	}
	if take < 1 {
		take = 20
	}
	query := url.Values{}
	query.Set("offset", fmt.Sprintf("%d", offset))
	query.Set("take", fmt.Sprintf("%d", take))
	if shuffle {
		query.Set("shuffle", "true")
	}
	var out []Post
	headers, err := c.doJSONWithHeaders(ctx, http.MethodGet, "/sphere/posts", query, nil, &out)
	if err != nil {
		return nil, err
	}
	return &PaginatedPosts{Items: out, Total: parseTotalHeader(headers)}, nil
}

func (c *Client) SearchPosts(ctx context.Context, q string, offset, take int) (*PaginatedPosts, error) {
	if offset < 0 {
		offset = 0
	}
	if take < 1 {
		take = 20
	}
	query := url.Values{}
	query.Set("q", strings.TrimSpace(q))
	query.Set("offset", fmt.Sprintf("%d", offset))
	query.Set("take", fmt.Sprintf("%d", take))
	var out []Post
	headers, err := c.doJSONWithHeaders(ctx, http.MethodGet, "/sphere/posts/search", query, nil, &out)
	if err != nil {
		return nil, err
	}
	return &PaginatedPosts{Items: out, Total: parseTotalHeader(headers)}, nil
}

func (c *Client) CreatePost(ctx context.Context, publisherName string, body map[string]any) (Post, error) {
	query := url.Values{}
	if strings.TrimSpace(publisherName) != "" {
		query.Set("pub", strings.TrimSpace(publisherName))
	}
	var out Post
	if err := c.doJSON(ctx, http.MethodPost, "/sphere/posts", query, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ReplyToPost(ctx context.Context, publisherName, postID, content string) (Post, error) {
	body := map[string]any{
		"content":         strings.TrimSpace(content),
		"replied_post_id": strings.TrimSpace(postID),
	}
	return c.CreatePost(ctx, publisherName, body)
}

func (c *Client) RepostPost(ctx context.Context, publisherName, postID string, comment *string) (Post, error) {
	body := map[string]any{
		"forwarded_post_id": strings.TrimSpace(postID),
	}
	if comment != nil && strings.TrimSpace(*comment) != "" {
		body["content"] = strings.TrimSpace(*comment)
	}
	return c.CreatePost(ctx, publisherName, body)
}

func (c *Client) ReactToPost(ctx context.Context, postID, symbol string, attitude int) error {
	body := map[string]any{
		"symbol":   strings.TrimSpace(symbol),
		"attitude": attitude,
	}
	path := "/sphere/posts/" + url.PathEscape(strings.TrimSpace(postID)) + "/reactions"
	return c.doJSON(ctx, http.MethodPost, path, nil, body, nil)
}

func (c *Client) ListPostReplies(ctx context.Context, postID string, offset, take int) (*PaginatedPosts, error) {
	if offset < 0 {
		offset = 0
	}
	if take < 1 {
		take = 20
	}
	query := url.Values{}
	query.Set("offset", fmt.Sprintf("%d", offset))
	query.Set("take", fmt.Sprintf("%d", take))

	var out []Post
	headers, err := c.doJSONWithHeaders(ctx, http.MethodGet, "/sphere/posts/"+url.PathEscape(strings.TrimSpace(postID))+"/replies", query, nil, &out)
	if err != nil {
		return nil, err
	}
	return &PaginatedPosts{Items: out, Total: parseTotalHeader(headers)}, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	_, err := c.doJSONWithHeaders(ctx, method, path, query, body, out)
	return err
}

func (c *Client) doJSONWithHeaders(ctx context.Context, method, path string, query url.Values, body any, out any) (http.Header, error) {
	requestURL := c.baseURL + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("solar %s %s failed with status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out == nil || len(bytes.TrimSpace(payload)) == 0 {
		return resp.Header, nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return nil, fmt.Errorf("decode solar %s %s response: %w", method, path, err)
	}
	return resp.Header, nil
}

func parseTotalHeader(headers http.Header) int {
	if headers == nil {
		return 0
	}
	totalRaw := strings.TrimSpace(headers.Get("X-Total"))
	if totalRaw == "" {
		return 0
	}
	total, err := strconv.Atoi(totalRaw)
	if err != nil {
		return 0
	}
	return total
}

func (c *Client) doMultipart(ctx context.Context, method, path string, fields map[string]string, fileField, filename string, content []byte, out any) error {
	_, err := c.doMultipartWithHeaders(ctx, method, path, fields, fileField, filename, content, out)
	return err
}

func (c *Client) doMultipartWithHeaders(ctx context.Context, method, path string, fields map[string]string, fileField, filename string, content []byte, out any) (http.Header, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add file part
	part, err := writer.CreateFormFile(fileField, filename)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(content); err != nil {
		return nil, err
	}

	// Add form fields
	for key, val := range fields {
		if err := writer.WriteField(key, val); err != nil {
			return nil, err
		}
	}

	if err := writer.Close(); err != nil {
		return nil, err
	}

	requestURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, requestURL, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("solar %s %s failed with status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out == nil || len(bytes.TrimSpace(payload)) == 0 {
		return resp.Header, nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return nil, fmt.Errorf("decode solar %s %s response: %w", method, path, err)
	}
	return resp.Header, nil
}

// ── Drive (DysonFS) ─────────────────────────────────────────────────────

func (c *Client) ListMyFiles(ctx context.Context, q url.Values) ([]map[string]any, int, error) {
	var out []map[string]any
	headers, err := c.doJSONWithHeaders(ctx, http.MethodGet, "/drive/files/me", q, nil, &out)
	if err != nil {
		return nil, 0, err
	}
	return out, parseTotalHeader(headers), nil
}

func (c *Client) GetFile(ctx context.Context, id string) (map[string]any, error) {
	var out map[string]any
	path := "/drive/files/" + url.PathEscape(strings.TrimSpace(id)) + "/info"
	if err := c.doJSON(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateFolder(ctx context.Context, name, parentID string) (map[string]any, error) {
	body := map[string]any{"name": strings.TrimSpace(name)}
	if strings.TrimSpace(parentID) != "" {
		body["parent_id"] = strings.TrimSpace(parentID)
	}
	var out map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/drive/files/folders", nil, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) UploadTextFile(ctx context.Context, name, parentID string, content []byte) (map[string]any, error) {
	fields := map[string]string{"name": strings.TrimSpace(name)}
	if strings.TrimSpace(parentID) != "" {
		fields["parent_id"] = strings.TrimSpace(parentID)
	}
	var out map[string]any
	if err := c.doMultipart(ctx, http.MethodPost, "/drive/files/upload/direct", fields, "file", strings.TrimSpace(name), content, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) RecycleFile(ctx context.Context, id string) error {
	path := "/drive/files/" + url.PathEscape(strings.TrimSpace(id)) + "/recycle"
	return c.doJSON(ctx, http.MethodPost, path, nil, nil, nil)
}

func (c *Client) RestoreFile(ctx context.Context, id string) error {
	path := "/drive/files/" + url.PathEscape(strings.TrimSpace(id)) + "/restore"
	return c.doJSON(ctx, http.MethodPost, path, nil, nil, nil)
}

func (c *Client) ListRecycleBin(ctx context.Context, take, offset int) ([]map[string]any, int, error) {
	if take < 1 {
		take = 20
	}
	if offset < 0 {
		offset = 0
	}
	query := url.Values{}
	query.Set("recycled", "true")
	query.Set("take", fmt.Sprintf("%d", take))
	query.Set("offset", fmt.Sprintf("%d", offset))
	var out []map[string]any
	headers, err := c.doJSONWithHeaders(ctx, http.MethodGet, "/drive/files/me", query, nil, &out)
	if err != nil {
		return nil, 0, err
	}
	return out, parseTotalHeader(headers), nil
}

func (c *Client) GetStorageQuota(ctx context.Context) (map[string]any, error) {
	quota := map[string]any{}
	if err := c.doJSON(ctx, http.MethodGet, "/drive/billing/quota", nil, nil, &quota); err != nil {
		return nil, err
	}
	usage := map[string]any{}
	if err := c.doJSON(ctx, http.MethodGet, "/drive/billing/usage", nil, nil, &usage); err != nil {
		return nil, err
	}
	for k, v := range usage {
		quota[k] = v
	}
	return quota, nil
}

// ── Wallet ───────────────────────────────────────────────────────────────

func (c *Client) ListWallets(ctx context.Context) ([]map[string]any, error) {
	var out []map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/wallet/wallets/all", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListOrders(ctx context.Context, take, offset int) ([]map[string]any, int, error) {
	if take < 1 {
		take = 20
	}
	if offset < 0 {
		offset = 0
	}
	query := url.Values{}
	query.Set("take", fmt.Sprintf("%d", take))
	query.Set("offset", fmt.Sprintf("%d", offset))
	var out []map[string]any
	headers, err := c.doJSONWithHeaders(ctx, http.MethodGet, "/wallet/orders/mine", query, nil, &out)
	if err != nil {
		return nil, 0, err
	}
	return out, parseTotalHeader(headers), nil
}

// ── Ring / Metoer (notifications) ────────────────────────────────────────

func (c *Client) ListNotifications(ctx context.Context, take, offset int) ([]map[string]any, int, error) {
	if take < 1 {
		take = 20
	}
	if offset < 0 {
		offset = 0
	}
	query := url.Values{}
	query.Set("take", fmt.Sprintf("%d", take))
	query.Set("offset", fmt.Sprintf("%d", offset))
	var out []map[string]any
	headers, err := c.doJSONWithHeaders(ctx, http.MethodGet, "/ring/notifications", query, nil, &out)
	if err != nil {
		return nil, 0, err
	}
	return out, parseTotalHeader(headers), nil
}

func (c *Client) UnreadNotificationCount(ctx context.Context) (int, error) {
	var out struct {
		Count int `json:"count"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/ring/notifications/count", nil, nil, &out); err != nil {
		return 0, err
	}
	return out.Count, nil
}

func (c *Client) MarkAllNotificationsRead(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodPost, "/ring/notifications/all/read", nil, nil, nil)
}

// ── Sphere ───────────────────────────────────────────────────────────────

func (c *Client) ReadWebpage(ctx context.Context, rawURL string) (map[string]any, error) {
	query := url.Values{}
	query.Set("url", strings.TrimSpace(rawURL))
	var out map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/sphere/api/scrap/link", query, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListStickers(ctx context.Context, take, offset int) ([]map[string]any, int, error) {
	if take < 1 {
		take = 20
	}
	if offset < 0 {
		offset = 0
	}
	query := url.Values{}
	query.Set("take", fmt.Sprintf("%d", take))
	query.Set("offset", fmt.Sprintf("%d", offset))
	var out []map[string]any
	headers, err := c.doJSONWithHeaders(ctx, http.MethodGet, "/sphere/api/stickers", query, nil, &out)
	if err != nil {
		return nil, 0, err
	}
	return out, parseTotalHeader(headers), nil
}

func (c *Client) ListSurveys(ctx context.Context, take, offset int) ([]map[string]any, int, error) {
	if take < 1 {
		take = 20
	}
	if offset < 0 {
		offset = 0
	}
	query := url.Values{}
	query.Set("take", fmt.Sprintf("%d", take))
	query.Set("offset", fmt.Sprintf("%d", offset))
	var out []map[string]any
	headers, err := c.doJSONWithHeaders(ctx, http.MethodGet, "/sphere/api/surveys/me", query, nil, &out)
	if err != nil {
		return nil, 0, err
	}
	return out, parseTotalHeader(headers), nil
}

// ── Passport ─────────────────────────────────────────────────────────────

func (c *Client) GetMyLeveling(ctx context.Context) ([]map[string]any, int, error) {
	query := url.Values{}
	query.Set("take", "20")
	query.Set("offset", "0")
	var out []map[string]any
	headers, err := c.doJSONWithHeaders(ctx, http.MethodGet, "/passport/accounts/me/leveling", query, nil, &out)
	if err != nil {
		return nil, 0, err
	}
	return out, parseTotalHeader(headers), nil
}

// ── Stargate ─────────────────────────────────────────────────────────────

func (c *Client) ListRelationships(ctx context.Context) ([]map[string]any, error) {
	var out []map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/stargate/relationships", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) FollowAccount(ctx context.Context, accountID string) error {
	path := "/stargate/relationships/" + url.PathEscape(strings.TrimSpace(accountID)) + "/friends"
	return c.doJSON(ctx, http.MethodPost, path, nil, map[string]any{}, nil)
}

func (c *Client) UnfollowAccount(ctx context.Context, accountID string) error {
	path := "/stargate/relationships/" + url.PathEscape(strings.TrimSpace(accountID))
	return c.doJSON(ctx, http.MethodDelete, path, nil, nil, nil)
}

func (c *Client) SearchAccounts(ctx context.Context, q string, take, offset int) ([]map[string]any, int, error) {
	if take < 1 {
		take = 20
	}
	if offset < 0 {
		offset = 0
	}
	query := url.Values{}
	query.Set("query", strings.TrimSpace(q))
	query.Set("take", fmt.Sprintf("%d", take))
	query.Set("offset", fmt.Sprintf("%d", offset))
	var out []map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/stargate/accounts/search", query, nil, &out); err != nil {
		return nil, 0, err
	}
	return out, len(out), nil
}
