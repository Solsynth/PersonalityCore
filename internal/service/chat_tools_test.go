package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
	"src.solsynth.dev/sosys/personality/internal/humanize"
	"src.solsynth.dev/sosys/personality/internal/solar_network"
)

type stubSolarBridge struct {
	roomID       string
	messageID    string
	messageIDs   []string
	lastAgent    string
	lastRoom     string
	lastTarget   string
	lastTargetID string
	lastBody     string
	sendCount    int
	account      *solar_network.Account
	profile      solar_network.AccountProfile
	post         solar_network.Post
	posts        *solar_network.PaginatedPosts
	postReplies  *solar_network.PaginatedPosts
}

func (s *stubSolarBridge) SendBotMessage(_ context.Context, agentID, roomID, targetAccountName, targetAccountID, content string) (string, string, error) {
	s.lastAgent = agentID
	s.lastRoom = roomID
	s.lastTarget = targetAccountName
	s.lastTargetID = targetAccountID
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

func (s *stubSolarBridge) GetAccount(_ context.Context, _, _, _ string) (*solar_network.Account, error) {
	return s.account, nil
}

func (s *stubSolarBridge) GetAccountProfile(_ context.Context, _, _ string) (solar_network.AccountProfile, error) {
	return s.profile, nil
}

func (s *stubSolarBridge) GetMessage(_ context.Context, _, _, _ string) (*solar_network.ChatMessage, error) {
	return &solar_network.ChatMessage{ID: "stub-msg", Content: "stub content"}, nil
}

func (s *stubSolarBridge) GetPost(_ context.Context, _, _ string) (solar_network.Post, error) {
	return s.post, nil
}

func (s *stubSolarBridge) ListPublisherPosts(_ context.Context, _, _ string, _, _ int) (*solar_network.PaginatedPosts, error) {
	return s.posts, nil
}

func (s *stubSolarBridge) ListPostReplies(_ context.Context, _, _ string, _, _ int) (*solar_network.PaginatedPosts, error) {
	return s.postReplies, nil
}

func (s *stubSolarBridge) ListFeed(_ context.Context, _ string, _, _ int, _ bool) (*solar_network.PaginatedPosts, error) {
	return s.posts, nil
}

func (s *stubSolarBridge) SearchPosts(_ context.Context, _, _ string, _, _ int) (*solar_network.PaginatedPosts, error) {
	return s.posts, nil
}

func (s *stubSolarBridge) CreatePost(_ context.Context, _, _ string, _ map[string]any) (solar_network.Post, error) {
	return s.post, nil
}

func (s *stubSolarBridge) ReplyToPost(_ context.Context, _, _, _, _ string) (solar_network.Post, error) {
	return s.post, nil
}

func (s *stubSolarBridge) RepostPost(_ context.Context, _, _, _ string, _ *string) (solar_network.Post, error) {
	return s.post, nil
}

func (s *stubSolarBridge) ReactToPost(_ context.Context, _, _, _ string, _ int) error {
	return nil
}

func TestExecuteChatToolCallRequiresDestination(t *testing.T) {
	svc := &ConversationService{sn: &stubSolarBridge{}}
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
	svc := &ConversationService{sn: bridge}

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
	svc := &ConversationService{sn: bridge}

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
	svc := &ConversationService{sn: &stubSolarBridge{}}
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
	svc := &ConversationService{sn: &stubSolarBridge{
		account: &solar_network.Account{ID: "user-1", Name: "alice", Nick: "Alice"},
		profile: solar_network.AccountProfile{"bio": "hello"},
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
	svc := &ConversationService{sn: &stubSolarBridge{
		posts: &solar_network.PaginatedPosts{
			Total: 1,
			Items: []solar_network.Post{{"id": "post-1", "content": "hello"}},
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

func TestDeliverFallbackChatResponseUsesAutonomousTargetWhenBindingMissing(t *testing.T) {
	db := openTestDB(t)
	bridge := &stubSolarBridge{roomID: "room-dm-1", messageID: "msg-1"}
	svc := &ConversationService{db: db, sn: bridge}

	thread := &database.ConversationThread{
		ID:        "thread-auto-dm-1",
		AccountID: "solar:michan:dm:alice",
		AgentID:   "michan",
		Title:     "alice",
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if _, err := svc.createMessageWithMetadata(context.Background(), thread, nil, "system", "Autonomous wake-up triggered.", nil, map[string]any{
		"source":              "autonomous",
		"target_account_id":   "acct-alice-1",
		"target_account_name": "alice",
	}); err != nil {
		t.Fatalf("create autonomous system message: %v", err)
	}

	if err := svc.deliverFallbackChatResponse(context.Background(), thread, "michan", "run-1", nil, "hello there"); err != nil {
		t.Fatalf("deliverFallbackChatResponse() error = %v", err)
	}
	if bridge.sendCount != 1 {
		t.Fatalf("expected one fallback send, got %d", bridge.sendCount)
	}
	if bridge.lastTarget != "alice" {
		t.Fatalf("expected fallback target alice, got %q", bridge.lastTarget)
	}
	if bridge.lastTargetID != "acct-alice-1" {
		t.Fatalf("expected fallback target id acct-alice-1, got %q", bridge.lastTargetID)
	}
	if bridge.lastBody != "hello there" {
		t.Fatalf("expected fallback body hello there, got %q", bridge.lastBody)
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
	svc := &ConversationService{db: db, sn: bridge}
	thread := &database.ConversationThread{ID: "thread-1", AccountID: "solar:support:room-1", AgentID: "support-bot", Title: "Room one"}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	binding := &database.ExternalChatBinding{RemoteRoomID: "room-1", RemoteAccount: "alice"}
	sender := newSnOutboundStreamSender(svc, thread, binding, "support-bot", "run-1")

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

func currentUserProfileCall() schema.ToolCall {
	return schema.ToolCall{
		ID: "call-1",
		Function: schema.FunctionCall{
			Name:      getCurrentUserProfileToolName,
			Arguments: `{}`,
		},
	}
}

func newTestConversationService(t *testing.T) *ConversationService {
	t.Helper()
	registry, err := agent.NewRegistry(nil)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	return NewConversationService(openTestDB(t), &config.Config{}, registry, nil)
}

func TestGetCurrentUserProfileToolFallsBackToCallerIdentity(t *testing.T) {
	svc := newTestConversationService(t)
	ctx := WithCallerIdentity(context.Background(), CallerIdentity{AccountID: "acct-9", Name: "alice", Nick: "Alice"})
	thread := &database.ConversationThread{ID: "thread-1", AccountID: "acct-9", AgentID: "mochi"}

	result, err := svc.executeGetCurrentUserProfileToolCall(ctx, "mochi", thread, currentUserProfileCall())
	if err != nil {
		t.Fatalf("executeGetCurrentUserProfileToolCall() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	account, ok := payload["account"].(map[string]any)
	if !ok {
		t.Fatalf("expected account object, got %#v", payload["account"])
	}
	if account["id"] != "acct-9" || account["name"] != "alice" || account["nick"] != "Alice" {
		t.Fatalf("unexpected account: %#v", account)
	}
	if payload["profile_unavailable"] != true {
		t.Fatalf("expected profile_unavailable, got %#v", payload)
	}
}

func TestGetCurrentUserProfileToolResolvesViaBridge(t *testing.T) {
	svc := newTestConversationService(t)
	svc.SetSnChatBridge(&stubSolarBridge{
		account: &solar_network.Account{ID: "acct-9", Name: "alice", Nick: "Alice"},
		profile: solar_network.AccountProfile{"time_zone": "Asia/Shanghai", "region": "CN"},
	})
	thread := &database.ConversationThread{ID: "thread-1", AccountID: "acct-9", AgentID: "mochi"}

	result, err := svc.executeGetCurrentUserProfileToolCall(context.Background(), "mochi", thread, currentUserProfileCall())
	if err != nil {
		t.Fatalf("executeGetCurrentUserProfileToolCall() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if _, ok := payload["profile"].(map[string]any); !ok {
		t.Fatalf("expected profile object, got %#v", payload["profile"])
	}
	if payload["profile_unavailable"] != nil {
		t.Fatalf("expected full profile without profile_unavailable, got %#v", payload)
	}
	if !strings.Contains(result.Content, "Current time for the sender") {
		t.Fatalf("expected local time in result, got %s", result.Content)
	}
}

func TestGetCurrentUserProfileToolNoUser(t *testing.T) {
	svc := newTestConversationService(t)
	thread := &database.ConversationThread{ID: "thread-1", AccountID: "solar:agent:room", AgentID: "bot"}

	result, err := svc.executeGetCurrentUserProfileToolCall(context.Background(), "bot", thread, currentUserProfileCall())
	if err != nil {
		t.Fatalf("executeGetCurrentUserProfileToolCall() error = %v", err)
	}
	if !strings.Contains(result.Content, `"ok":false`) {
		t.Fatalf("expected soft failure result, got %s", result.Content)
	}
}

func TestBuildToolInfosStaticModeLoadsAllAbilitySkills(t *testing.T) {
	// Static mode (dynamicSkills = false) should load all ability-gated skills eagerly
	// and omit list_skills / activate_skill meta tools.
	cfg := &config.Config{
		Personality: config.PersonalityConfig{
			DynamicSkills: false,
		},
	}
	svc := &ConversationService{cfg: cfg}

	// Chat agent gets: chat skill + solar_network skill + sequential_thinking + get_current_user_profile + end_engagement
	def := agent.Definition{
		ID:        "test-bot",
		Model:     "openai/test",
		Abilities: []string{"chat"},
	}

	tools := svc.buildToolInfos(def, nil, 0)
	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}

	// list_skills / activate_skill must NOT be present
	if toolNames["list_skills"] {
		t.Fatal("static mode should not include list_skills")
	}
	if toolNames["activate_skill"] {
		t.Fatal("static mode should not include activate_skill")
	}

	// chat skill tools must be present
	if !toolNames["send_chat_message"] {
		t.Fatal("static mode should eagerly load chat skill tools (send_chat_message)")
	}
	if !toolNames["send_chat_message_batch"] {
		t.Fatal("static mode should eagerly load chat skill tools (send_chat_message_batch)")
	}
	if !toolNames["no_reply"] {
		t.Fatal("static mode should eagerly load chat skill tools (no_reply)")
	}

	// solar_network skill tools must be present (gated on chat ability)
	if !toolNames["list_user_posts"] {
		t.Fatal("static mode should eagerly load solar_network skill tools (list_user_posts)")
	}
	if !toolNames["get_post"] {
		t.Fatal("static mode should eagerly load solar_network skill tools (get_post)")
	}
	if !toolNames["list_post_replies"] {
		t.Fatal("static mode should eagerly load solar_network skill tools (list_post_replies)")
	}
	if !toolNames["get_chat_message"] {
		t.Fatal("static mode should eagerly load solar_network skill tools (get_chat_message)")
	}
	if !toolNames["get_user_profile"] {
		t.Fatal("static mode should eagerly load solar_network skill tools (get_user_profile)")
	}

	// end_engagement (standalone tool for chat agents) must be present
	if !toolNames["end_engagement"] {
		t.Fatal("static mode should load end_engagement for chat agents")
	}

	// get_current_user_profile must be present
	if !toolNames["get_current_user_profile"] {
		t.Fatal("static mode should load get_current_user_profile")
	}
}

func TestBuildToolInfosStaticModeOmitsUnrelatedAbilitySkills(t *testing.T) {
	// A non-chat agent should NOT get solar_network or chat skill tools.
	cfg := &config.Config{
		Personality: config.PersonalityConfig{
			DynamicSkills: false,
		},
	}
	svc := &ConversationService{cfg: cfg}

	def := agent.Definition{
		ID:        "general",
		Model:     "openai/test",
		Abilities: []string{},
	}

	tools := svc.buildToolInfos(def, nil, 0)
	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}

	if toolNames["send_chat_message"] {
		t.Fatal("non-chat agent should not get chat skill tools")
	}
	if toolNames["list_user_posts"] {
		t.Fatal("non-chat agent should not get solar_network skill tools")
	}
	if toolNames["list_skills"] {
		t.Fatal("static mode should not include list_skills")
	}
}

func TestBuildToolInfosDynamicModeRetainsMetaTools(t *testing.T) {
	// Dynamic mode (default) should include list_skills / activate_skill.
	cfg := &config.Config{
		Personality: config.PersonalityConfig{
			DynamicSkills: true,
		},
	}
	svc := &ConversationService{cfg: cfg}

	def := agent.Definition{
		ID:        "test-bot",
		Model:     "openai/test",
		Abilities: []string{"chat"},
	}

	tools := svc.buildToolInfos(def, nil, 0)
	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}

	if !toolNames["list_skills"] {
		t.Fatal("dynamic mode should include list_skills")
	}
	if !toolNames["activate_skill"] {
		t.Fatal("dynamic mode should include activate_skill")
	}
	// solar_network NOT eagerly loaded in dynamic mode (requires activate_skill)
	if toolNames["list_user_posts"] {
		t.Fatal("dynamic mode should not eagerly load solar_network skill tools")
	}
}

func TestToolsForConversationFiltersSolarOutboundTools(t *testing.T) {
	svc := &ConversationService{cfg: &config.Config{
		Personality: config.PersonalityConfig{DynamicSkills: true},
	}}
	def := agent.Definition{ID: "michan", Abilities: []string{"chat", "pet"}}

	ordinary := svc.ToolsForConversation(def, 0, false)
	ordinaryNames := make(map[string]bool, len(ordinary))
	for _, tool := range ordinary {
		ordinaryNames[tool.Name] = true
	}
	for _, name := range []string{"send_chat_message", "send_chat_message_batch", "no_reply", "end_engagement"} {
		if ordinaryNames[name] {
			t.Fatalf("ordinary conversation exposed Solar outbound tool %q", name)
		}
	}
	if !ordinaryNames["pet_adjust_affection"] {
		t.Fatal("ordinary conversation lost non-Solar agent tool")
	}

	solar := svc.ToolsForConversation(def, 0, true)
	solarNames := make(map[string]bool, len(solar))
	for _, tool := range solar {
		solarNames[tool.Name] = true
	}
	for _, name := range []string{"send_chat_message", "send_chat_message_batch", "no_reply", "end_engagement"} {
		if !solarNames[name] {
			t.Fatalf("Solar conversation omitted outbound tool %q", name)
		}
	}
}
