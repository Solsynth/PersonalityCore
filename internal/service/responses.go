package service

import (
	"context"
	"fmt"
	"strings"

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
}

// ExecuteResponse creates the initial conversation when needed and persists
// each response turn through the normal conversation/run lifecycle.
func (s *ConversationService) ExecuteResponse(ctx context.Context, accountID string, input ResponseInput) (*RunResult, error) {
	message := strings.TrimSpace(input.Message)
	if message == "" {
		return nil, fmt.Errorf("input must not be empty")
	}

	threadID := strings.TrimSpace(input.ConversationID)
	previousID := strings.TrimSpace(input.PreviousResponseID)
	if threadID != "" && previousID != "" {
		return nil, fmt.Errorf("conversation_id and previous_response_id are mutually exclusive")
	}
	if previousID != "" {
		var run database.ConversationRun
		if err := s.db.WithContext(ctx).
			Where("id = ? AND account_id = ?", previousID, accountID).
			First(&run).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil, ErrNotFound
			}
			return nil, err
		}
		threadID = run.ThreadID
	}

	if threadID == "" {
		thread, err := s.CreateConversation(ctx, accountID, CreateConversationInput{AgentID: input.AgentID})
		if err != nil {
			return nil, err
		}
		threadID = thread.ID
	} else {
		thread, err := s.GetConversation(ctx, accountID, threadID)
		if err != nil {
			return nil, err
		}
		if agentID := strings.TrimSpace(input.AgentID); agentID != "" && thread.AgentID != agentID {
			return nil, fmt.Errorf("agent_id does not match the response conversation")
		}
	}

	return s.ExecuteRun(ctx, accountID, threadID, RunInput{Message: message})
}
