package service

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/persona/internal/database"
	"src.solsynth.dev/sosys/persona/internal/humanize"
)

// ResetAgentMemories purges every trace of an agent for one account: all
// conversation threads and their messages/runs, humanizer state, both memory
// stores, the agent's own self notes, scheduled tasks, and external chat
// bindings. Deleted rows are hard-deleted so the agent genuinely forgets the
// account.
func (s *ConversationService) ResetAgentMemories(ctx context.Context, accountID, agentID string) error {
	accountID = strings.TrimSpace(accountID)
	agentID = strings.TrimSpace(agentID)
	if accountID == "" || agentID == "" {
		return fmt.Errorf("account_id and agent_id are required")
	}
	def, ok := s.registry.Get(agentID)
	if !ok || !def.Enabled {
		return fmt.Errorf("agent %q is unavailable", agentID)
	}

	var threadIDs []string
	if err := s.db.WithContext(ctx).Model(&database.ConversationThread{}).
		Where("account_id = ? AND agent_id = ?", accountID, agentID).
		Pluck("id", &threadIDs).Error; err != nil {
		return err
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(threadIDs) > 0 {
			if err := tx.Unscoped().Delete(&database.ConversationThread{}, "account_id = ? AND agent_id = ?", accountID, agentID).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Delete(&database.ConversationMessage{}, "account_id = ? AND thread_id IN ?", accountID, threadIDs).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Delete(&database.ConversationRun{}, "account_id = ? AND thread_id IN ?", accountID, threadIDs).Error; err != nil {
				return err
			}
		}
		if err := tx.Unscoped().Delete(&database.AgentHumanState{}, "account_id = ? AND agent_id = ?", accountID, agentID).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Delete(&database.AgentMemory{}, "account_id = ? AND agent_id = ?", accountID, agentID).Error; err != nil {
			return err
		}
		// The agent's own notes are agent-global rather than account-scoped, so
		// they need their own predicate to be forgotten with the rest.
		if err := tx.Unscoped().Delete(&database.AgentMemory{}, "agent_id = ? AND scope = ?", agentID, humanize.MemoryScopeAgent).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Delete(&database.AgentManualMemory{}, "account_id = ? AND agent_id = ?", accountID, agentID).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Delete(&database.ScheduledTask{}, "account_id = ? AND agent_id = ?", accountID, agentID).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Delete(&database.ExternalChatBinding{}, "account_id = ? AND agent_id = ?", accountID, agentID).Error; err != nil {
			return err
		}
		return nil
	})
}
