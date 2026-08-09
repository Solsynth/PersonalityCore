package service

import (
	"context"

	"src.solsynth.dev/sosys/personality/internal/database"
	"src.solsynth.dev/sosys/personality/internal/humanize"
)

type MemoryInput = humanize.MemoryInput

func (s *ConversationService) ListMemories(ctx context.Context, accountID, agentID, query string, limit int) ([]database.AgentMemory, error) {
	if s == nil || s.humanize == nil {
		return nil, nil
	}
	return s.humanize.ListMemories(ctx, accountID, agentID, query, limit)
}

func (s *ConversationService) SaveMemory(ctx context.Context, accountID, agentID string, input MemoryInput) (*database.AgentMemory, error) {
	if s == nil || s.humanize == nil {
		return nil, nil
	}
	return s.humanize.SaveMemory(ctx, accountID, agentID, input)
}

func (s *ConversationService) ForgetMemory(ctx context.Context, accountID, agentID, memoryID string) error {
	if s == nil || s.humanize == nil {
		return nil
	}
	return s.humanize.ForgetMemory(ctx, accountID, agentID, memoryID)
}
