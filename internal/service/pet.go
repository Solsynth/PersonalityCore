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

func isPetThread(thread *database.ConversationThread) bool {
	return thread != nil && strings.EqualFold(strings.TrimSpace(thread.Kind), petThreadKind)
}
