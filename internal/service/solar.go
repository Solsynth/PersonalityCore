package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/personality/internal/database"
	"src.solsynth.dev/sosys/personality/internal/logging"
	"src.solsynth.dev/sosys/personality/internal/solar"
)

type SolarChatBridge interface {
	SendBotMessage(ctx context.Context, agentID, roomID, targetAccountName, targetAccountID, content string) (resolvedRoomID, messageID string, err error)
	TrackRoom(agentID, roomID string)
	GetAccount(ctx context.Context, agentID, accountName, accountID string) (*solar.Account, error)
	GetAccountProfile(ctx context.Context, agentID, accountName string) (solar.AccountProfile, error)
	GetMessage(ctx context.Context, agentID, roomID, messageID string) (*solar.ChatMessage, error)
	GetPost(ctx context.Context, agentID, postID string) (solar.Post, error)
	ListPublisherPosts(ctx context.Context, agentID, accountName string, offset, take int) (*solar.PaginatedPosts, error)
	ListPostReplies(ctx context.Context, agentID, postID string, offset, take int) (*solar.PaginatedPosts, error)
}

type SolarRoomState struct {
	RoomID        string
	LastMessageAt *time.Time
}

type ExternalInboundMessage struct {
	RoomID              string
	RoomType            int
	MessageID           string
	MessageType         string
	Content             string
	Attachments         []solar.ChatAttachment
	SenderAccountID     string
	SenderName          string
	SenderNick          string
	MentionedBot        bool
	RepliedMessageID    string
	RepliedMessageContent string
	CreatedAt           time.Time
}

const (
	solarRoomEngagementStatePassive = "passive"
	solarRoomEngagementStateActive  = "active"
)

const (
	solarReplyForceAllow = "force_allow" // mentioned – must send
	solarReplyAllow      = "allow"       // active engagement – agent decides
	solarReplySuppress   = "suppress"    // passive – must not send
)

const solarRoomActiveWindow = 5 * time.Minute

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
	if s.solarInbound == nil {
		return s.handleSolarInboundMessageNow(ctx, agentID, input)
	}
	return s.solarInbound.Enqueue(ctx, agentID, input)
}

func (s *ConversationService) handleSolarInboundBatch(ctx context.Context, agentID string, inputs []ExternalInboundMessage) error {
	if len(inputs) == 0 {
		return nil
	}
	merged := inputs[0]
	if len(inputs) > 1 {
		var builder strings.Builder
		var mentions bool
		attachments := append([]solar.ChatAttachment(nil), merged.Attachments...)
		roomType := merged.RoomType
		repliedMessageID := merged.RepliedMessageID
		repliedMessageContent := merged.RepliedMessageContent
		for i, item := range inputs {
			if i > 0 {
				builder.WriteString("\n\n")
			}
			label := firstNonEmpty(item.SenderNick, item.SenderName, item.SenderAccountID, "unknown")
			content := strings.TrimSpace(item.Content)
			if content == "" && len(item.Attachments) > 0 {
				content = fmt.Sprintf("[attached %d file(s)]", len(item.Attachments))
			}
			builder.WriteString(fmt.Sprintf("%s: %s", label, content))
			mentions = mentions || item.MentionedBot
			attachments = append(attachments, item.Attachments...)
			if roomType == 0 && item.RoomType == 1 {
				roomType = 1
			}
			if strings.TrimSpace(repliedMessageID) == "" && strings.TrimSpace(item.RepliedMessageID) != "" {
				repliedMessageID = strings.TrimSpace(item.RepliedMessageID)
				repliedMessageContent = item.RepliedMessageContent
			}
		}
		merged.Content = builder.String()
		merged.MentionedBot = mentions
		merged.Attachments = attachments
		merged.RoomType = roomType
		merged.RepliedMessageID = repliedMessageID
		merged.RepliedMessageContent = repliedMessageContent
		merged.MessageID = inputs[len(inputs)-1].MessageID
		merged.CreatedAt = inputs[len(inputs)-1].CreatedAt
	}
	return s.handleSolarInboundMessageNow(ctx, agentID, merged)
}

func (s *ConversationService) handleSolarInboundMessageNow(ctx context.Context, agentID string, input ExternalInboundMessage) error {
	inputParts := s.buildSolarInboundInputParts(input.Attachments)
	if strings.TrimSpace(input.RoomID) == "" || (strings.TrimSpace(input.Content) == "" && len(inputParts) == 0) {
		logging.Log.Debug().
			Str("agent_id", strings.TrimSpace(agentID)).
			Str("room_id", strings.TrimSpace(input.RoomID)).
			Str("message_id", strings.TrimSpace(input.MessageID)).
			Msg("ignoring solar inbound message without room, content, or supported attachments")
		return nil
	}
	if messageType := strings.TrimSpace(input.MessageType); messageType != "" && messageType != "text" && len(inputParts) == 0 {
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
		Int("room_type", input.RoomType).
		Bool("mentioned_bot", input.MentionedBot).
		Str("sender_account_id", strings.TrimSpace(input.SenderAccountID)).
		Int("content_chars", len(strings.TrimSpace(input.Content))).
		Int("attachment_count", len(input.Attachments)).
		Int("vision_input_part_count", len(inputParts)).
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
		Message:    strings.TrimSpace(input.Content),
		InputParts: inputParts,
		Stream:     false,
		RequestMetadata: map[string]any{
			"source":                "solar",
			"room_type":             input.RoomType,
			"mentioned_bot":         input.MentionedBot,
			"sender_account_id":     strings.TrimSpace(input.SenderAccountID),
			"sender_account_name":   strings.TrimSpace(input.SenderName),
			"sender_nick":           strings.TrimSpace(input.SenderNick),
			"replied_message_id":    strings.TrimSpace(input.RepliedMessageID),
			"replied_message_content": strings.TrimSpace(input.RepliedMessageContent),
		},
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

func (s *ConversationService) buildSolarInboundInputParts(attachments []solar.ChatAttachment) []userMessageInputPart {
	if len(attachments) == 0 {
		return nil
	}
	baseURL := ""
	if s.cfg != nil {
		baseURL = strings.TrimSpace(s.cfg.SolarNetwork.BaseURL)
	}
	if baseURL == "" {
		return nil
	}

	parts := make([]userMessageInputPart, 0, len(attachments))
	for _, attachment := range attachments {
		mimeType := strings.ToLower(strings.TrimSpace(attachment.MIMEType))
		fileID := strings.TrimSpace(attachment.ID)
		if fileID == "" || !strings.HasPrefix(mimeType, "image/") {
			continue
		}
		parts = append(parts, userMessageInputPart{
			Type:     "image_url",
			ImageURL: strings.TrimRight(baseURL, "/") + "/drive/files/" + url.PathEscape(fileID),
			MIMEType: mimeType,
		})
	}
	return parts
}

func (s *ConversationService) FlushSolarInboundBatches(ctx context.Context) error {
	if s.solarInbound == nil {
		return nil
	}
	return s.solarInbound.FlushAll(ctx)
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
		if strings.TrimSpace(binding.EngagementState) == "" {
			binding.EngagementState = solarRoomEngagementStatePassive
		}
		s.applySolarOutboundEngagementState(&binding, at)
		return s.db.WithContext(ctx).Save(&binding).Error
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	binding = database.ExternalChatBinding{
		ID:              newID(),
		AgentID:         agentID,
		RemoteRoomID:    roomID,
		EngagementState: solarRoomEngagementStatePassive,
		ThreadID:        thread.ID,
		AccountID:       thread.AccountID,
		RemoteAccount:   strings.TrimSpace(remoteAccount),
		LastMessageAt:   &at,
	}
	s.applySolarOutboundEngagementState(&binding, at)
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
		if roomType := roomTypePtr(input.RoomType); roomType != nil {
			binding.RemoteRoomType = roomType
		}
		if strings.TrimSpace(input.SenderAccountID) != "" {
			binding.RemoteAccountID = strings.TrimSpace(input.SenderAccountID)
		}
		if remoteAccount := firstNonEmpty(input.SenderName, input.SenderNick); remoteAccount != "" {
			binding.RemoteAccount = remoteAccount
		}
		s.applySolarRoomEngagementState(&binding, input, at)
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
		RemoteRoomType:  roomTypePtr(input.RoomType),
		EngagementState: solarRoomEngagementStatePassive,
		ThreadID:        thread.ID,
		AccountID:       accountID,
		RemoteAccountID: strings.TrimSpace(input.SenderAccountID),
		RemoteAccount:   firstNonEmpty(input.SenderName, input.SenderNick),
		LastMessageAt:   &now,
	}
	s.applySolarRoomEngagementState(&binding, input, now)
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

func roomTypePtr(value int) *int {
	if value != 0 && value != 1 {
		return nil
	}
	v := value
	return &v
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

func (s *ConversationService) buildSolarSystemOverlay(ctx context.Context, agentID, threadID string, records []database.ConversationMessage) (string, error) {
	binding, err := s.getSolarRoomBinding(ctx, agentID, threadID)
	if err != nil {
		return "", err
	}
	if binding == nil {
		return "", nil
	}
	inboundMeta := latestSolarInboundMetadata(records)

	parts := []string{
		solarRoomTypePrompt(binding.RemoteRoomType),
		"IMPORTANT: You MUST use the send_chat_message or send_chat_message_batch tool to send any reply. Do NOT write reply text in assistant content.",
		"If you decide not to reply, use the no_reply tool explicitly.",
		"Do NOT output reply text directly - it will be ignored. Always use tools.",
		fmt.Sprintf("Current room_id: %q. Always pass this as room_id when calling send_chat_message.", binding.RemoteRoomID),
		solarRoomBehaviorPrompt(binding.RemoteRoomType),
		solarInboundPrompt(inboundMeta),
		solarRoomEngagementPrompt(binding),
		solarSenderIdentityPrompt(inboundMeta, binding),
		fmt.Sprintf("Current remote account: %q (%s).", binding.RemoteAccount, binding.RemoteAccountID),
	}

	// Inject sender profile if available
	senderName := binding.RemoteAccount
	if inboundMeta != nil && strings.TrimSpace(inboundMeta.SenderAccountName) != "" {
		senderName = inboundMeta.SenderAccountName
	}
	if senderName != "" {
		if profile, err := s.getCachedSolarUserProfile(ctx, agentID, senderName); err == nil && profile != nil {
			parts = append(parts, solarUserProfilePrompt(profile))
		}
	}

	return strings.Join(parts, "\n"), nil
}

func solarRoomTypePrompt(roomType *int) string {
	if roomType != nil && *roomType == 1 {
		return "This conversation is connected to a Solar Network direct message. This is a DM, not a group chat."
	}
	return "This conversation is connected to a Solar Network group chat. This is not a DM. Multiple different users may be speaking, so track participants carefully."
}

func solarRoomBehaviorPrompt(roomType *int) string {
	if roomType != nil && *roomType == 1 {
		return "Because this is a DM, you can respond more proactively, warmly, and conversationally."
	}
	return "Because this is a group chat, pay extra attention to which participant sent each message, avoid mixing different users together, be selective, keep replies concise, and avoid jumping into every message unless the bot was explicitly mentioned."
}

func solarUserProfilePrompt(profile solar.AccountProfile) string {
	if profile == nil {
		return ""
	}
	var parts []string
	if bio, ok := profile["bio"]; ok {
		if s, ok := bio.(string); ok && strings.TrimSpace(s) != "" {
			parts = append(parts, fmt.Sprintf("bio: %s", s))
		}
	}
	if gender, ok := profile["gender"]; ok {
		if s, ok := gender.(string); ok && strings.TrimSpace(s) != "" {
			parts = append(parts, fmt.Sprintf("gender: %s", s))
		}
	}
	if pronouns, ok := profile["pronouns"]; ok {
		if s, ok := pronouns.(string); ok && strings.TrimSpace(s) != "" {
			parts = append(parts, fmt.Sprintf("pronouns: %s", s))
		}
	}
	if location, ok := profile["location"]; ok {
		if s, ok := location.(string); ok && strings.TrimSpace(s) != "" {
			parts = append(parts, fmt.Sprintf("location: %s", s))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("Sender profile: %s.", strings.Join(parts, ", "))
}

func latestSolarInboundMetadata(records []database.ConversationMessage) *solarInboundRequestMetadata {
	for _, record := range records {
		if strings.ToLower(record.Role) != "user" {
			continue
		}
		var meta solarInboundRequestMetadata
		if decodeMessageMetadata(record.Metadata, &meta) != nil {
			continue
		}
		if strings.TrimSpace(meta.Source) != "solar" {
			continue
		}
		return &meta
	}
	return nil
}

func solarInboundPrompt(meta *solarInboundRequestMetadata) string {
	if meta == nil {
		return "No special inbound routing hint is available for the latest message."
	}
	if meta.RoomType == 1 {
		if strings.TrimSpace(meta.RepliedMessageContent) != "" {
			return fmt.Sprintf("The latest inbound message is from a DM and is replying to: %q. It is appropriate to reply proactively.", meta.RepliedMessageContent)
		}
		return "The latest inbound message is from a DM. It is appropriate to reply proactively."
	}
	if meta.MentionedBot {
		if strings.TrimSpace(meta.RepliedMessageContent) != "" {
			return fmt.Sprintf("The latest inbound group message mentioned the bot. The replied message content is: %q. Decide whether to join the conversation; if you reply, write the outbound chat message text directly.", meta.RepliedMessageContent)
		}
		return "The latest inbound group message mentioned the bot. Decide whether to join the conversation; if you reply, write the outbound chat message text directly."
	}
	if strings.TrimSpace(meta.RepliedMessageContent) != "" {
		return fmt.Sprintf("The latest inbound group message did not mention the bot. It is replying to: %q. Decide whether to join the conversation; if you reply, write the outbound chat message text directly.", meta.RepliedMessageContent)
	}
	return "The latest inbound group message did not mention the bot. Decide whether to join the conversation; if you reply, write the outbound chat message text directly."
}

func solarSenderIdentityPrompt(meta *solarInboundRequestMetadata, binding *database.ExternalChatBinding) string {
	username := ""
	accountID := ""
	displayName := ""
	if meta != nil {
		username = strings.TrimSpace(meta.SenderAccountName)
		accountID = strings.TrimSpace(meta.SenderAccountID)
		displayName = strings.TrimSpace(meta.SenderNick)
	}
	if binding != nil {
		if username == "" {
			username = strings.TrimSpace(binding.RemoteAccount)
		}
		if accountID == "" {
			accountID = strings.TrimSpace(binding.RemoteAccountID)
		}
	}

	switch {
	case username != "" && displayName != "" && accountID != "":
		return fmt.Sprintf("Latest sender identity: username=%q, display_name=%q, account_id=%q. Treat the username as the canonical identity.", username, displayName, accountID)
	case username != "" && displayName != "":
		return fmt.Sprintf("Latest sender identity: username=%q, display_name=%q. Treat the username as the canonical identity.", username, displayName)
	case username != "" && accountID != "":
		return fmt.Sprintf("Latest sender identity: username=%q, account_id=%q. Treat the username as the canonical identity.", username, accountID)
	case username != "":
		return fmt.Sprintf("Latest sender identity: username=%q. Treat the username as the canonical identity.", username)
	case accountID != "":
		return fmt.Sprintf("Latest sender identity: account_id=%q. Use this to avoid confusing participants.", accountID)
	default:
		return "Latest sender identity is unavailable."
	}
}

func (s *ConversationService) latestSolarInboundMetadataForThread(ctx context.Context, accountID, threadID string) (*solarInboundRequestMetadata, error) {
	var records []database.ConversationMessage
	if err := s.db.WithContext(ctx).
		Where("thread_id = ? AND account_id = ? AND role = ?", threadID, accountID, "user").
		Order("sequence DESC").
		Limit(12).
		Find(&records).Error; err != nil {
		return nil, err
	}
	return latestSolarInboundMetadata(records), nil
}

func (s *ConversationService) allowSolarRoomReply(ctx context.Context, thread *database.ConversationThread, binding *database.ExternalChatBinding) (string, error) {
	if thread == nil || binding == nil {
		return solarReplyAllow, nil
	}
	if binding.RemoteRoomType != nil && *binding.RemoteRoomType == 1 {
		return solarReplyAllow, nil
	}

	meta, err := s.latestSolarInboundMetadataForThread(ctx, thread.AccountID, thread.ID)
	if err != nil {
		return solarReplySuppress, err
	}
	if meta != nil && meta.MentionedBot {
		return solarReplyForceAllow, nil
	}
	if solarRoomBindingIsActive(binding, time.Now()) {
		return solarReplyAllow, nil
	}
	return solarReplySuppress, nil
}

func (s *ConversationService) getCachedSolarUserProfile(ctx context.Context, agentID, accountName string) (solar.AccountProfile, error) {
	if s.solar == nil || strings.TrimSpace(accountName) == "" {
		return nil, nil
	}
	cacheKey := agentID + ":" + accountName
	if cached, ok := s.profileCache.Load(cacheKey); ok {
		return cached.(solar.AccountProfile), nil
	}
	profile, err := s.solar.GetAccountProfile(ctx, agentID, accountName)
	if err != nil {
		return nil, err
	}
	if profile != nil {
		s.profileCache.Store(cacheKey, profile)
	}
	return profile, nil
}

func (s *ConversationService) applySolarRoomEngagementState(binding *database.ExternalChatBinding, input ExternalInboundMessage, now time.Time) {
	if binding == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	if binding.RemoteRoomType != nil && *binding.RemoteRoomType == 1 {
		binding.EngagementState = solarRoomEngagementStateActive
		binding.EngagedUntil = nil
		return
	}

	involved := input.MentionedBot
	if involved {
		binding.EngagementState = solarRoomEngagementStateActive
		until := now.Add(solarRoomActiveWindow)
		binding.EngagedUntil = &until
		return
	}
	if solarRoomBindingIsActive(binding, now) {
		binding.EngagementState = solarRoomEngagementStateActive
		return
	}
	binding.EngagementState = solarRoomEngagementStatePassive
	binding.EngagedUntil = nil
}

func (s *ConversationService) applySolarOutboundEngagementState(binding *database.ExternalChatBinding, now time.Time) {
	if binding == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	if binding.RemoteRoomType != nil && *binding.RemoteRoomType == 1 {
		binding.EngagementState = solarRoomEngagementStateActive
		binding.EngagedUntil = nil
		return
	}
	binding.EngagementState = solarRoomEngagementStateActive
	until := now.Add(solarRoomActiveWindow)
	binding.EngagedUntil = &until
}

func solarRoomBindingIsActive(binding *database.ExternalChatBinding, now time.Time) bool {
	if binding == nil {
		return false
	}
	if binding.RemoteRoomType != nil && *binding.RemoteRoomType == 1 {
		return true
	}
	if strings.TrimSpace(binding.EngagementState) != solarRoomEngagementStateActive {
		return false
	}
	if binding.EngagedUntil == nil {
		return false
	}
	return binding.EngagedUntil.After(now)
}

func solarRoomEngagementPrompt(binding *database.ExternalChatBinding) string {
	if binding == nil || binding.RemoteRoomType == nil || *binding.RemoteRoomType == 1 {
		return "No special follow-up engagement window is needed for this room."
	}
	if solarRoomBindingIsActive(binding, time.Now()) {
		return "The bot is currently in an active follow-up window for this group chat because it was recently mentioned. You may continue the conversation proactively even without a fresh mention, but you can still choose to stay silent if the conversation does not require your input."
	}
	return "The bot is not currently in an active follow-up window for this group chat. Do not reply unless the latest message directly mentioned the bot."
}
