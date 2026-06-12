package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/database"
	"src.solsynth.dev/sosys/personality/internal/humanize"
	"src.solsynth.dev/sosys/personality/internal/logging"
)

const sendChatToolName = "send_chat_message"
const sendChatBatchToolName = "send_chat_message_batch"
const noReplyToolName = "no_reply"
const getUserProfileToolName = "get_user_profile"
const listUserPostsToolName = "list_user_posts"
const getPostToolName = "get_post"
const listPostRepliesToolName = "list_post_replies"
const listSelfNotesToolName = "list_self_notes"
const saveSelfNoteToolName = "save_self_note"
const deleteSelfNoteToolName = "delete_self_note"
const solarOutboundMessageMinGap = 650 * time.Millisecond

type sendChatToolInput struct {
	RoomID            string `json:"room_id"`
	TargetAccountName string `json:"target_account_name"`
	Message           string `json:"message"`
}

type sendChatBatchToolInput struct {
	RoomID            string   `json:"room_id"`
	TargetAccountName string   `json:"target_account_name"`
	Messages          []string `json:"messages"`
}

type getUserProfileToolInput struct {
	AccountName string `json:"account_name"`
	AccountID   string `json:"account_id"`
}

type listUserPostsToolInput struct {
	AccountName string `json:"account_name"`
	Offset      int    `json:"offset"`
	Take        int    `json:"take"`
}

type getPostToolInput struct {
	PostID string `json:"post_id"`
}

type listPostRepliesToolInput struct {
	PostID string `json:"post_id"`
	Offset int    `json:"offset"`
	Take   int    `json:"take"`
}

type listSelfNotesToolInput struct {
	Category string `json:"category"`
}

type saveSelfNoteToolInput struct {
	Key      string `json:"key"`
	Category string `json:"category"`
	Content  string `json:"content"`
}

type deleteSelfNoteToolInput struct {
	Key string `json:"key"`
}

type executedChatToolResult struct {
	Content           string
	RoomID            string
	TargetAccountName string
	ToolName          string
	ToolCallID        string
}

const noChatReplyToken = "NO_REPLY"

func (s *ConversationService) runWithChatTools(
	ctx context.Context,
	accountID, threadID string,
	runID string,
	modelMessages []*schema.Message,
	agentDef agent.Definition,
) (string, error) {
	toolModel, err := s.executor.NewToolCallingModel(ctx, agentDef, []*schema.ToolInfo{
		s.sendChatToolInfo(),
		s.sendChatBatchToolInfo(),
		s.noReplyToolInfo(),
		s.getUserProfileToolInfo(),
		s.listUserPostsToolInfo(),
		s.getPostToolInfo(),
		s.listPostRepliesToolInfo(),
		s.listSelfNotesToolInfo(),
		s.saveSelfNoteToolInfo(),
		s.deleteSelfNoteToolInfo(),
	})
	if err != nil {
		return "", err
	}

	thread, err := s.GetConversation(ctx, accountID, threadID)
	if err != nil {
		return "", err
	}
	binding, err := s.getSolarRoomBinding(ctx, agentDef.ID, threadID)
	if err != nil {
		return "", err
	}
	allowOutboundReply, err := s.allowSolarRoomReply(ctx, thread, binding)
	if err != nil {
		return "", err
	}

	messages := append([]*schema.Message(nil), modelMessages...)
	for step := 0; step < 6; step++ {
		logging.Log.Debug().
			Str("agent_id", agentDef.ID).
			Str("conversation_id", threadID).
			Str("run_id", runID).
			Int("tool_loop_step", step+1).
			Int("message_count", len(messages)).
			Msg("invoking chat tool-capable model")
		response, err := toolModel.Generate(ctx, messages)
		if err != nil {
			return "", err
		}
		if len(response.ToolCalls) == 0 {
			finalContent := strings.TrimSpace(response.Content)
			if binding != nil {
				if !allowOutboundReply {
					logging.Log.Info().
						Str("agent_id", agentDef.ID).
						Str("conversation_id", threadID).
						Str("run_id", runID).
						Msg("suppressing plain-text group chat reply because latest inbound message did not mention or reply to the bot")
					finalContent = ""
				}
				normalized, shouldFallbackSend := normalizeSolarChatFinalResponse(finalContent)
				if shouldFallbackSend {
					if err := s.deliverFallbackChatResponse(ctx, thread, agentDef.ID, runID, binding, normalized); err != nil {
						return "", err
					}
				}
				finalContent = normalized
			}
			logging.Log.Info().
				Str("agent_id", agentDef.ID).
				Str("conversation_id", threadID).
				Str("run_id", runID).
				Int("tool_loop_step", step+1).
				Int("response_chars", len(finalContent)).
				Str("response_content", finalContent).
				Msg("chat model returned final response without tool calls")
			return finalContent, nil
		}

		logging.Log.Info().
			Str("agent_id", agentDef.ID).
			Str("conversation_id", threadID).
			Str("run_id", runID).
			Int("tool_loop_step", step+1).
			Int("tool_call_count", len(response.ToolCalls)).
			Msg("chat model requested tool calls")

		assistantMetadata := map[string]any{
			"tool_calls": response.ToolCalls,
		}
		if strings.TrimSpace(response.ReasoningContent) != "" {
			assistantMetadata["reasoning_content"] = strings.TrimSpace(response.ReasoningContent)
		}
		if _, err := s.createMessageWithMetadata(ctx, thread, &runID, "assistant", strings.TrimSpace(response.Content), stringPtr(agentDef.Model), assistantMetadata); err != nil {
			return "", err
		}

		messages = append(messages, response)
		for _, call := range response.ToolCalls {
			logging.Log.Debug().
				Str("agent_id", agentDef.ID).
				Str("conversation_id", threadID).
				Str("run_id", runID).
				Str("tool_name", call.Function.Name).
				Str("tool_call_id", call.ID).
				Str("tool_arguments", call.Function.Arguments).
				Msg("executing chat tool call")
			result := &executedChatToolResult{}
			if binding != nil && !allowOutboundReply && isSolarOutboundChatToolName(call.Function.Name) {
				logging.Log.Info().
					Str("agent_id", agentDef.ID).
					Str("conversation_id", threadID).
					Str("run_id", runID).
					Str("tool_name", call.Function.Name).
					Str("tool_call_id", call.ID).
					Msg("suppressing outbound group chat tool call because latest inbound message did not mention or reply to the bot")
				result, err = suppressedChatToolResult(call)
			} else {
				result, err = s.executeChatToolCall(ctx, agentDef.ID, call)
				if err != nil {
					return "", err
				}
			}
			if err := s.ensureSolarRoomBinding(ctx, thread, agentDef.ID, result.RoomID, result.TargetAccountName, time.Now()); err != nil {
				return "", err
			}
			if _, err := s.createMessageWithMetadata(ctx, thread, &runID, "tool", result.Content, nil, map[string]any{
				"tool_call_id": result.ToolCallID,
				"tool_name":    result.ToolName,
			}); err != nil {
				return "", err
			}
			logging.Log.Debug().
				Str("agent_id", agentDef.ID).
				Str("conversation_id", threadID).
				Str("run_id", runID).
				Str("tool_name", call.Function.Name).
				Str("tool_call_id", call.ID).
				Str("tool_result", result.Content).
				Msg("chat tool call completed")
			messages = append(messages, schema.ToolMessage(result.Content, call.ID, schema.WithToolName(call.Function.Name)))
		}
		if binding != nil {
			return "", nil
		}
	}

	return "", fmt.Errorf("chat tool loop exceeded maximum iterations")
}

func isSolarOutboundChatToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case sendChatToolName, sendChatBatchToolName:
		return true
	default:
		return false
	}
}

func suppressedChatToolResult(call schema.ToolCall) (*executedChatToolResult, error) {
	payload, err := json.Marshal(map[string]any{
		"ok":     true,
		"status": "reply_suppressed",
		"reason": "group_message_without_mention_or_reply",
	})
	if err != nil {
		return nil, err
	}
	return &executedChatToolResult{
		Content:    string(payload),
		ToolName:   call.Function.Name,
		ToolCallID: call.ID,
	}, nil
}

func normalizeSolarChatFinalResponse(content string) (normalized string, shouldFallbackSend bool) {
	switch trimmed := strings.TrimSpace(content); {
	case trimmed == "":
		return "", false
	case strings.EqualFold(trimmed, noChatReplyToken):
		return "", false
	default:
		return strings.Join(splitSolarOutboundMessages(trimmed), "\n"), true
	}
}

func sanitizeSolarOutboundMessage(content string) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	sanitized := make([]string, 0, len(lines))
	skippedTimestampHeader := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if !skippedTimestampHeader || len(sanitized) == 0 {
				continue
			}
			sanitized = append(sanitized, "")
			continue
		}
		if strings.HasPrefix(trimmed, "Sent at: ") {
			skippedTimestampHeader = true
			continue
		}
		sanitized = append(sanitized, line)
	}
	return strings.TrimSpace(strings.Join(sanitized, "\n"))
}

func (s *ConversationService) deliverFallbackChatResponse(
	ctx context.Context,
	thread *database.ConversationThread,
	agentID, runID string,
	binding *database.ExternalChatBinding,
	content string,
) error {
	if thread == nil || binding == nil || strings.TrimSpace(content) == "" || s.solar == nil {
		return nil
	}
	logging.Log.Warn().
		Str("agent_id", agentID).
		Str("conversation_id", thread.ID).
		Str("run_id", runID).
		Str("room_id", binding.RemoteRoomID).
		Int("response_chars", len(content)).
		Msg("chat model skipped tool call; forwarding assistant text via fallback solar send")

	messages := splitSolarOutboundMessages(content)
	if len(messages) == 0 {
		return nil
	}
	roomID := binding.RemoteRoomID
	messageID := ""
	lastSentAt := time.Time{}
	for i, message := range messages {
		if i > 0 {
			if err := waitForSolarOutboundGap(ctx, lastSentAt); err != nil {
				return err
			}
		}
		sentRoomID, sentMessageID, err := s.solar.SendBotMessage(ctx, agentID, roomID, "", message)
		if err != nil {
			return err
		}
		roomID = sentRoomID
		messageID = sentMessageID
		lastSentAt = time.Now()
		logging.Log.Debug().
			Str("agent_id", agentID).
			Str("conversation_id", thread.ID).
			Str("run_id", runID).
			Str("room_id", roomID).
			Str("message_id", messageID).
			Int("batch_index", i).
			Msg("sent fallback solar message chunk")
	}
	if err := s.ensureSolarRoomBinding(ctx, thread, agentID, roomID, binding.RemoteAccount, time.Now()); err != nil {
		return err
	}
	logging.Log.Info().
		Str("agent_id", agentID).
		Str("conversation_id", thread.ID).
		Str("run_id", runID).
		Str("room_id", roomID).
		Str("message_id", messageID).
		Msg("fallback solar send succeeded")
	return nil
}

func splitSolarOutboundMessages(content string) []string {
	if strings.EqualFold(strings.TrimSpace(content), noChatReplyToken) {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	messages := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(sanitizeSolarOutboundMessage(line)); trimmed != "" {
			messages = append(messages, trimmed)
		}
	}
	return messages
}

type solarOutboundStreamSender struct {
	service        *ConversationService
	thread         *database.ConversationThread
	binding        *database.ExternalChatBinding
	agentID        string
	runID          string
	buffer         strings.Builder
	lastRoomID     string
	lastSentAt     time.Time
	sentMessageIDs []string
}

func newSolarOutboundStreamSender(service *ConversationService, thread *database.ConversationThread, binding *database.ExternalChatBinding, agentID, runID string) *solarOutboundStreamSender {
	lastRoomID := ""
	if binding != nil {
		lastRoomID = binding.RemoteRoomID
	}
	return &solarOutboundStreamSender{
		service:    service,
		thread:     thread,
		binding:    binding,
		agentID:    agentID,
		runID:      runID,
		lastRoomID: lastRoomID,
	}
}

func (s *solarOutboundStreamSender) Push(ctx context.Context, chunk string) error {
	if s == nil || s.service == nil || s.binding == nil || s.service.solar == nil || chunk == "" {
		return nil
	}
	s.buffer.WriteString(strings.ReplaceAll(chunk, "\r\n", "\n"))
	for {
		current := s.buffer.String()
		idx := strings.IndexByte(current, '\n')
		if idx < 0 {
			return nil
		}
		line := strings.TrimSpace(current[:idx])
		rest := current[idx+1:]
		s.buffer.Reset()
		s.buffer.WriteString(rest)
		if line == "" || strings.EqualFold(line, noChatReplyToken) {
			continue
		}
		if err := s.sendMessage(ctx, line); err != nil {
			return err
		}
	}
}

func (s *solarOutboundStreamSender) Flush(ctx context.Context) error {
	if s == nil || s.binding == nil || s.service == nil || s.service.solar == nil {
		return nil
	}
	line := strings.TrimSpace(s.buffer.String())
	s.buffer.Reset()
	if line == "" || strings.EqualFold(line, noChatReplyToken) {
		return nil
	}
	return s.sendMessage(ctx, line)
}

func (s *solarOutboundStreamSender) sendMessage(ctx context.Context, message string) error {
	message = sanitizeSolarOutboundMessage(message)
	if message == "" {
		return nil
	}
	if err := waitForSolarOutboundGap(ctx, s.lastSentAt); err != nil {
		return err
	}
	roomID, messageID, err := s.service.solar.SendBotMessage(ctx, s.agentID, s.lastRoomID, "", message)
	if err != nil {
		return err
	}
	s.lastRoomID = roomID
	s.lastSentAt = time.Now()
	s.sentMessageIDs = append(s.sentMessageIDs, messageID)
	if err := s.service.ensureSolarRoomBinding(ctx, s.thread, s.agentID, roomID, s.binding.RemoteAccount, time.Now()); err != nil {
		return err
	}
	logging.Log.Info().
		Str("agent_id", s.agentID).
		Str("conversation_id", s.thread.ID).
		Str("run_id", s.runID).
		Str("room_id", roomID).
		Str("message_id", messageID).
		Msg("sent streamed solar chat line")
	return nil
}

func (s *ConversationService) sendChatToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: sendChatToolName,
		Desc: "Send one Solar Network chat message through this agent's configured bot identity. Use room_id when replying in an existing room. Use target_account_name when you need to open or reuse a direct message with a named account. Always call this tool instead of writing an unsent reply in assistant text.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"room_id": {
				Type: schema.String,
				Desc: "Existing Solar chat room ID to send into. Prefer this when replying in the current conversation.",
			},
			"target_account_name": {
				Type: schema.String,
				Desc: "Solar account name to direct-message when room_id is not available.",
			},
			"message": {
				Type:     schema.String,
				Desc:     "Exact chat message content to send.",
				Required: true,
			},
		}),
	}
}

func (s *ConversationService) sendChatBatchToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: sendChatBatchToolName,
		Desc: "Send multiple Solar Network chat messages in order. Use this when you intentionally want to split a reply into multiple separate chat messages. Always use this tool instead of writing unsent outbound messages in assistant text.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"room_id": {
				Type: schema.String,
				Desc: "Existing Solar chat room ID to send into. Prefer this when replying in the current conversation.",
			},
			"target_account_name": {
				Type: schema.String,
				Desc: "Solar account name to direct-message when room_id is not available.",
			},
			"messages": {
				Type: schema.Array,
				ElemInfo: &schema.ParameterInfo{
					Type: schema.String,
					Desc: "One chat message to send.",
				},
				Desc:     "Ordered list of chat messages to send as separate messages.",
				Required: true,
			},
		}),
	}
}

func (s *ConversationService) noReplyToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name:        noReplyToolName,
		Desc:        "Choose this when you intentionally decide not to send any Solar chat reply for the current inbound message.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func (s *ConversationService) getUserProfileToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: getUserProfileToolName,
		Desc: "Fetch a Solar Network user's public account and profile information by account_name or account_id.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"account_name": {
				Type: schema.String,
				Desc: "Solar account name to look up.",
			},
			"account_id": {
				Type: schema.String,
				Desc: "Solar account ID to look up.",
			},
		}),
	}
}

func (s *ConversationService) listUserPostsToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: listUserPostsToolName,
		Desc: "List recent public posts published by a Solar account.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"account_name": {
				Type:     schema.String,
				Desc:     "Solar account name whose posts should be listed.",
				Required: true,
			},
			"offset": {
				Type: schema.Integer,
				Desc: "Pagination offset. Defaults to 0.",
			},
			"take": {
				Type: schema.Integer,
				Desc: "Number of posts to fetch. Defaults to 5.",
			},
		}),
	}
}

func (s *ConversationService) getPostToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: getPostToolName,
		Desc: "Fetch one Solar Network post by post_id.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"post_id": {
				Type:     schema.String,
				Desc:     "Solar post ID to fetch.",
				Required: true,
			},
		}),
	}
}

func (s *ConversationService) listPostRepliesToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: listPostRepliesToolName,
		Desc: "List replies for a Solar Network post.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"post_id": {
				Type:     schema.String,
				Desc:     "Solar post ID whose replies should be listed.",
				Required: true,
			},
			"offset": {
				Type: schema.Integer,
				Desc: "Pagination offset. Defaults to 0.",
			},
			"take": {
				Type: schema.Integer,
				Desc: "Number of replies to fetch. Defaults to 5.",
			},
		}),
	}
}

func (s *ConversationService) listSelfNotesToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: listSelfNotesToolName,
		Desc: "List your persistent self notes shared across all conversations for this same agent. Use this before answering questions about your own likes, background, ongoing projects, routines, or other stable self-identity details when consistency matters.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"category": {
				Type: schema.String,
				Desc: "Optional category filter such as identity, preference, project, lore, or routine.",
			},
		}),
	}
}

func (s *ConversationService) saveSelfNoteToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: saveSelfNoteToolName,
		Desc: "Create or update one persistent self note for this agent. Use this when you decide on a stable personal detail about yourself that should stay consistent across future conversations. Prefer concise durable keys like favorite_drink, current_project, speaking_style, or hometown_story.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"key": {
				Type:     schema.String,
				Desc:     "Stable identifier for this self note.",
				Required: true,
			},
			"category": {
				Type: schema.String,
				Desc: "Optional bucket such as identity, preference, project, lore, or routine.",
			},
			"content": {
				Type:     schema.String,
				Desc:     "The exact self note content to persist.",
				Required: true,
			},
		}),
	}
}

func (s *ConversationService) deleteSelfNoteToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: deleteSelfNoteToolName,
		Desc: "Delete one persistent self note for this agent by key. Use this when a prior self note should no longer be treated as part of your identity or current state.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"key": {
				Type:     schema.String,
				Desc:     "Stable identifier for the self note to remove.",
				Required: true,
			},
		}),
	}
}

func (s *ConversationService) executeChatToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	switch call.Function.Name {
	case sendChatToolName, sendChatBatchToolName, noReplyToolName, getUserProfileToolName, listUserPostsToolName, getPostToolName, listPostRepliesToolName, listSelfNotesToolName, saveSelfNoteToolName, deleteSelfNoteToolName:
	default:
		return nil, fmt.Errorf("unsupported tool %q", call.Function.Name)
	}
	switch call.Function.Name {
	case listSelfNotesToolName:
		return s.executeListSelfNotesToolCall(ctx, agentID, call)
	case saveSelfNoteToolName:
		return s.executeSaveSelfNoteToolCall(ctx, agentID, call)
	case deleteSelfNoteToolName:
		return s.executeDeleteSelfNoteToolCall(ctx, agentID, call)
	}
	if s.solar == nil {
		return nil, fmt.Errorf("solar chat bridge is not configured")
	}
	if call.Function.Name == noReplyToolName {
		payload, err := json.Marshal(map[string]any{
			"ok":     true,
			"status": "no_reply",
		})
		if err != nil {
			return nil, err
		}
		return &executedChatToolResult{
			Content:    string(payload),
			ToolName:   call.Function.Name,
			ToolCallID: call.ID,
		}, nil
	}

	if call.Function.Name == sendChatBatchToolName {
		return s.executeChatBatchToolCall(ctx, agentID, call)
	}
	if call.Function.Name == getUserProfileToolName {
		return s.executeGetUserProfileToolCall(ctx, agentID, call)
	}
	if call.Function.Name == listUserPostsToolName {
		return s.executeListUserPostsToolCall(ctx, agentID, call)
	}
	if call.Function.Name == getPostToolName {
		return s.executeGetPostToolCall(ctx, agentID, call)
	}
	if call.Function.Name == listPostRepliesToolName {
		return s.executeListPostRepliesToolCall(ctx, agentID, call)
	}

	var input sendChatToolInput
	if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
		return nil, fmt.Errorf("decode %s arguments: %w", sendChatToolName, err)
	}
	input.Message = strings.TrimSpace(input.Message)
	input.RoomID = strings.TrimSpace(input.RoomID)
	input.TargetAccountName = strings.TrimSpace(input.TargetAccountName)
	input.Message = sanitizeSolarOutboundMessage(input.Message)

	if input.Message == "" {
		return nil, fmt.Errorf("%s requires message", sendChatToolName)
	}
	if input.RoomID == "" && input.TargetAccountName == "" {
		return nil, fmt.Errorf("%s requires room_id or target_account_name", sendChatToolName)
	}

	logging.Log.Info().
		Str("agent_id", strings.TrimSpace(agentID)).
		Str("tool_name", call.Function.Name).
		Str("tool_call_id", call.ID).
		Str("room_id", input.RoomID).
		Str("target_account_name", input.TargetAccountName).
		Int("message_chars", len(input.Message)).
		Msg("sending solar chat message via tool")

	roomID, messageID, err := s.solar.SendBotMessage(ctx, agentID, input.RoomID, input.TargetAccountName, input.Message)
	if err != nil {
		logging.Log.Error().
			Err(err).
			Str("agent_id", strings.TrimSpace(agentID)).
			Str("tool_name", call.Function.Name).
			Str("tool_call_id", call.ID).
			Str("room_id", input.RoomID).
			Str("target_account_name", input.TargetAccountName).
			Msg("solar chat tool send failed")
		return nil, err
	}
	logging.Log.Info().
		Str("agent_id", strings.TrimSpace(agentID)).
		Str("tool_name", call.Function.Name).
		Str("tool_call_id", call.ID).
		Str("room_id", roomID).
		Str("message_id", messageID).
		Msg("solar chat tool send succeeded")

	payload, err := json.Marshal(map[string]any{
		"ok":                  true,
		"room_id":             roomID,
		"message_id":          messageID,
		"target_account_name": input.TargetAccountName,
		"status":              "message_sent",
	})
	if err != nil {
		return nil, err
	}
	return &executedChatToolResult{
		Content:           string(payload),
		RoomID:            roomID,
		TargetAccountName: input.TargetAccountName,
		ToolName:          call.Function.Name,
		ToolCallID:        call.ID,
	}, nil
}

func (s *ConversationService) executeChatBatchToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input sendChatBatchToolInput
	if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
		return nil, fmt.Errorf("decode %s arguments: %w", sendChatBatchToolName, err)
	}
	input.RoomID = strings.TrimSpace(input.RoomID)
	input.TargetAccountName = strings.TrimSpace(input.TargetAccountName)
	originalTargetAccountName := input.TargetAccountName

	messages := make([]string, 0, len(input.Messages))
	for _, item := range input.Messages {
		if trimmed := strings.TrimSpace(sanitizeSolarOutboundMessage(item)); trimmed != "" {
			messages = append(messages, trimmed)
		}
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("%s requires at least one message", sendChatBatchToolName)
	}
	if input.RoomID == "" && input.TargetAccountName == "" {
		return nil, fmt.Errorf("%s requires room_id or target_account_name", sendChatBatchToolName)
	}

	logging.Log.Info().
		Str("agent_id", strings.TrimSpace(agentID)).
		Str("tool_name", call.Function.Name).
		Str("tool_call_id", call.ID).
		Str("room_id", input.RoomID).
		Str("target_account_name", input.TargetAccountName).
		Int("message_count", len(messages)).
		Msg("sending solar chat message batch via tool")

	resolvedRoomID := input.RoomID
	messageIDs := make([]string, 0, len(messages))
	lastSentAt := time.Time{}
	for i, item := range messages {
		if i > 0 {
			if err := waitForSolarOutboundGap(ctx, lastSentAt); err != nil {
				return nil, err
			}
		}
		roomID, messageID, err := s.solar.SendBotMessage(ctx, agentID, resolvedRoomID, input.TargetAccountName, item)
		if err != nil {
			logging.Log.Error().
				Err(err).
				Str("agent_id", strings.TrimSpace(agentID)).
				Str("tool_name", call.Function.Name).
				Str("tool_call_id", call.ID).
				Int("batch_index", i).
				Str("room_id", resolvedRoomID).
				Msg("solar chat batch send failed")
			return nil, err
		}
		resolvedRoomID = roomID
		input.TargetAccountName = ""
		lastSentAt = time.Now()
		messageIDs = append(messageIDs, messageID)
	}

	payload, err := json.Marshal(map[string]any{
		"ok":                  true,
		"room_id":             resolvedRoomID,
		"message_ids":         messageIDs,
		"target_account_name": originalTargetAccountName,
		"status":              "messages_sent",
	})
	if err != nil {
		return nil, err
	}
	return &executedChatToolResult{
		Content:           string(payload),
		RoomID:            resolvedRoomID,
		TargetAccountName: originalTargetAccountName,
		ToolName:          call.Function.Name,
		ToolCallID:        call.ID,
	}, nil
}

func (s *ConversationService) executeGetUserProfileToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input getUserProfileToolInput
	if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
		return nil, fmt.Errorf("decode %s arguments: %w", getUserProfileToolName, err)
	}
	input.AccountName = strings.TrimSpace(input.AccountName)
	input.AccountID = strings.TrimSpace(input.AccountID)
	if input.AccountName == "" && input.AccountID == "" {
		return nil, fmt.Errorf("%s requires account_name or account_id", getUserProfileToolName)
	}

	account, err := s.solar.GetAccount(ctx, agentID, input.AccountName, input.AccountID)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"account": account,
	}
	if account != nil && strings.TrimSpace(account.Name) != "" {
		profile, err := s.solar.GetAccountProfile(ctx, agentID, account.Name)
		if err != nil {
			return nil, err
		}
		payload["profile"] = profile
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &executedChatToolResult{Content: string(raw), ToolName: call.Function.Name, ToolCallID: call.ID}, nil
}

func (s *ConversationService) executeListUserPostsToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input listUserPostsToolInput
	if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
		return nil, fmt.Errorf("decode %s arguments: %w", listUserPostsToolName, err)
	}
	input.AccountName = strings.TrimSpace(input.AccountName)
	if input.AccountName == "" {
		return nil, fmt.Errorf("%s requires account_name", listUserPostsToolName)
	}
	if input.Take < 1 {
		input.Take = 5
	}
	posts, err := s.solar.ListPublisherPosts(ctx, agentID, input.AccountName, input.Offset, input.Take)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{
		"account_name": input.AccountName,
		"offset":       input.Offset,
		"take":         input.Take,
		"total":        posts.Total,
		"items":        posts.Items,
	})
	if err != nil {
		return nil, err
	}
	return &executedChatToolResult{Content: string(raw), ToolName: call.Function.Name, ToolCallID: call.ID}, nil
}

func (s *ConversationService) executeGetPostToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input getPostToolInput
	if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
		return nil, fmt.Errorf("decode %s arguments: %w", getPostToolName, err)
	}
	input.PostID = strings.TrimSpace(input.PostID)
	if input.PostID == "" {
		return nil, fmt.Errorf("%s requires post_id", getPostToolName)
	}
	post, err := s.solar.GetPost(ctx, agentID, input.PostID)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(post)
	if err != nil {
		return nil, err
	}
	return &executedChatToolResult{Content: string(raw), ToolName: call.Function.Name, ToolCallID: call.ID}, nil
}

func (s *ConversationService) executeListPostRepliesToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input listPostRepliesToolInput
	if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
		return nil, fmt.Errorf("decode %s arguments: %w", listPostRepliesToolName, err)
	}
	input.PostID = strings.TrimSpace(input.PostID)
	if input.PostID == "" {
		return nil, fmt.Errorf("%s requires post_id", listPostRepliesToolName)
	}
	if input.Take < 1 {
		input.Take = 5
	}
	replies, err := s.solar.ListPostReplies(ctx, agentID, input.PostID, input.Offset, input.Take)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{
		"post_id": input.PostID,
		"offset":  input.Offset,
		"take":    input.Take,
		"total":   replies.Total,
		"items":   replies.Items,
	})
	if err != nil {
		return nil, err
	}
	return &executedChatToolResult{Content: string(raw), ToolName: call.Function.Name, ToolCallID: call.ID}, nil
}

func (s *ConversationService) executeListSelfNotesToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	if s.humanize == nil {
		return nil, fmt.Errorf("humanize manager is not configured")
	}
	var input listSelfNotesToolInput
	if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
		return nil, fmt.Errorf("decode %s arguments: %w", listSelfNotesToolName, err)
	}
	notes, err := s.humanize.ListAgentSelfNotes(ctx, agentID, strings.TrimSpace(input.Category))
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{
		"agent_id": agentID,
		"category": strings.TrimSpace(input.Category),
		"items":    notes,
	})
	if err != nil {
		return nil, err
	}
	return &executedChatToolResult{Content: string(raw), ToolName: call.Function.Name, ToolCallID: call.ID}, nil
}

func (s *ConversationService) executeSaveSelfNoteToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	if s.humanize == nil {
		return nil, fmt.Errorf("humanize manager is not configured")
	}
	var input saveSelfNoteToolInput
	if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
		return nil, fmt.Errorf("decode %s arguments: %w", saveSelfNoteToolName, err)
	}
	note, err := s.humanize.SaveAgentSelfNote(ctx, agentID, humanize.AgentSelfNoteInput{
		Key:      input.Key,
		Category: input.Category,
		Content:  input.Content,
	})
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{
		"ok":       true,
		"status":   "self_note_saved",
		"agent_id": agentID,
		"item":     note,
	})
	if err != nil {
		return nil, err
	}
	return &executedChatToolResult{Content: string(raw), ToolName: call.Function.Name, ToolCallID: call.ID}, nil
}

func (s *ConversationService) executeDeleteSelfNoteToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	if s.humanize == nil {
		return nil, fmt.Errorf("humanize manager is not configured")
	}
	var input deleteSelfNoteToolInput
	if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
		return nil, fmt.Errorf("decode %s arguments: %w", deleteSelfNoteToolName, err)
	}
	deleted, err := s.humanize.DeleteAgentSelfNote(ctx, agentID, input.Key)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{
		"ok":       true,
		"status":   "self_note_deleted",
		"agent_id": agentID,
		"key":      input.Key,
		"deleted":  deleted,
	})
	if err != nil {
		return nil, err
	}
	return &executedChatToolResult{Content: string(raw), ToolName: call.Function.Name, ToolCallID: call.ID}, nil
}

func waitForSolarOutboundGap(ctx context.Context, lastSentAt time.Time) error {
	if solarOutboundMessageMinGap <= 0 || lastSentAt.IsZero() {
		return nil
	}
	wait := time.Until(lastSentAt.Add(solarOutboundMessageMinGap))
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
