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
	"src.solsynth.dev/sosys/personality/internal/logging"
)

const sendChatToolName = "send_chat_message"
const sendChatBatchToolName = "send_chat_message_batch"

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

type executedChatToolResult struct {
	Content           string
	RoomID            string
	TargetAccountName string
	ToolName          string
	ToolCallID        string
}

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
	})
	if err != nil {
		return "", err
	}

	thread, err := s.GetConversation(ctx, accountID, threadID)
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
			logging.Log.Info().
				Str("agent_id", agentDef.ID).
				Str("conversation_id", threadID).
				Str("run_id", runID).
				Int("tool_loop_step", step+1).
				Int("response_chars", len(finalContent)).
				Str("response_content", finalContent).
				Msg("chat model returned final response without tool calls")
			if err := s.deliverFinalChatResponse(ctx, thread, agentDef.ID, runID, finalContent); err != nil {
				return "", err
			}
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
			result, err := s.executeChatToolCall(ctx, agentDef.ID, call)
			if err != nil {
				return "", err
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
	}

	return "", fmt.Errorf("chat tool loop exceeded maximum iterations")
}

func (s *ConversationService) deliverFinalChatResponse(
	ctx context.Context,
	thread *database.ConversationThread,
	agentID, runID, content string,
) error {
	content = strings.TrimSpace(content)
	if thread == nil || content == "" || s.solar == nil {
		return nil
	}

	binding, err := s.getSolarRoomBinding(ctx, agentID, thread.ID)
	if err != nil {
		return err
	}
	if binding == nil || strings.TrimSpace(binding.RemoteRoomID) == "" {
		return nil
	}

	logging.Log.Warn().
		Str("agent_id", agentID).
		Str("conversation_id", thread.ID).
		Str("run_id", runID).
		Str("room_id", binding.RemoteRoomID).
		Int("response_chars", len(content)).
		Str("response_content", content).
		Msg("chat model skipped send_chat_message; forwarding final response to solar room")

	roomID, messageID, err := s.solar.SendBotMessage(ctx, agentID, binding.RemoteRoomID, "", content)
	if err != nil {
		return err
	}
	if err := s.ensureSolarRoomBinding(ctx, thread, agentID, roomID, binding.RemoteAccount, time.Now()); err != nil {
		return err
	}
	toolPayload, err := json.Marshal(map[string]any{
		"ok":         true,
		"room_id":    roomID,
		"message_id": messageID,
		"status":     "message_sent_via_fallback",
	})
	if err != nil {
		return err
	}
	_, err = s.createMessageWithMetadata(ctx, thread, stringPtr(runID), "tool", string(toolPayload), nil, map[string]any{
		"tool_name": "fallback_send_chat_message",
	})
	return err
}

func (s *ConversationService) sendChatToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: sendChatToolName,
		Desc: "Send one Solar Network chat message through this agent's configured bot identity. Use room_id when replying in an existing room. Use target_account_name when you need to open or reuse a direct message with a named account. Always call this tool instead of pretending a message was sent.",
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
		Desc: "Send multiple Solar Network chat messages in order. Use this when you intentionally want to split a reply into multiple separate chat messages.",
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

func (s *ConversationService) executeChatToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	if s.solar == nil {
		return nil, fmt.Errorf("solar chat bridge is not configured")
	}
	if call.Function.Name != sendChatToolName && call.Function.Name != sendChatBatchToolName {
		return nil, fmt.Errorf("unsupported tool %q", call.Function.Name)
	}

	if call.Function.Name == sendChatBatchToolName {
		return s.executeChatBatchToolCall(ctx, agentID, call)
	}

	var input sendChatToolInput
	if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
		return nil, fmt.Errorf("decode %s arguments: %w", sendChatToolName, err)
	}
	input.Message = strings.TrimSpace(input.Message)
	input.RoomID = strings.TrimSpace(input.RoomID)
	input.TargetAccountName = strings.TrimSpace(input.TargetAccountName)

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
		if trimmed := strings.TrimSpace(item); trimmed != "" {
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
	for i, item := range messages {
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
