package solar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	request := map[string]any{
		"related_user_id": strings.TrimSpace(targetAccountID),
		"encryption_mode": 0,
	}
	body := map[string]any{
		"request": request,
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
