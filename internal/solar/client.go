package solar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

func (c *Client) CreateDirectMessage(ctx context.Context, targetAccountID string) (*ChatRoom, error) {
	body := map[string]any{
		"related_user_id": strings.TrimSpace(targetAccountID),
		"encryption_mode": "None",
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

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	requestURL := c.baseURL + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL, bodyReader)
	if err != nil {
		return err
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
		return err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("solar %s %s failed with status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out == nil || len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode solar %s %s response: %w", method, path, err)
	}
	return nil
}
