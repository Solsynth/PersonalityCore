package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/personality/internal/database"
	"src.solsynth.dev/sosys/personality/internal/logging"
)

type SolarChatBridge interface {
	SendBotMessage(ctx context.Context, agentID, roomID, targetAccountName, content string) (resolvedRoomID, messageID string, err error)
	TrackRoom(agentID, roomID string)
}

type SolarRoomState struct {
	RoomID        string
	LastMessageAt *time.Time
}

type ExternalInboundMessage struct {
	RoomID          string
	MessageID       string
	MessageType     string
	Content         string
	SenderAccountID string
	SenderName      string
	SenderNick      string
	CreatedAt       time.Time
}

func (s *ConversationService) SetSolarChatBridge(bridge SolarChatBridge) {
	s.solar = bridge
}

func (s *ConversationService) ListTrackedSolarRooms(ctx context.Context, agentID string) ([]SolarRoomState, error) {
	var bindings []database.ExternalChatBinding
	if err := s.db.WithContext(ctx).
		Where("agent_id = ?", strings.TrimSpace(agentID)).
		Find(&bindings).Error; err != nil {
		return nil, err
	}

	rooms := make([]SolarRoomState, 0, len(bindings))
	for _, binding := range bindings {
		if roomID := strings.TrimSpace(binding.RemoteRoomID); roomID != "" {
			rooms = append(rooms, SolarRoomState{
				RoomID:        roomID,
				LastMessageAt: binding.LastMessageAt,
			})
		}
	}
	return rooms, nil
}

func (s *ConversationService) HandleSolarInboundMessage(ctx context.Context, agentID string, input ExternalInboundMessage) error {
	if strings.TrimSpace(input.RoomID) == "" || strings.TrimSpace(input.Content) == "" {
		logging.Log.Debug().
			Str("agent_id", strings.TrimSpace(agentID)).
			Str("room_id", strings.TrimSpace(input.RoomID)).
			Str("message_id", strings.TrimSpace(input.MessageID)).
			Msg("ignoring solar inbound message without room or content")
		return nil
	}
	if messageType := strings.TrimSpace(input.MessageType); messageType != "" && messageType != "text" {
		logging.Log.Debug().
			Str("agent_id", strings.TrimSpace(agentID)).
			Str("room_id", strings.TrimSpace(input.RoomID)).
			Str("message_id", strings.TrimSpace(input.MessageID)).
			Str("message_type", messageType).
			Msg("ignoring unsupported solar inbound message type")
		return nil
	}

	logging.Log.Info().
		Str("agent_id", strings.TrimSpace(agentID)).
		Str("room_id", strings.TrimSpace(input.RoomID)).
		Str("message_id", strings.TrimSpace(input.MessageID)).
		Str("sender_account_id", strings.TrimSpace(input.SenderAccountID)).
		Int("content_chars", len(strings.TrimSpace(input.Content))).
		Msg("handling solar inbound message")

	binding, err := s.getOrCreateExternalBinding(ctx, agentID, input)
	if err != nil {
		return err
	}
	if s.solar != nil {
		s.solar.TrackRoom(agentID, input.RoomID)
	}

	logging.Log.Info().
		Str("agent_id", strings.TrimSpace(agentID)).
		Str("room_id", strings.TrimSpace(input.RoomID)).
		Str("conversation_id", binding.ThreadID).
		Str("account_id", binding.AccountID).
		Msg("triggering agent run for solar inbound message")

	_, err = s.ExecuteRun(ctx, binding.AccountID, binding.ThreadID, RunInput{
		Message: strings.TrimSpace(input.Content),
		Stream:  false,
	})
	if err != nil {
		logging.Log.Error().
			Err(err).
			Str("agent_id", strings.TrimSpace(agentID)).
			Str("room_id", strings.TrimSpace(input.RoomID)).
			Str("conversation_id", binding.ThreadID).
			Msg("solar inbound message run failed")
		return err
	}
	logging.Log.Info().
		Str("agent_id", strings.TrimSpace(agentID)).
		Str("room_id", strings.TrimSpace(input.RoomID)).
		Str("conversation_id", binding.ThreadID).
		Msg("solar inbound message run completed")
	return err
}

func (s *ConversationService) ensureSolarRoomBinding(
	ctx context.Context,
	thread *database.ConversationThread,
	agentID, roomID, remoteAccount string,
	at time.Time,
) error {
	roomID = strings.TrimSpace(roomID)
	agentID = strings.TrimSpace(agentID)
	if thread == nil || roomID == "" || agentID == "" {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}

	var binding database.ExternalChatBinding
	err := s.db.WithContext(ctx).
		Where("agent_id = ? AND remote_room_id = ?", agentID, roomID).
		First(&binding).Error
	if err == nil {
		binding.ThreadID = thread.ID
		binding.AccountID = thread.AccountID
		binding.LastMessageAt = &at
		if strings.TrimSpace(binding.RemoteAccount) == "" && strings.TrimSpace(remoteAccount) != "" {
			binding.RemoteAccount = strings.TrimSpace(remoteAccount)
		}
		return s.db.WithContext(ctx).Save(&binding).Error
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	binding = database.ExternalChatBinding{
		ID:            newID(),
		AgentID:       agentID,
		RemoteRoomID:  roomID,
		ThreadID:      thread.ID,
		AccountID:     thread.AccountID,
		RemoteAccount: strings.TrimSpace(remoteAccount),
		LastMessageAt: &at,
	}
	return s.db.WithContext(ctx).Create(&binding).Error
}

func (s *ConversationService) getOrCreateExternalBinding(ctx context.Context, agentID string, input ExternalInboundMessage) (*database.ExternalChatBinding, error) {
	var binding database.ExternalChatBinding
	err := s.db.WithContext(ctx).
		Where("agent_id = ? AND remote_room_id = ?", strings.TrimSpace(agentID), strings.TrimSpace(input.RoomID)).
		First(&binding).Error
	if err == nil {
		at := input.CreatedAt
		if at.IsZero() {
			at = time.Now()
		}
		binding.LastMessageAt = &at
		if strings.TrimSpace(input.SenderAccountID) != "" {
			binding.RemoteAccountID = strings.TrimSpace(input.SenderAccountID)
		}
		if remoteAccount := firstNonEmpty(input.SenderName, input.SenderNick); remoteAccount != "" {
			binding.RemoteAccount = remoteAccount
		}
		logging.Log.Debug().
			Str("agent_id", strings.TrimSpace(agentID)).
			Str("room_id", strings.TrimSpace(input.RoomID)).
			Str("conversation_id", binding.ThreadID).
			Msg("reusing existing solar room binding")
		return &binding, s.db.WithContext(ctx).Save(&binding).Error
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	accountID := externalConversationAccountID(agentID, input.RoomID)
	title := firstNonEmpty(input.SenderNick, input.SenderName, "Solar chat")
	thread, err := s.CreateConversation(ctx, accountID, CreateConversationInput{
		AgentID: strings.TrimSpace(agentID),
		Title:   title,
	})
	if err != nil {
		return nil, err
	}

	now := input.CreatedAt
	if now.IsZero() {
		now = time.Now()
	}
	binding = database.ExternalChatBinding{
		ID:              newID(),
		AgentID:         strings.TrimSpace(agentID),
		RemoteRoomID:    strings.TrimSpace(input.RoomID),
		ThreadID:        thread.ID,
		AccountID:       accountID,
		RemoteAccountID: strings.TrimSpace(input.SenderAccountID),
		RemoteAccount:   firstNonEmpty(input.SenderName, input.SenderNick),
		LastMessageAt:   &now,
	}
	if err := s.db.WithContext(ctx).Create(&binding).Error; err != nil {
		return nil, err
	}
	logging.Log.Info().
		Str("agent_id", strings.TrimSpace(agentID)).
		Str("room_id", strings.TrimSpace(input.RoomID)).
		Str("conversation_id", thread.ID).
		Str("account_id", accountID).
		Msg("created new solar room binding")
	return &binding, nil
}

func externalConversationAccountID(agentID, roomID string) string {
	return fmt.Sprintf("solar:%s:%s", strings.TrimSpace(agentID), strings.TrimSpace(roomID))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *ConversationService) getSolarRoomBinding(ctx context.Context, agentID, threadID string) (*database.ExternalChatBinding, error) {
	var binding database.ExternalChatBinding
	err := s.db.WithContext(ctx).
		Where("agent_id = ? AND thread_id = ?", strings.TrimSpace(agentID), strings.TrimSpace(threadID)).
		First(&binding).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &binding, nil
}

func (s *ConversationService) buildSolarSystemOverlay(ctx context.Context, agentID, threadID string) (string, error) {
	binding, err := s.getSolarRoomBinding(ctx, agentID, threadID)
	if err != nil {
		return "", err
	}
	if binding == nil {
		return "", nil
	}
	return fmt.Sprintf(
		"This conversation is connected to a Solar Network chat room.\n"+
			"You must deliver every user-facing reply by calling the send_chat_message tool with room_id=%q.\n"+
			"Do not stop after writing a normal assistant reply in the model output; use the tool so the message is actually sent.\n"+
			"Current remote account: %q (%s).\n"+
			"Do not claim a chat message was sent unless the tool succeeds.",
		binding.RemoteRoomID,
		binding.RemoteAccount,
		binding.RemoteAccountID,
	), nil
}
