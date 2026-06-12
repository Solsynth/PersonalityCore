package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"src.solsynth.dev/sosys/personality/internal/database"
	"src.solsynth.dev/sosys/personality/internal/humanize"
	"src.solsynth.dev/sosys/personality/internal/solar"
)

type stubSolarBridge struct {
	roomID      string
	messageID   string
	messageIDs  []string
	lastAgent   string
	lastRoom    string
	lastTarget  string
	lastBody    string
	sendCount   int
	account     *solar.Account
	profile     solar.AccountProfile
	post        solar.Post
	posts       *solar.PaginatedPosts
	postReplies *solar.PaginatedPosts
}

func (s *stubSolarBridge) SendBotMessage(_ context.Context, agentID, roomID, targetAccountName, content string) (string, string, error) {
	s.lastAgent = agentID
	s.lastRoom = roomID
	s.lastTarget = targetAccountName
	s.lastBody = content
	s.sendCount++
	if len(s.messageIDs) >= s.sendCount {
		return s.roomID, s.messageIDs[s.sendCount-1], nil
	}
	return s.roomID, s.messageID, nil
}

func (s *stubSolarBridge) TrackRoom(agentID, roomID string) {
	s.lastAgent = agentID
	s.lastRoom = roomID
}

func (s *stubSolarBridge) GetAccount(_ context.Context, _, _, _ string) (*solar.Account, error) {
	return s.account, nil
}

func (s *stubSolarBridge) GetAccountProfile(_ context.Context, _, _ string) (solar.AccountProfile, error) {
	return s.profile, nil
}

func (s *stubSolarBridge) GetPost(_ context.Context, _, _ string) (solar.Post, error) {
	return s.post, nil
}

func (s *stubSolarBridge) ListPublisherPosts(_ context.Context, _, _ string, _, _ int) (*solar.PaginatedPosts, error) {
	return s.posts, nil
}

func (s *stubSolarBridge) ListPostReplies(_ context.Context, _, _ string, _, _ int) (*solar.PaginatedPosts, error) {
	return s.postReplies, nil
}

func TestExecuteChatToolCallRequiresDestination(t *testing.T) {
	svc := &ConversationService{solar: &stubSolarBridge{}}
	_, err := svc.executeChatToolCall(context.Background(), "support-bot", schema.ToolCall{
		Function: schema.FunctionCall{
			Name:      sendChatToolName,
			Arguments: `{"message":"hello"}`,
		},
	})
	if err == nil {
		t.Fatal("expected destination validation error")
	}
}

func TestExecuteChatToolCallSendsMessage(t *testing.T) {
	bridge := &stubSolarBridge{roomID: "room-1", messageID: "msg-1"}
	svc := &ConversationService{solar: bridge}

	result, err := svc.executeChatToolCall(context.Background(), "support-bot", schema.ToolCall{
		Function: schema.FunctionCall{
			Name:      sendChatToolName,
			Arguments: `{"room_id":"room-1","message":"hello from bot"}`,
		},
	})
	if err != nil {
		t.Fatalf("executeChatToolCall() error = %v", err)
	}
	if bridge.lastAgent != "support-bot" {
		t.Fatalf("expected agent support-bot, got %q", bridge.lastAgent)
	}
	if bridge.lastRoom != "room-1" {
		t.Fatalf("expected room room-1, got %q", bridge.lastRoom)
	}
	if bridge.lastBody != "hello from bot" {
		t.Fatalf("expected message body to be forwarded, got %q", bridge.lastBody)
	}
	if result == nil || result.Content == "" {
		t.Fatal("expected tool result payload")
	}
}

func TestExecuteChatBatchToolCallSendsMessages(t *testing.T) {
	bridge := &stubSolarBridge{
		roomID:     "room-1",
		messageIDs: []string{"msg-1", "msg-2"},
	}
	svc := &ConversationService{solar: bridge}

	result, err := svc.executeChatToolCall(context.Background(), "support-bot", schema.ToolCall{
		ID: "call-1",
		Function: schema.FunctionCall{
			Name:      sendChatBatchToolName,
			Arguments: `{"room_id":"room-1","messages":["hello","world"]}`,
		},
	})
	if err != nil {
		t.Fatalf("executeChatToolCall() error = %v", err)
	}
	if bridge.sendCount != 2 {
		t.Fatalf("expected 2 sends, got %d", bridge.sendCount)
	}
	if bridge.lastBody != "world" {
		t.Fatalf("expected final message body to be forwarded, got %q", bridge.lastBody)
	}
	if result == nil || result.ToolName != sendChatBatchToolName {
		t.Fatal("expected batch tool result payload")
	}
}

func TestExecuteChatToolCallNoReply(t *testing.T) {
	svc := &ConversationService{solar: &stubSolarBridge{}}
	result, err := svc.executeChatToolCall(context.Background(), "support-bot", schema.ToolCall{
		ID: "call-1",
		Function: schema.FunctionCall{
			Name:      noReplyToolName,
			Arguments: `{}`,
		},
	})
	if err != nil {
		t.Fatalf("executeChatToolCall() error = %v", err)
	}
	if result == nil || result.ToolName != noReplyToolName {
		t.Fatal("expected no_reply tool result payload")
	}
}

func TestExecuteChatToolCallGetUserProfile(t *testing.T) {
	svc := &ConversationService{solar: &stubSolarBridge{
		account: &solar.Account{ID: "user-1", Name: "alice", Nick: "Alice"},
		profile: solar.AccountProfile{"bio": "hello"},
	}}
	result, err := svc.executeChatToolCall(context.Background(), "support-bot", schema.ToolCall{
		ID: "call-1",
		Function: schema.FunctionCall{
			Name:      getUserProfileToolName,
			Arguments: `{"account_name":"alice"}`,
		},
	})
	if err != nil {
		t.Fatalf("executeChatToolCall() error = %v", err)
	}
	if result == nil || !strings.Contains(result.Content, `"bio":"hello"`) {
		t.Fatal("expected profile payload")
	}
}

func TestExecuteChatToolCallListUserPosts(t *testing.T) {
	svc := &ConversationService{solar: &stubSolarBridge{
		posts: &solar.PaginatedPosts{
			Total: 1,
			Items: []solar.Post{{"id": "post-1", "content": "hello"}},
		},
	}}
	result, err := svc.executeChatToolCall(context.Background(), "support-bot", schema.ToolCall{
		ID: "call-1",
		Function: schema.FunctionCall{
			Name:      listUserPostsToolName,
			Arguments: `{"account_name":"alice","take":3}`,
		},
	})
	if err != nil {
		t.Fatalf("executeChatToolCall() error = %v", err)
	}
	if result == nil || !strings.Contains(result.Content, `"post-1"`) {
		t.Fatal("expected posts payload")
	}
}

func TestExecuteChatToolCallSaveAndListSelfNotes(t *testing.T) {
	db := openTestDB(t)
	svc := &ConversationService{db: db, humanize: humanize.NewManager(db)}

	if _, err := svc.executeChatToolCall(context.Background(), "michan", schema.ToolCall{
		ID: "call-save",
		Function: schema.FunctionCall{
			Name:      saveSelfNoteToolName,
			Arguments: `{"key":"favorite_drink","category":"preference","content":"I like hojicha lattes."}`,
		},
	}); err != nil {
		t.Fatalf("save self note error = %v", err)
	}

	result, err := svc.executeChatToolCall(context.Background(), "michan", schema.ToolCall{
		ID: "call-list",
		Function: schema.FunctionCall{
			Name:      listSelfNotesToolName,
			Arguments: `{}`,
		},
	})
	if err != nil {
		t.Fatalf("list self notes error = %v", err)
	}
	var payload struct {
		Items []database.AgentSelfNote `json:"items"`
	}
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("decode result payload: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("expected 1 self note, got %d", len(payload.Items))
	}
	if payload.Items[0].Key != "favorite_drink" {
		t.Fatalf("unexpected self note key: %q", payload.Items[0].Key)
	}
}

func TestNormalizeSolarChatFinalResponseAllowsSilence(t *testing.T) {
	tests := []string{"", "   ", noChatReplyToken, strings.ToLower(noChatReplyToken)}
	for _, input := range tests {
		got, shouldFallbackSend := normalizeSolarChatFinalResponse(input)
		if got != "" {
			t.Fatalf("normalizeSolarChatFinalResponse(%q) = %q, want empty", input, got)
		}
		if shouldFallbackSend {
			t.Fatalf("normalizeSolarChatFinalResponse(%q) unexpectedly requested fallback send", input)
		}
	}
}

func TestNormalizeSolarChatFinalResponseFallsBackForPlainAssistantReply(t *testing.T) {
	got, shouldFallbackSend := normalizeSolarChatFinalResponse("I sent the message for you.")
	if got != "I sent the message for you." {
		t.Fatalf("expected original assistant reply, got %q", got)
	}
	if !shouldFallbackSend {
		t.Fatal("expected plain assistant reply to trigger fallback send")
	}
}

func TestNormalizeSolarChatFinalResponseSplitsAndTrimsLines(t *testing.T) {
	got, shouldFallbackSend := normalizeSolarChatFinalResponse(" first line \n\n second line \n")
	if got != "first line\nsecond line" {
		t.Fatalf("unexpected normalized output: %q", got)
	}
	if !shouldFallbackSend {
		t.Fatal("expected fallback send")
	}
}

func TestSolarOutboundStreamSenderSendsCompletedLines(t *testing.T) {
	db := openTestDB(t)
	bridge := &stubSolarBridge{
		roomID:     "room-1",
		messageIDs: []string{"msg-1", "msg-2"},
	}
	svc := &ConversationService{db: db, solar: bridge}
	thread := &database.ConversationThread{ID: "thread-1", AccountID: "solar:support:room-1", AgentID: "support-bot", Title: "Room one"}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	binding := &database.ExternalChatBinding{RemoteRoomID: "room-1", RemoteAccount: "alice"}
	sender := newSolarOutboundStreamSender(svc, thread, binding, "support-bot", "run-1")

	if err := sender.Push(context.Background(), "hello\nhow"); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	if bridge.sendCount != 1 {
		t.Fatalf("expected 1 send after first newline, got %d", bridge.sendCount)
	}
	if bridge.lastBody != "hello" {
		t.Fatalf("expected first line to send, got %q", bridge.lastBody)
	}

	if err := sender.Push(context.Background(), " are you?\n"); err != nil {
		t.Fatalf("Push() second error = %v", err)
	}
	if bridge.sendCount != 2 {
		t.Fatalf("expected 2 sends after second newline, got %d", bridge.sendCount)
	}
	if bridge.lastBody != "how are you?" {
		t.Fatalf("expected buffered second line to send, got %q", bridge.lastBody)
	}
}
