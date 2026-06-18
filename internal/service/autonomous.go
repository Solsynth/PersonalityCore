package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/database"
	"src.solsynth.dev/sosys/personality/internal/logging"
)

const autonomousAbility = "autonomous"

func (s *ConversationService) TriggerAutonomousRun(ctx context.Context, agentID string, input AutonomousRunInput) (*RunResult, error) {
	def, ok := s.registry.Get(agentID)
	if !ok {
		return nil, fmt.Errorf("agent %q is unavailable", strings.TrimSpace(agentID))
	}
	if !agent.HasAbility(def, autonomousAbility) {
		return nil, fmt.Errorf("agent %q does not have %q ability", def.ID, autonomousAbility)
	}
	if strings.TrimSpace(input.Prompt) == "" {
		input.Prompt = strings.TrimSpace(def.Autonomous.WakePrompt)
	}
	if strings.TrimSpace(input.TargetAccountID) == "" && strings.TrimSpace(input.TargetAccountName) != "" && s.solar != nil {
		account, err := s.solar.GetAccount(ctx, def.ID, input.TargetAccountName, "")
		if err != nil {
			return nil, err
		}
		if account != nil {
			input.TargetAccountID = strings.TrimSpace(account.ID)
			if strings.TrimSpace(input.TargetAccountName) == "" {
				input.TargetAccountName = strings.TrimSpace(account.Name)
			}
		}
	}

	thread, err := s.resolveAutonomousThread(ctx, def, input)
	if err != nil {
		return nil, err
	}

	trigger := strings.TrimSpace(input.Trigger)
	if trigger == "" {
		trigger = "manual"
	}
	requestMetadata := ensureMetadataMap(cloneMetadataMap(input.RequestMetadata), 4)
	requestMetadata["source"] = "autonomous"
	requestMetadata["trigger"] = trigger
	if roomID := strings.TrimSpace(input.RoomID); roomID != "" {
		requestMetadata["room_id"] = roomID
	}
	if targetAccountName := strings.TrimSpace(input.TargetAccountName); targetAccountName != "" {
		requestMetadata["target_account_name"] = targetAccountName
	}
	if targetAccountID := strings.TrimSpace(input.TargetAccountID); targetAccountID != "" {
		requestMetadata["target_account_id"] = targetAccountID
	}

	requestContent := buildAutonomousWakePrompt(thread, input, trigger)
	thread, run, requestMessage, err := s.createRunWithRequest(
		ctx,
		thread,
		thread.AccountID,
		"system",
		requestContent,
		false,
		requestMetadata,
	)
	if err != nil {
		return nil, err
	}

	logging.Log.Info().
		Str("conversation_id", thread.ID).
		Str("run_id", run.ID).
		Str("agent_id", thread.AgentID).
		Str("trigger", trigger).
		Msg("starting autonomous generation")

	modelMessages, agentDef, err := s.BuildModelMessages(ctx, thread.AccountID, thread.ID)
	if err != nil {
		_ = s.FailRun(ctx, run, err)
		return nil, err
	}

	run.Model = agentDef.Model
	responseContent := ""
	if agent.HasAbility(agentDef, "chat") && s.solar != nil {
		agentDef = effectiveChatAgentDefinition(agentDef)
		responseContent, err = s.runWithChatTools(ctx, thread.AccountID, thread.ID, run.ID, modelMessages, agentDef)
		if err != nil {
			_ = s.FailRun(ctx, run, err)
			return nil, err
		}
	} else {
		response, err := s.executor.Generate(ctx, agent.RunRequest{Agent: agentDef, Messages: modelMessages})
		if err != nil {
			_ = s.FailRun(ctx, run, err)
			return nil, err
		}
		responseContent = response.Content
	}

	responseMessage, err := s.CompleteRun(ctx, run, responseContent)
	if err != nil {
		return nil, err
	}
	if s.humanize != nil {
		if err := s.humanize.ObserveInteraction(ctx, s.resolveImpressionAccountIDFromRecord(thread.AccountID, requestMessage), agentDef, requestMessage.Content, responseContent); err != nil {
			return nil, err
		}
	}

	return &RunResult{
		Thread:          thread,
		Run:             run,
		RequestMessage:  requestMessage,
		ResponseMessage: responseMessage,
		ResponseContent: responseContent,
	}, nil
}

func (s *ConversationService) TriggerPeriodicAutonomousWake(ctx context.Context, agentID string) error {
	def, ok := s.registry.Get(agentID)
	if !ok || !agent.HasAbility(def, autonomousAbility) || !agent.HasAbility(def, "chat") {
		return nil
	}

	var bindings []database.ExternalChatBinding
	if err := s.db.WithContext(ctx).
		Where("agent_id = ?", def.ID).
		Order("updated_at DESC").
		Find(&bindings).Error; err != nil {
		return err
	}

	for _, binding := range bindings {
		if strings.TrimSpace(binding.ThreadID) == "" {
			continue
		}
		thread, err := s.getConversationByID(ctx, binding.ThreadID)
		if err != nil {
			logging.Log.Error().
				Err(err).
				Str("agent_id", def.ID).
				Str("thread_id", binding.ThreadID).
				Str("room_id", binding.RemoteRoomID).
				Msg("periodic autonomous wake could not load thread")
			continue
		}
		allow, err := s.allowAutonomousOldMessagePickup(ctx, thread, &binding)
		if err != nil {
			logging.Log.Error().
				Err(err).
				Str("agent_id", def.ID).
				Str("thread_id", binding.ThreadID).
				Str("room_id", binding.RemoteRoomID).
				Msg("periodic autonomous wake eligibility check failed")
			continue
		}
		if !allow {
			continue
		}
		_, err = s.TriggerAutonomousRun(ctx, def.ID, AutonomousRunInput{
			ThreadID: binding.ThreadID,
			RoomID:   binding.RemoteRoomID,
			Prompt:   strings.TrimSpace(def.Autonomous.WakePrompt),
			Trigger:  "periodic",
		})
		if err != nil {
			logging.Log.Error().
				Err(err).
				Str("agent_id", def.ID).
				Str("thread_id", binding.ThreadID).
				Str("room_id", binding.RemoteRoomID).
				Msg("periodic autonomous wake failed")
		}
	}

	return nil
}

func (s *ConversationService) allowAutonomousOldMessagePickup(ctx context.Context, thread *database.ConversationThread, binding *database.ExternalChatBinding) (bool, error) {
	if thread == nil || binding == nil {
		return false, nil
	}
	if binding.RemoteRoomType != nil && *binding.RemoteRoomType == 1 {
		return true, nil
	}

	meta, err := s.latestSolarInboundMetadataForThread(ctx, thread.AccountID, thread.ID)
	if err != nil {
		return false, err
	}
	if meta == nil {
		return false, nil
	}
	return meta.MentionedBot, nil
}

func (s *ConversationService) resolveAutonomousThread(ctx context.Context, def agent.Definition, input AutonomousRunInput) (*database.ConversationThread, error) {
	if threadID := strings.TrimSpace(input.ThreadID); threadID != "" {
		thread, err := s.getConversationByID(ctx, threadID)
		if err != nil {
			return nil, err
		}
		if thread.AgentID != def.ID {
			return nil, fmt.Errorf("conversation %q does not belong to agent %q", thread.ID, def.ID)
		}
		return thread, nil
	}

	if roomID := strings.TrimSpace(input.RoomID); roomID != "" {
		binding, err := s.getBindingByRoom(ctx, def.ID, roomID)
		if err != nil {
			return nil, err
		}
		if binding != nil {
			return s.getConversationByID(ctx, binding.ThreadID)
		}

		accountID := externalConversationAccountID(def.ID, roomID)
		thread, err := s.CreateConversation(ctx, accountID, CreateConversationInput{
			AgentID: def.ID,
			Title:   coalesceTitle(strings.TrimSpace(input.TargetAccountName), "Autonomous Solar chat"),
		})
		if err != nil {
			return nil, err
		}
		binding = &database.ExternalChatBinding{
			ID:              newID(),
			AgentID:         def.ID,
			RemoteRoomID:    roomID,
			ThreadID:        thread.ID,
			AccountID:       accountID,
			RemoteAccount:   strings.TrimSpace(input.TargetAccountName),
			EngagementState: solarRoomEngagementStatePassive,
		}
		if err := s.db.WithContext(ctx).Create(binding).Error; err != nil {
			return nil, err
		}
		return thread, nil
	}

	if targetAccountID := strings.TrimSpace(input.TargetAccountID); targetAccountID != "" {
		accountID := fmt.Sprintf("solar:%s:dm:%s", def.ID, targetAccountID)
		if existing, err := s.findConversationByAccountAndAgent(ctx, accountID, def.ID); err != nil {
			return nil, err
		} else if existing != nil {
			return existing, nil
		}
		return s.CreateConversation(ctx, accountID, CreateConversationInput{
			AgentID: def.ID,
			Title:   coalesceTitle(strings.TrimSpace(input.TargetAccountName), targetAccountID),
		})
	}

	return nil, fmt.Errorf("thread_id, room_id, or target_account_id is required")
}

func (s *ConversationService) getConversationByID(ctx context.Context, threadID string) (*database.ConversationThread, error) {
	var thread database.ConversationThread
	if err := s.db.WithContext(ctx).Where("id = ?", strings.TrimSpace(threadID)).First(&thread).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &thread, nil
}

func (s *ConversationService) findConversationByAccountAndAgent(ctx context.Context, accountID, agentID string) (*database.ConversationThread, error) {
	var thread database.ConversationThread
	if err := s.db.WithContext(ctx).
		Where("account_id = ? AND agent_id = ?", strings.TrimSpace(accountID), strings.TrimSpace(agentID)).
		Order("updated_at DESC").
		First(&thread).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &thread, nil
}

func (s *ConversationService) getBindingByRoom(ctx context.Context, agentID, roomID string) (*database.ExternalChatBinding, error) {
	var binding database.ExternalChatBinding
	err := s.db.WithContext(ctx).
		Where("agent_id = ? AND remote_room_id = ?", strings.TrimSpace(agentID), strings.TrimSpace(roomID)).
		First(&binding).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &binding, nil
}

func buildAutonomousWakePrompt(thread *database.ConversationThread, input AutonomousRunInput, trigger string) string {
	lines := []string{
		"Autonomous wake-up triggered.",
		"You initiated this run yourself. There is no new inbound user message attached to this run.",
		fmt.Sprintf("Trigger source: %s.", trigger),
		"Decide whether to stay silent, inspect Solar profile or post context, update self notes, or send proactive outbound chat messages.",
	}
	if thread != nil && strings.TrimSpace(thread.Title) != "" {
		lines = append(lines, fmt.Sprintf("Current conversation title: %q.", thread.Title))
	}
	if roomID := strings.TrimSpace(input.RoomID); roomID != "" {
		lines = append(lines, fmt.Sprintf("Current Solar room ID: %q.", roomID))
	}
	if targetAccountName := strings.TrimSpace(input.TargetAccountName); targetAccountName != "" {
		lines = append(lines, fmt.Sprintf("Preferred Solar target account name: %q.", targetAccountName))
	}
	if targetAccountID := strings.TrimSpace(input.TargetAccountID); targetAccountID != "" {
		lines = append(lines, fmt.Sprintf("Preferred Solar target account id: %q.", targetAccountID))
	}
	if prompt := strings.TrimSpace(input.Prompt); prompt != "" {
		lines = append(lines, "Operator wake prompt: "+prompt)
	}
	return strings.Join(lines, "\n")
}
