package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type Agent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Model       string `json:"model"`
}

type Conversation struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Message struct {
	ID       string `json:"id"`
	ThreadID string `json:"thread_id"`
	Role     string `json:"role"`
	Content  string `json:"content"`
	Sequence int64  `json:"sequence"`
}

type Run struct {
	ID                string  `json:"id"`
	ThreadID          string  `json:"thread_id"`
	AgentID           string  `json:"agent_id"`
	Status            string  `json:"status"`
	Model             string  `json:"model"`
	RequestMessageID  string  `json:"request_message_id"`
	ResponseMessageID *string `json:"response_message_id"`
	Error             *string `json:"error"`
}

type RunResult struct {
	Thread          Conversation `json:"thread"`
	Run             Run          `json:"run"`
	RequestMessage  Message      `json:"request_message"`
	ResponseMessage Message      `json:"response_message"`
	Content         string       `json:"content"`
}

type SSEEvent struct {
	Event string
	Data  json.RawMessage
}

type Client struct {
	baseURL   *url.URL
	http      *http.Client
	accountID string
}

func NewClient(rawBaseURL, accountID string) (*Client, error) {
	if strings.TrimSpace(rawBaseURL) == "" {
		rawBaseURL = "http://127.0.0.1:8090"
	}
	baseURL, err := url.Parse(rawBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}

	return &Client{
		baseURL:   baseURL,
		accountID: strings.TrimSpace(accountID),
		http:      &http.Client{Timeout: 120 * time.Second},
	}, nil
}

func (c *Client) ListAgents(ctx context.Context) ([]Agent, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/agents", nil)
	if err != nil {
		return nil, err
	}

	var agents []Agent
	if err := c.doJSON(req, &agents); err != nil {
		return nil, err
	}
	return agents, nil
}

func (c *Client) CreateConversation(ctx context.Context, agentID, title string) (*Conversation, error) {
	payload := map[string]string{
		"agent_id": agentID,
		"title":    title,
	}
	req, err := c.newJSONRequest(ctx, http.MethodPost, "/api/conversations", payload)
	if err != nil {
		return nil, err
	}

	var conversation Conversation
	if err := c.doJSON(req, &conversation); err != nil {
		return nil, err
	}
	return &conversation, nil
}

func (c *Client) ListMessages(ctx context.Context, conversationID string) ([]Message, error) {
	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("/api/conversations/%s/messages?take=200&offset=0", conversationID), nil)
	if err != nil {
		return nil, err
	}

	var messages []Message
	if err := c.doJSON(req, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func (c *Client) Run(ctx context.Context, conversationID, message string, stream bool, onEvent func(SSEEvent) error) (*RunResult, error) {
	payload := map[string]any{
		"message": message,
		"stream":  stream,
	}

	if stream {
		req, err := c.newJSONRequest(ctx, http.MethodPost, fmt.Sprintf("/api/conversations/%s/runs", conversationID), payload)
		if err != nil {
			return nil, err
		}
		return c.doStream(req, onEvent)
	}

	req, err := c.newJSONRequest(ctx, http.MethodPost, fmt.Sprintf("/api/conversations/%s/runs", conversationID), payload)
	if err != nil {
		return nil, err
	}

	var result RunResult
	if err := c.doJSON(req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) newRequest(ctx context.Context, method, route string, body io.Reader) (*http.Request, error) {
	endpoint := *c.baseURL
	endpoint.Path = path.Join(strings.TrimSuffix(c.baseURL.Path, "/"), route)

	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	if c.accountID != "" {
		req.Header.Set("X-Account-Id", c.accountID)
	}
	return req, nil
}

func (c *Client) newJSONRequest(ctx context.Context, method, route string, payload any) (*http.Request, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	req, err := c.newRequest(ctx, method, route, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (c *Client) doJSON(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := decodeError(resp); err != nil {
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) doStream(req *http.Request, onEvent func(SSEEvent) error) (*RunResult, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := decodeError(resp); err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)

	var currentEvent string
	var result RunResult
	var sawCompletion bool

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			currentEvent = ""
			continue
		}

		switch {
		case strings.HasPrefix(line, "event: "):
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
		case strings.HasPrefix(line, "data: "):
			payload := json.RawMessage(strings.TrimSpace(strings.TrimPrefix(line, "data: ")))
			event := SSEEvent{Event: currentEvent, Data: payload}
			if onEvent != nil {
				if err := onEvent(event); err != nil {
					return nil, err
				}
			}
			if currentEvent == "run.failed" {
				var failed struct {
					Error string `json:"error"`
				}
				_ = json.Unmarshal(payload, &failed)
				if strings.TrimSpace(failed.Error) == "" {
					failed.Error = "run failed"
				}
				return nil, errors.New(failed.Error)
			}
			if currentEvent == "message.completed" {
				var completed struct {
					Content   string `json:"content"`
					MessageID string `json:"message_id"`
				}
				if err := json.Unmarshal(payload, &completed); err != nil {
					return nil, err
				}
				result.Content = completed.Content
				result.ResponseMessage = Message{
					ID:      completed.MessageID,
					Content: completed.Content,
					Role:    "assistant",
				}
			}
			if currentEvent == "run.completed" {
				var completed struct {
					RunID     string `json:"run_id"`
					MessageID string `json:"message_id"`
				}
				if err := json.Unmarshal(payload, &completed); err != nil {
					return nil, err
				}
				result.Run = Run{
					ID:                completed.RunID,
					Status:            "completed",
					ResponseMessageID: stringPtr(completed.MessageID),
				}
				sawCompletion = true
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !sawCompletion {
		return nil, fmt.Errorf("stream ended before run.completed")
	}
	return &result, nil
}

func decodeError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	var payload struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err == nil && strings.TrimSpace(payload.Error) != "" {
		return fmt.Errorf("%s", payload.Error)
	}
	return fmt.Errorf("request failed: %s", resp.Status)
}

func stringPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}
