package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/personality/internal/database"
)

// ResponseInput describes one native, stateful response turn. A response can
// continue either a conversation directly or the run returned by a prior turn.
type ResponseInput struct {
	AgentID            string
	ConversationID     string
	PreviousResponseID string
	Message            string
	ClientTools        []*schema.ToolInfo
	ToolOutputs        []ResponseToolOutput
}

type ResponseToolOutput struct {
	CallID string
	Name   string
	Output string
}

type ResponseResult struct {
	Thread          *database.ConversationThread
	Run             *database.ConversationRun
	ResponseMessage *database.ConversationMessage
	ResponseContent string
	ToolCalls       []schema.ToolCall
	Model           string
}

// ExecuteResponse creates the initial conversation when needed and persists
// each response turn through the normal conversation/run lifecycle. Server
// tools execute in PersonalityCore; client tools are returned as tool calls and
// resume through ToolOutputs on the next request.
func (s *ConversationService) ExecuteResponse(ctx context.Context, accountID string, input ResponseInput) (*ResponseResult, error) {
	message := strings.TrimSpace(input.Message)
	threadID := strings.TrimSpace(input.ConversationID)
	previousID := strings.TrimSpace(input.PreviousResponseID)
	if len(input.ToolOutputs) > 0 && previousID == "" {
		return nil, fmt.Errorf("previous_response_id is required for tool outputs")
	}
	if threadID != "" && previousID != "" {
		return nil, fmt.Errorf("conversation_id and previous_response_id are mutually exclusive")
	}

	var previousRun *database.ConversationRun
	if previousID != "" {
		var run database.ConversationRun
		if err := s.db.WithContext(ctx).
			Where("id = ? AND account_id = ?", previousID, accountID).
			First(&run).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if run.Status != "requires_action" && len(input.ToolOutputs) > 0 {
			return nil, fmt.Errorf("response does not require tool outputs")
		}
		previousRun = &run
		threadID = run.ThreadID
	}

	var (
		thread *database.ConversationThread
		run    *database.ConversationRun
		err    error
	)
	if threadID == "" {
		thread, err = s.CreateConversation(ctx, accountID, CreateConversationInput{AgentID: input.AgentID})
		if err != nil {
			return nil, err
		}
		threadID = thread.ID
	} else {
		thread, err = s.GetConversation(ctx, accountID, threadID)
		if err != nil {
			return nil, err
		}
		if agentID := strings.TrimSpace(input.AgentID); agentID != "" && thread.AgentID != agentID {
			return nil, fmt.Errorf("agent_id does not match the response conversation")
		}
	}

	if len(input.ToolOutputs) > 0 {
		if previousRun == nil {
			return nil, fmt.Errorf("previous response is required for tool outputs")
		}
		first := input.ToolOutputs[0]
		_, run, _, err = s.createRunWithRequest(ctx, thread, accountID, "tool", first.Output, false, map[string]any{
			"tool_call_id": first.CallID,
			"tool_name":    first.Name,
		})
		if err != nil {
			return nil, err
		}
		for _, output := range input.ToolOutputs[1:] {
			if _, err := s.createMessageWithMetadata(ctx, thread, &run.ID, "tool", output.Output, nil, map[string]any{
				"tool_call_id": output.CallID,
				"tool_name":    output.Name,
			}); err != nil {
				_ = s.FailRun(ctx, run, err)
				return nil, err
			}
		}
	} else {
		if message == "" {
			return nil, fmt.Errorf("input must not be empty")
		}
		_, run, _, err = s.createRunWithRequest(ctx, thread, accountID, "user", message, false, nil)
		if err != nil {
			return nil, err
		}
	}

	modelMessages, agentDef, err := s.BuildModelMessages(ctx, accountID, threadID, thread.PerkLevel)
	if err != nil {
		_ = s.FailRun(ctx, run, err)
		return nil, err
	}
	result, err := s.CompleteOpenAI(ctx, OpenAICompletionInput{
		AgentID:            thread.AgentID,
		AccountID:          accountID,
		Messages:           modelMessages,
		ClientTools:        input.ClientTools,
		IncludeServerTools: true,
	})
	if err != nil {
		_ = s.FailRun(ctx, run, err)
		return nil, err
	}
	if result == nil || result.Message == nil {
		_ = s.FailRun(ctx, run, errors.New("generation returned no message"))
		return nil, fmt.Errorf("generation returned no message")
	}

	run.Model = result.Model
	if err := s.db.WithContext(ctx).Save(run).Error; err != nil {
		return nil, err
	}
	s.recordBilling(ctx, run, agentDef, result.Usage)

	if len(result.Message.ToolCalls) > 0 {
		metadata := map[string]any{"tool_calls": result.Message.ToolCalls}
		if strings.TrimSpace(result.Message.ReasoningContent) != "" {
			metadata["reasoning_content"] = result.Message.ReasoningContent
		}
		responseMessage, err := s.createMessageWithMetadata(ctx, thread, &run.ID, "assistant", result.Message.Content, stringPtr(run.Model), metadata)
		if err != nil {
			_ = s.FailRun(ctx, run, err)
			return nil, err
		}
		now := responseMessage.CreatedAt
		run.Status = "requires_action"
		run.ResponseMessageID = &responseMessage.ID
		run.CompletedAt = &now
		if err := s.db.WithContext(ctx).Save(run).Error; err != nil {
			return nil, err
		}
		return &ResponseResult{Thread: thread, Run: run, ResponseMessage: responseMessage, ResponseContent: result.Message.Content, ToolCalls: result.Message.ToolCalls, Model: result.Model}, nil
	}

	responseMessage, err := s.CompleteRun(ctx, run, result.Message.Content)
	if err != nil {
		return nil, err
	}
	return &ResponseResult{Thread: thread, Run: run, ResponseMessage: responseMessage, ResponseContent: result.Message.Content, Model: result.Model}, nil
}
