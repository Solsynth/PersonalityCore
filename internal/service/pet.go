package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/database"
	"src.solsynth.dev/sosys/personality/internal/humanize"
)

const petThreadKind = "pet"

func (s *ConversationService) GetOrCreatePetThread(ctx context.Context, accountID, agentID string) (*database.ConversationThread, error) {
	accountID = strings.TrimSpace(accountID)
	agentID = strings.TrimSpace(agentID)
	if accountID == "" || agentID == "" {
		return nil, fmt.Errorf("account_id and agent_id are required")
	}
	def, ok := s.registry.Get(agentID)
	if !ok || !agent.HasAbility(def, "pet") {
		return nil, fmt.Errorf("agent %q is not a pet agent", agentID)
	}

	var session database.PetSession
	err := s.db.WithContext(ctx).Where("account_id = ? AND agent_id = ?", accountID, agentID).First(&session).Error
	if err == nil {
		return s.GetConversation(ctx, accountID, session.ThreadID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	thread, err := s.CreateConversation(ctx, accountID, CreateConversationInput{AgentID: agentID, Title: "Pet"})
	if err != nil {
		return nil, err
	}
	thread.Kind = petThreadKind
	if err := s.db.WithContext(ctx).Save(thread).Error; err != nil {
		_ = s.db.WithContext(ctx).Delete(thread).Error
		return nil, err
	}
	session = database.PetSession{
		ID:        ulid.Make().String(),
		AccountID: accountID,
		AgentID:   agentID,
		ThreadID:  thread.ID,
	}
	if err := s.db.WithContext(ctx).Create(&session).Error; err != nil {
		var existing database.PetSession
		if lookupErr := s.db.WithContext(ctx).Where("account_id = ? AND agent_id = ?", accountID, agentID).First(&existing).Error; lookupErr == nil {
			_ = s.db.WithContext(ctx).Delete(thread).Error
			return s.GetConversation(ctx, accountID, existing.ThreadID)
		}
		_ = s.db.WithContext(ctx).Delete(thread).Error
		return nil, err
	}
	return thread, nil
}

func (s *ConversationService) ResetPetThread(ctx context.Context, accountID, agentID string) error {
	var session database.PetSession
	err := s.db.WithContext(ctx).Where("account_id = ? AND agent_id = ?", strings.TrimSpace(accountID), strings.TrimSpace(agentID)).First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&database.ConversationThread{}, "id = ? AND account_id = ?", session.ThreadID, accountID).Error; err != nil {
			return err
		}
		return tx.Delete(&session).Error
	})
}

// ResetAgentMemories purges every trace of an agent for one account: all
// conversation threads and their messages/runs, the pet session (and with it
// the affection score), humanizer state, both memory stores, self-notes,
// scheduled tasks, and external chat bindings. Deleted rows are hard-deleted
// so the agent genuinely forgets the account.
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
		if err := tx.Unscoped().Delete(&database.PetSession{}, "account_id = ? AND agent_id = ?", accountID, agentID).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Delete(&database.AgentHumanState{}, "account_id = ? AND agent_id = ?", accountID, agentID).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Delete(&database.AgentMemory{}, "account_id = ? AND agent_id = ?", accountID, agentID).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Delete(&database.AgentManualMemory{}, "account_id = ? AND agent_id = ?", accountID, agentID).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Delete(&database.AgentSelfNote{}, "agent_id = ?", agentID).Error; err != nil {
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

// PetAffection is the affection state of one (account, pet agent) session.
type PetAffection struct {
	AgentID   string `json:"agent_id"`
	Affection int    `json:"affection"`
	Level     string `json:"level"`
	Reason    string `json:"reason,omitempty"`
}

func (s *ConversationService) getPetSession(ctx context.Context, accountID, agentID string) (*database.PetSession, error) {
	var session database.PetSession
	err := s.db.WithContext(ctx).Where("account_id = ? AND agent_id = ?", strings.TrimSpace(accountID), strings.TrimSpace(agentID)).First(&session).Error
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// GetPetAffection returns the current affection for a pet session. Sessions
// are only created by GetOrCreatePetThread, so a session-less lookup yields
// ErrNotFound instead of silently manufacturing one.
func (s *ConversationService) GetPetAffection(ctx context.Context, accountID, agentID string) (*PetAffection, error) {
	agentID = strings.TrimSpace(agentID)
	session, err := s.getPetSession(ctx, accountID, agentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &PetAffection{
		AgentID:   session.AgentID,
		Affection: session.Affection,
		Level:     humanize.AffectionLevel(session.Affection),
		Reason:    strings.TrimSpace(session.AffectionReason),
	}, nil
}

// AdjustPetAffection applies a model-directed delta to the session's
// affection, clamped to 0-100, and records the model's stated reason.
// Unbounded accumulation is impossible because the value is clamped.
func (s *ConversationService) AdjustPetAffection(ctx context.Context, accountID, agentID string, delta int, reason string) (*PetAffection, error) {
	agentID = strings.TrimSpace(agentID)
	session, err := s.getPetSession(ctx, accountID, agentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("pet session not found for agent %q", agentID)
		}
		return nil, err
	}
	if delta == 0 && strings.TrimSpace(reason) == "" {
		return s.GetPetAffection(ctx, accountID, agentID)
	}
	session.Affection = clampAffection(session.Affection + delta)
	if strings.TrimSpace(reason) != "" {
		session.AffectionReason = strings.TrimSpace(reason)
	}
	if err := s.db.WithContext(ctx).Save(session).Error; err != nil {
		return nil, err
	}
	return &PetAffection{
		AgentID:   session.AgentID,
		Affection: session.Affection,
		Level:     humanize.AffectionLevel(session.Affection),
		Reason:    strings.TrimSpace(session.AffectionReason),
	}, nil
}

func clampAffection(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func isPetThread(thread *database.ConversationThread) bool {
	return thread != nil && strings.EqualFold(strings.TrimSpace(thread.Kind), petThreadKind)
}
