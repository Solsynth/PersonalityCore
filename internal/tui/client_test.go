package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientRunStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/conversations/thread-1/runs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: run.started\n")
		fmt.Fprint(w, "data: {\"conversation_id\":\"thread-1\"}\n\n")
		fmt.Fprint(w, "event: message.delta\n")
		fmt.Fprint(w, "data: {\"delta\":\"Hel\"}\n\n")
		fmt.Fprint(w, "event: message.delta\n")
		fmt.Fprint(w, "data: {\"delta\":\"lo\"}\n\n")
		fmt.Fprint(w, "event: message.completed\n")
		fmt.Fprint(w, "data: {\"content\":\"Hello\",\"message_id\":\"msg-1\"}\n\n")
		fmt.Fprint(w, "event: run.completed\n")
		fmt.Fprint(w, "data: {\"run_id\":\"run-1\",\"message_id\":\"msg-1\"}\n\n")
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	var deltas []string
	result, err := client.Run(context.Background(), "thread-1", "hello", true, func(event SSEEvent) error {
		if event.Event == "message.delta" {
			var payload struct {
				Delta string `json:"delta"`
			}
			if err := jsonUnmarshal(event.Data, &payload); err != nil {
				return err
			}
			deltas = append(deltas, payload.Delta)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got, want := len(deltas), 2; got != want {
		t.Fatalf("delta count = %d, want %d", got, want)
	}
	if result.Content != "Hello" {
		t.Fatalf("content = %q, want %q", result.Content, "Hello")
	}
	if result.Run.ID != "run-1" {
		t.Fatalf("run id = %q, want %q", result.Run.ID, "run-1")
	}
}

func TestClientListMessagesPreservesQueryString(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/conversations/thread-1/messages" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("take"); got != "200" {
			t.Fatalf("take = %q", got)
		}
		if got := r.URL.Query().Get("offset"); got != "0" {
			t.Fatalf("offset = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "[]")
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	messages, err := client.ListMessages(context.Background(), "thread-1")
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("messages len = %d, want 0", len(messages))
	}
}
