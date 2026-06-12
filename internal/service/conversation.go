package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/oklog/ulid/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
	"src.solsynth.dev/sosys/personality/internal/humanize"
	"src.solsynth.dev/sosys/personality/internal/logging"
)

var ErrNotFound = errors.New("not found")
var ErrForbidden = errors.New("forbidden")

type assistantMessageMetadata struct {
	ToolCalls        []schema.ToolCall `json:"tool_calls"`
	ReasoningContent string            `json:"reasoning_content"`
}

type toolMessageMetadata struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
}

type userMessageMetadata struct {
	InputParts []userMessageInputPart `json:"input_parts"`
}

type solarInboundRequestMetadata struct {
	Source            string `json:"source"`
	RoomType          int    `json:"room_type"`
	MentionedBot      bool   `json:"mentioned_bot"`
	SenderAccountID   string `json:"sender_account_id"`
	SenderAccountName string `json:"sender_account_name"`
	SenderNick        string `json:"sender_nick"`
	RepliedMessageID  string `json:"replied_message_id"`
}

type ConversationService struct {
	db           *database.DB
	cfg          *config.Config
	registry     *agent.Registry
	executor     *agent.Executor
	humanize     *humanize.Manager
	solar        SolarChatBridge
	solarInbound *solarInboundBatcher
}

type CreateConversationInput struct {
	AgentID string `json:"agent_id"`
	Title   string `json:"title"`
}

type AddMessageInput struct {
	Content string `json:"content"`
}

type userMessageInputPart struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	ImageURL    string `json:"image_url,omitempty"`
	ImageBase64 string `json:"image_base64,omitempty"`
	MIMEType    string `json:"mime_type,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

type RunInput struct {
	Message         string                 `json:"message"`
	InputParts      []userMessageInputPart `json:"input_parts"`
	Stream          bool                   `json:"stream"`
	RequestMetadata map[string]any         `json:"-"`
}

type ListInput struct {
	Take   int
	Offset int
}

type RunResult struct {
	Thread          *database.ConversationThread  `json:"thread"`
	Run             *database.ConversationRun     `json:"run"`
	RequestMessage  *database.ConversationMessage `json:"request_message"`
	ResponseMessage *database.ConversationMessage `json:"response_message"`
	ResponseContent string                        `json:"content"`
}

func NewConversationService(db *database.DB, cfg *config.Config, registry *agent.Registry, executor *agent.Executor) *ConversationService {
	svc := &ConversationService{
		db:       db,
		cfg:      cfg,
		registry: registry,
		executor: executor,
		humanize: humanize.NewManager(db),
	}
	svc.solarInbound = newSolarInboundBatcher(2*time.Second, svc.handleSolarInboundBatch)
	return svc
}

func (s *ConversationService) ListAgents() []agent.Definition {
	return s.registry.List()
}

func (s *ConversationService) GetAgent(id string) (agent.Definition, bool) {
	return s.registry.Get(id)
}

func (s *ConversationService) CreateConversation(ctx context.Context, accountID string, input CreateConversationInput) (*database.ConversationThread, error) {
	if _, ok := s.registry.Get(input.AgentID); !ok {
		return nil, fmt.Errorf("unknown agent_id")
	}
	thread := &database.ConversationThread{
		ID:        newID(),
		AccountID: accountID,
		AgentID:   strings.TrimSpace(input.AgentID),
		Title:     coalesceTitle(strings.TrimSpace(input.Title), "New conversation"),
	}
	if err := s.db.WithContext(ctx).Create(thread).Error; err != nil {
		return nil, err
	}
	return thread, nil
}

func (s *ConversationService) ListConversations(ctx context.Context, accountID string, input ListInput) ([]database.ConversationThread, int64, error) {
	var total int64
	query := s.db.WithContext(ctx).Model(&database.ConversationThread{}).Where("account_id = ?", accountID)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var items []database.ConversationThread
	if err := query.Order("updated_at DESC").Offset(input.Offset).Limit(input.Take).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *ConversationService) GetConversation(ctx context.Context, accountID, threadID string) (*database.ConversationThread, error) {
	var thread database.ConversationThread
	if err := s.db.WithContext(ctx).Where("id = ?", threadID).First(&thread).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if thread.AccountID != accountID {
		return nil, ErrForbidden
	}
	return &thread, nil
}

func (s *ConversationService) ListMessages(ctx context.Context, accountID, threadID string, input ListInput) ([]database.ConversationMessage, int64, error) {
	if _, err := s.GetConversation(ctx, accountID, threadID); err != nil {
		return nil, 0, err
	}

	var total int64
	query := s.db.WithContext(ctx).Model(&database.ConversationMessage{}).Where("thread_id = ? AND account_id = ?", threadID, accountID)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var items []database.ConversationMessage
	if err := query.Order("sequence ASC").Offset(input.Offset).Limit(input.Take).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *ConversationService) AddUserMessage(ctx context.Context, accountID, threadID string, input AddMessageInput) (*database.ConversationMessage, error) {
	thread, err := s.GetConversation(ctx, accountID, threadID)
	if err != nil {
		return nil, err
	}
	return s.createMessage(ctx, thread, nil, "user", strings.TrimSpace(input.Content), nil)
}

func (s *ConversationService) ListRuns(ctx context.Context, accountID, threadID string, input ListInput) ([]database.ConversationRun, int64, error) {
	if _, err := s.GetConversation(ctx, accountID, threadID); err != nil {
		return nil, 0, err
	}

	var total int64
	query := s.db.WithContext(ctx).Model(&database.ConversationRun{}).Where("thread_id = ? AND account_id = ?", threadID, accountID)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var items []database.ConversationRun
	if err := query.Order("created_at DESC").Offset(input.Offset).Limit(input.Take).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *ConversationService) GetRun(ctx context.Context, accountID, threadID, runID string) (*database.ConversationRun, error) {
	if _, err := s.GetConversation(ctx, accountID, threadID); err != nil {
		return nil, err
	}

	var run database.ConversationRun
	if err := s.db.WithContext(ctx).Where("id = ? AND thread_id = ? AND account_id = ?", runID, threadID, accountID).First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &run, nil
}

func (s *ConversationService) CreateRun(ctx context.Context, accountID, threadID string, input RunInput) (*database.ConversationThread, *database.ConversationRun, *database.ConversationMessage, error) {
	thread, err := s.GetConversation(ctx, accountID, threadID)
	if err != nil {
		return nil, nil, nil, err
	}
	content, metadata, err := input.userMessagePayload(input.RequestMetadata)
	if err != nil {
		return nil, nil, nil, err
	}

	requestMessage, err := s.createMessageWithMetadata(ctx, thread, nil, "user", content, nil, metadata)
	if err != nil {
		return nil, nil, nil, err
	}

	now := time.Now()
	settings, _ := json.Marshal(map[string]any{"stream": input.Stream})
	run := &database.ConversationRun{
		ID:               newID(),
		ThreadID:         thread.ID,
		AccountID:        accountID,
		AgentID:          thread.AgentID,
		Status:           "running",
		Model:            "",
		RequestMessageID: requestMessage.ID,
		Stream:           input.Stream,
		Settings:         settings,
		Usage:            datatypes.JSON([]byte("{}")),
		StartedAt:        now,
	}
	if err := s.db.WithContext(ctx).Create(run).Error; err != nil {
		return nil, nil, nil, err
	}
	logging.Log.Info().
		Str("conversation_id", thread.ID).
		Str("run_id", run.ID).
		Str("agent_id", thread.AgentID).
		Str("account_id", accountID).
		Bool("stream", input.Stream).
		Int("prompt_chars", len(content)).
		Int("input_part_count", len(input.InputParts)).
		Msg("run created")
	return thread, run, requestMessage, nil
}

func (s *ConversationService) BuildModelMessages(ctx context.Context, accountID, threadID string) ([]*schema.Message, agent.Definition, error) {
	thread, err := s.GetConversation(ctx, accountID, threadID)
	if err != nil {
		return nil, agent.Definition{}, err
	}
	def, ok := s.registry.Get(thread.AgentID)
	if !ok {
		return nil, agent.Definition{}, fmt.Errorf("agent %q is unavailable", thread.AgentID)
	}
	logging.Log.Debug().
		Str("conversation_id", threadID).
		Str("agent_id", def.ID).
		Str("model", def.Model).
		Msg("building model messages")

	limit := s.cfg.Personality.MaxHistoryMessages
	if limit < 1 {
		limit = 24
	}
	if err := s.ensureThreadContextCompaction(ctx, thread, limit); err != nil {
		return nil, agent.Definition{}, err
	}

	var records []database.ConversationMessage
	if err := s.db.WithContext(ctx).
		Where("thread_id = ? AND account_id = ?", threadID, accountID).
		Order("sequence DESC").
		Limit(limit).
		Find(&records).Error; err != nil {
		return nil, agent.Definition{}, err
	}

	messages := make([]*schema.Message, 0, len(records)+1)
	supportsVision := s.supportsVisionForAgent(def)
	if strings.TrimSpace(def.SystemPrompt) != "" {
		messages = append(messages, schema.SystemMessage(def.SystemPrompt))
	}
	messages = append(messages, schema.SystemMessage(renderCharacterConsistencyOverlay()))
	if strings.TrimSpace(thread.ContextSummary) != "" {
		messages = append(messages, schema.SystemMessage("Earlier compacted thread context:\n\n"+strings.TrimSpace(thread.ContextSummary)))
	}
	if agent.HasAbility(def, "chat") {
		overlay, err := s.buildSolarSystemOverlay(ctx, def.ID, threadID, records)
		if err != nil {
			return nil, agent.Definition{}, err
		}
		if strings.TrimSpace(overlay) != "" {
			messages = append(messages, schema.SystemMessage(overlay))
		}
	}
	if s.humanize != nil {
		identityOverlay, err := s.humanize.BuildAgentIdentityOverlay(ctx, def.ID)
		if err != nil {
			return nil, agent.Definition{}, err
		}
		if strings.TrimSpace(identityOverlay) != "" {
			logging.Log.Debug().
				Str("conversation_id", threadID).
				Str("agent_id", def.ID).
				Int("overlay_chars", len(identityOverlay)).
				Msg("attached agent identity overlay")
			messages = append(messages, schema.SystemMessage(identityOverlay))
		}
		state, err := s.humanize.BuildPromptState(ctx, s.resolveImpressionAccountID(accountID, records), accountID, threadID, def)
		if err != nil {
			return nil, agent.Definition{}, err
		}
		if overlay := humanize.RenderSystemOverlay(def, state); strings.TrimSpace(overlay) != "" {
			logging.Log.Debug().
				Str("conversation_id", threadID).
				Str("agent_id", def.ID).
				Int("overlay_chars", len(overlay)).
				Msg("attached humanizer overlay")
			messages = append(messages, schema.SystemMessage(overlay))
		}
	}

	pendingToolCalls := make(map[string]struct{})
	for i := len(records) - 1; i >= 0; i-- {
		record := records[i]
		role := schema.User
		switch strings.ToLower(record.Role) {
		case "assistant":
			role = schema.Assistant
		case "system":
			role = schema.System
		case "tool":
			role = schema.Tool
		}
		msg := &schema.Message{Role: role, Content: renderMessageContextContent(record.Content, record.CreatedAt)}
		switch role {
		case schema.User:
			var meta userMessageMetadata
			if decodeMessageMetadata(record.Metadata, &meta) == nil && len(meta.InputParts) > 0 {
				if !supportsVision {
					msg.Content = renderTextOnlyMessageInputParts(meta.InputParts, msg.Content)
					break
				}
				parts, err := buildSchemaMessageInputParts(meta.InputParts, renderMessageContextContent(record.Content, record.CreatedAt))
				if err != nil {
					return nil, agent.Definition{}, err
				}
				msg.Content = ""
				msg.UserInputMultiContent = parts
			}
		case schema.Assistant:
			var meta assistantMessageMetadata
			if decodeMessageMetadata(record.Metadata, &meta) == nil {
				msg.ToolCalls = meta.ToolCalls
				msg.ReasoningContent = meta.ReasoningContent
				for _, call := range meta.ToolCalls {
					if strings.TrimSpace(call.ID) == "" {
						continue
					}
					pendingToolCalls[strings.TrimSpace(call.ID)] = struct{}{}
				}
			}
			if strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 {
				logging.Log.Debug().
					Str("conversation_id", threadID).
					Str("message_id", record.ID).
					Msg("skipping empty persisted assistant message")
				continue
			}
		case schema.Tool:
			var meta toolMessageMetadata
			if decodeMessageMetadata(record.Metadata, &meta) == nil {
				msg.ToolCallID = meta.ToolCallID
				msg.ToolName = meta.ToolName
			}
			if strings.TrimSpace(msg.ToolCallID) == "" {
				logging.Log.Debug().
					Str("conversation_id", threadID).
					Str("message_id", record.ID).
					Str("tool_name", msg.ToolName).
					Msg("skipping persisted tool message without tool_call_id")
				continue
			}
			if _, ok := pendingToolCalls[strings.TrimSpace(msg.ToolCallID)]; !ok {
				logging.Log.Debug().
					Str("conversation_id", threadID).
					Str("message_id", record.ID).
					Str("tool_name", msg.ToolName).
					Str("tool_call_id", msg.ToolCallID).
					Msg("skipping persisted orphaned tool message without matching assistant tool call")
				continue
			}
			delete(pendingToolCalls, strings.TrimSpace(msg.ToolCallID))
		}
		messages = append(messages, msg)
	}
	messages = append(messages, schema.SystemMessage(renderCurrentDateTimeContext(time.Now())))
	return messages, def, nil
}

func (s *ConversationService) CompleteRun(ctx context.Context, run *database.ConversationRun, assistantContent string) (*database.ConversationMessage, error) {
	thread, err := s.GetConversation(ctx, run.AccountID, run.ThreadID)
	if err != nil {
		return nil, err
	}

	responseMessage, err := s.createMessage(ctx, thread, &run.ID, "assistant", strings.TrimSpace(assistantContent), stringPtr(run.Model))
	if err != nil {
		return nil, err
	}

	now := time.Now()
	run.Status = "completed"
	run.ResponseMessageID = &responseMessage.ID
	run.CompletedAt = &now
	logging.Log.Info().
		Str("conversation_id", run.ThreadID).
		Str("run_id", run.ID).
		Str("agent_id", run.AgentID).
		Str("model", run.Model).
		Int("response_chars", len(strings.TrimSpace(assistantContent))).
		Msg("run completed")
	return responseMessage, s.db.WithContext(ctx).Save(run).Error
}

func (s *ConversationService) FailRun(ctx context.Context, run *database.ConversationRun, failure error) error {
	now := time.Now()
	message := failure.Error()
	run.Status = "failed"
	run.Error = &message
	run.CompletedAt = &now
	logging.Log.Error().
		Err(failure).
		Str("conversation_id", run.ThreadID).
		Str("run_id", run.ID).
		Str("agent_id", run.AgentID).
		Str("model", run.Model).
		Msg("run failed")
	return s.db.WithContext(ctx).Save(run).Error
}

func (s *ConversationService) ExecuteRun(ctx context.Context, accountID, threadID string, input RunInput) (*RunResult, error) {
	thread, run, requestMessage, err := s.CreateRun(ctx, accountID, threadID, input)
	if err != nil {
		return nil, err
	}
	logging.Log.Info().
		Str("conversation_id", threadID).
		Str("run_id", run.ID).
		Str("agent_id", thread.AgentID).
		Msg("starting non-streaming generation")

	modelMessages, agentDef, err := s.BuildModelMessages(ctx, accountID, threadID)
	if err != nil {
		_ = s.FailRun(ctx, run, err)
		return nil, err
	}

	run.Model = agentDef.Model
	logging.Log.Debug().
		Str("conversation_id", threadID).
		Str("run_id", run.ID).
		Str("agent_id", agentDef.ID).
		Str("model", run.Model).
		Int("message_count", len(modelMessages)).
		Msg("invoking model")
	responseContent := ""
	if agent.HasAbility(agentDef, "chat") && s.solar != nil {
		logging.Log.Info().
			Str("conversation_id", threadID).
			Str("run_id", run.ID).
			Str("agent_id", agentDef.ID).
			Msg("routing run through chat tool execution path")
		responseContent, err = s.runWithChatTools(ctx, accountID, threadID, run.ID, modelMessages, agentDef)
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
		if err := s.humanize.ObserveInteraction(ctx, s.resolveImpressionAccountIDFromRecord(accountID, requestMessage), agentDef, requestMessage.Content, responseContent); err != nil {
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

type StreamCallbacks struct {
	OnChunk func(string) error
}

func (s *ConversationService) StreamRun(ctx context.Context, accountID, threadID string, input RunInput, callbacks StreamCallbacks) (*RunResult, error) {
	thread, run, requestMessage, err := s.CreateRun(ctx, accountID, threadID, input)
	if err != nil {
		return nil, err
	}
	logging.Log.Info().
		Str("conversation_id", threadID).
		Str("run_id", run.ID).
		Str("agent_id", thread.AgentID).
		Msg("starting streaming generation")

	modelMessages, agentDef, err := s.BuildModelMessages(ctx, accountID, threadID)
	if err != nil {
		_ = s.FailRun(ctx, run, err)
		return nil, err
	}

	run.Model = agentDef.Model
	logging.Log.Debug().
		Str("conversation_id", threadID).
		Str("run_id", run.ID).
		Str("agent_id", agentDef.ID).
		Str("model", run.Model).
		Int("message_count", len(modelMessages)).
		Msg("opening model stream")
	var builder strings.Builder
	chunkCount := 0
	if agent.HasAbility(agentDef, "chat") && s.solar != nil {
		logging.Log.Info().
			Str("conversation_id", threadID).
			Str("run_id", run.ID).
			Str("agent_id", agentDef.ID).
			Msg("routing streaming run through direct solar chat response path")
		stream, err := s.executor.Stream(ctx, agent.RunRequest{Agent: agentDef, Messages: modelMessages})
		if err != nil {
			_ = s.FailRun(ctx, run, err)
			return nil, err
		}
		defer stream.Close()

		binding, err := s.getSolarRoomBinding(ctx, agentDef.ID, threadID)
		if err != nil {
			_ = s.FailRun(ctx, run, err)
			return nil, err
		}
		allowOutboundReply, err := s.allowSolarRoomReply(ctx, thread, binding)
		if err != nil {
			_ = s.FailRun(ctx, run, err)
			return nil, err
		}
		var outboundSender *solarOutboundStreamSender
		if binding != nil && allowOutboundReply {
			outboundSender = newSolarOutboundStreamSender(s, thread, binding, agentDef.ID, run.ID)
		}

		for {
			chunk, recvErr := stream.Recv()
			if recvErr != nil {
				if recvErr == io.EOF {
					break
				}
				_ = s.FailRun(ctx, run, recvErr)
				return nil, recvErr
			}
			if chunk == nil || chunk.Content == "" {
				continue
			}
			chunkCount++
			builder.WriteString(chunk.Content)
			if callbacks.OnChunk != nil {
				if err := callbacks.OnChunk(chunk.Content); err != nil {
					_ = s.FailRun(ctx, run, err)
					return nil, err
				}
			}
			if outboundSender != nil {
				if err := outboundSender.Push(ctx, chunk.Content); err != nil {
					_ = s.FailRun(ctx, run, err)
					return nil, err
				}
			}
		}
		if outboundSender != nil {
			if err := outboundSender.Flush(ctx); err != nil {
				_ = s.FailRun(ctx, run, err)
				return nil, err
			}
		}
		if binding != nil && !allowOutboundReply {
			builder.Reset()
		}
	} else {
		stream, err := s.executor.Stream(ctx, agent.RunRequest{Agent: agentDef, Messages: modelMessages})
		if err != nil {
			_ = s.FailRun(ctx, run, err)
			return nil, err
		}
		defer stream.Close()

		for {
			chunk, recvErr := stream.Recv()
			if recvErr != nil {
				if recvErr == io.EOF {
					break
				}
				_ = s.FailRun(ctx, run, recvErr)
				return nil, recvErr
			}
			if chunk == nil || chunk.Content == "" {
				continue
			}
			chunkCount++
			builder.WriteString(chunk.Content)
			if callbacks.OnChunk != nil {
				if err := callbacks.OnChunk(chunk.Content); err != nil {
					_ = s.FailRun(ctx, run, err)
					return nil, err
				}
			}
		}
	}

	responseMessage, err := s.CompleteRun(ctx, run, builder.String())
	if err != nil {
		return nil, err
	}
	if s.humanize != nil {
		if err := s.humanize.ObserveInteraction(ctx, s.resolveImpressionAccountIDFromRecord(accountID, requestMessage), agentDef, requestMessage.Content, builder.String()); err != nil {
			return nil, err
		}
	}
	logging.Log.Debug().
		Str("conversation_id", threadID).
		Str("run_id", run.ID).
		Int("chunk_count", chunkCount).
		Int("response_chars", len(builder.String())).
		Msg("stream generation finished")

	return &RunResult{
		Thread:          thread,
		Run:             run,
		RequestMessage:  requestMessage,
		ResponseMessage: responseMessage,
		ResponseContent: builder.String(),
	}, nil
}

func (s *ConversationService) createMessage(ctx context.Context, thread *database.ConversationThread, runID *string, role, content string, model *string) (*database.ConversationMessage, error) {
	return s.createMessageWithMetadata(ctx, thread, runID, role, content, model, nil)
}

func (s *ConversationService) createMessageWithMetadata(
	ctx context.Context,
	thread *database.ConversationThread,
	runID *string,
	role, content string,
	model *string,
	metadata map[string]any,
) (*database.ConversationMessage, error) {
	sequence, err := s.nextSequence(ctx, thread.ID)
	if err != nil {
		return nil, err
	}

	rawMetadata := datatypes.JSON([]byte("{}"))
	if len(metadata) > 0 {
		payload, err := json.Marshal(metadata)
		if err != nil {
			return nil, err
		}
		rawMetadata = payload
	}

	message := &database.ConversationMessage{
		ID:        newID(),
		ThreadID:  thread.ID,
		RunID:     runID,
		AccountID: thread.AccountID,
		Role:      role,
		Content:   content,
		Sequence:  sequence,
		Model:     model,
		Metadata:  rawMetadata,
	}
	if err := s.db.WithContext(ctx).Create(message).Error; err != nil {
		return nil, err
	}

	now := time.Now()
	thread.LastMessageAt = &now
	if strings.TrimSpace(thread.Title) == "" || thread.Title == "New conversation" {
		thread.Title = deriveTitle(content)
	}
	if err := s.db.WithContext(ctx).Save(thread).Error; err != nil {
		return nil, err
	}
	return message, nil
}

func (s *ConversationService) nextSequence(ctx context.Context, threadID string) (int64, error) {
	var latest database.ConversationMessage
	if err := s.db.WithContext(ctx).Where("thread_id = ?", threadID).Order("sequence DESC").First(&latest).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 1, nil
		}
		return 0, err
	}
	return latest.Sequence + 1, nil
}

func newID() string {
	return ulid.Make().String()
}

func deriveTitle(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return "New conversation"
	}
	runes := []rune(content)
	if len(runes) > 48 {
		return string(runes[:48])
	}
	return content
}

func coalesceTitle(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func stringPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func decodeMessageMetadata(raw datatypes.JSON, out any) error {
	if len(raw) == 0 {
		return nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (s *ConversationService) resolveImpressionAccountID(fallbackAccountID string, records []database.ConversationMessage) string {
	for i := len(records) - 1; i >= 0; i-- {
		if resolved := resolveImpressionAccountIDFromMetadata(fallbackAccountID, records[i].Metadata); resolved != "" {
			return resolved
		}
	}
	return strings.TrimSpace(fallbackAccountID)
}

func (s *ConversationService) resolveImpressionAccountIDFromRecord(fallbackAccountID string, message *database.ConversationMessage) string {
	if message == nil {
		return strings.TrimSpace(fallbackAccountID)
	}
	return resolveImpressionAccountIDFromMetadata(fallbackAccountID, message.Metadata)
}

func resolveImpressionAccountIDFromMetadata(fallbackAccountID string, raw datatypes.JSON) string {
	var meta solarInboundRequestMetadata
	if decodeMessageMetadata(raw, &meta) == nil {
		if senderAccountID := strings.TrimSpace(meta.SenderAccountID); senderAccountID != "" {
			return senderAccountID
		}
	}
	return strings.TrimSpace(fallbackAccountID)
}

func (input RunInput) userMessagePayload(baseMetadata map[string]any) (string, map[string]any, error) {
	content := strings.TrimSpace(input.Message)
	if content == "" && len(input.InputParts) == 0 {
		return "", nil, fmt.Errorf("message or input_parts is required")
	}

	parts, err := buildSchemaMessageInputParts(input.InputParts, content)
	if err != nil {
		return "", nil, err
	}

	metadata := cloneMetadataMap(baseMetadata)
	if len(parts) == 0 {
		return content, metadata, nil
	}
	metadata["input_parts"] = input.InputParts
	return content, metadata, nil
}

func buildSchemaMessageInputParts(rawParts []userMessageInputPart, message string) ([]schema.MessageInputPart, error) {
	parts := make([]schema.MessageInputPart, 0, len(rawParts)+1)
	if text := strings.TrimSpace(message); text != "" {
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeText,
			Text: text,
		})
	}

	for idx, part := range rawParts {
		switch strings.ToLower(strings.TrimSpace(part.Type)) {
		case "text":
			text := strings.TrimSpace(part.Text)
			if text == "" {
				return nil, fmt.Errorf("input_parts[%d].text is required", idx)
			}
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: text,
			})
		case "image", "image_url":
			image, err := buildSchemaMessageInputImage(part, idx)
			if err != nil {
				return nil, err
			}
			parts = append(parts, schema.MessageInputPart{
				Type:  schema.ChatMessagePartTypeImageURL,
				Image: image,
			})
		default:
			return nil, fmt.Errorf("input_parts[%d].type %q is unsupported", idx, part.Type)
		}
	}

	return parts, nil
}

func buildSchemaMessageInputImage(part userMessageInputPart, idx int) (*schema.MessageInputImage, error) {
	image := &schema.MessageInputImage{
		Detail: schema.ImageURLDetailAuto,
	}
	url := strings.TrimSpace(part.ImageURL)
	base64Data := strings.TrimSpace(part.ImageBase64)
	switch {
	case url != "" && base64Data != "":
		return nil, fmt.Errorf("input_parts[%d] cannot set both image_url and image_base64", idx)
	case url != "":
		image.URL = &url
	case base64Data != "":
		image.Base64Data = &base64Data
		image.MIMEType = strings.TrimSpace(part.MIMEType)
		if image.MIMEType == "" {
			return nil, fmt.Errorf("input_parts[%d].mime_type is required when image_base64 is set", idx)
		}
	default:
		return nil, fmt.Errorf("input_parts[%d] requires image_url or image_base64", idx)
	}

	if detail := strings.ToLower(strings.TrimSpace(part.Detail)); detail != "" {
		switch detail {
		case string(schema.ImageURLDetailAuto):
			image.Detail = schema.ImageURLDetailAuto
		case string(schema.ImageURLDetailLow):
			image.Detail = schema.ImageURLDetailLow
		case string(schema.ImageURLDetailHigh):
			image.Detail = schema.ImageURLDetailHigh
		default:
			return nil, fmt.Errorf("input_parts[%d].detail %q is unsupported", idx, part.Detail)
		}
	}
	return image, nil
}

func cloneMetadataMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (s *ConversationService) supportsVisionForAgent(def agent.Definition) bool {
	if s == nil || s.executor == nil {
		return true
	}
	return s.executor.SupportsVision(def)
}

func renderMessageContextContent(content string, createdAt time.Time) string {
	timestamp := "Sent at: " + createdAt.In(time.Local).Format("2006-01-02 15:04:05 -07:00 MST")
	content = strings.TrimSpace(content)
	if content == "" {
		return timestamp
	}
	return timestamp + "\n\n" + content
}

func renderCurrentDateTimeContext(now time.Time) string {
	return "Current date and time: " + now.In(time.Local).Format("2006-01-02 15:04:05 -07:00 MST")
}

func renderCharacterConsistencyOverlay() string {
	return strings.Join([]string{
		"Stay in character for the entire conversation.",
		"Do not describe yourself as an AI, assistant, language model, system prompt, or out-of-character narrator unless the existing role definition explicitly requires that framing.",
		"Respond in your character's own tone, identity, and perspective.",
		"Many different people may talk to you. Distinguish them carefully by sender identity, account, room context, and remembered history instead of blending them together.",
	}, "\n")
}

func renderTextOnlyMessageInputParts(parts []userMessageInputPart, baseContent string) string {
	lines := make([]string, 0, len(parts)+1)
	if trimmed := strings.TrimSpace(baseContent); trimmed != "" {
		lines = append(lines, trimmed)
	}
	for _, part := range parts {
		switch strings.ToLower(strings.TrimSpace(part.Type)) {
		case "text":
			if text := strings.TrimSpace(part.Text); text != "" {
				lines = append(lines, text)
			}
		case "image", "image_url":
			lines = append(lines, renderTextOnlyImagePart(part))
		}
	}
	return strings.Join(lines, "\n\n")
}

func renderTextOnlyImagePart(part userMessageInputPart) string {
	if url := strings.TrimSpace(part.ImageURL); url != "" {
		return "[Image attachment provided but this model only accepts text input. Image URL: " + url + "]"
	}
	if mimeType := strings.TrimSpace(part.MIMEType); mimeType != "" {
		return "[Image attachment provided but this model only accepts text input. MIME type: " + mimeType + "]"
	}
	return "[Image attachment provided but this model only accepts text input.]"
}

func (s *ConversationService) ensureThreadContextCompaction(ctx context.Context, thread *database.ConversationThread, historyLimit int) error {
	if s == nil || s.db == nil || thread == nil {
		return nil
	}
	if historyLimit < 1 {
		historyLimit = 24
	}

	var total int64
	if err := s.db.WithContext(ctx).
		Model(&database.ConversationMessage{}).
		Where("thread_id = ? AND account_id = ?", thread.ID, thread.AccountID).
		Count(&total).Error; err != nil {
		return err
	}
	if total <= int64(historyLimit) {
		return nil
	}

	var latest database.ConversationMessage
	if err := s.db.WithContext(ctx).
		Where("thread_id = ? AND account_id = ?", thread.ID, thread.AccountID).
		Order("sequence DESC").
		First(&latest).Error; err != nil {
		return err
	}

	cutoffSeq := latest.Sequence - int64(historyLimit)
	if cutoffSeq <= thread.SummarySeq {
		return nil
	}

	var records []database.ConversationMessage
	if err := s.db.WithContext(ctx).
		Where("thread_id = ? AND account_id = ? AND sequence > ? AND sequence <= ?", thread.ID, thread.AccountID, thread.SummarySeq, cutoffSeq).
		Order("sequence ASC").
		Find(&records).Error; err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}

	snippets := make([]string, 0, len(records))
	for _, record := range records {
		if snippet := compactConversationRecord(record); snippet != "" {
			snippets = append(snippets, snippet)
		}
	}
	if len(snippets) == 0 {
		thread.SummarySeq = cutoffSeq
		now := time.Now()
		thread.SummaryAt = &now
		return s.db.WithContext(ctx).Save(thread).Error
	}

	summary := strings.TrimSpace(thread.ContextSummary)
	if summary != "" {
		summary += "\n"
	}
	summary += strings.Join(snippets, "\n")
	thread.ContextSummary = trimCompactedSummary(summary, 5000)
	thread.SummarySeq = cutoffSeq
	now := time.Now()
	thread.SummaryAt = &now
	return s.db.WithContext(ctx).Save(thread).Error
}

func compactConversationRecord(record database.ConversationMessage) string {
	role := strings.TrimSpace(strings.ToLower(record.Role))
	switch role {
	case "system":
		return ""
	case "tool":
		role = "tool"
	case "assistant", "user":
	default:
		role = "message"
	}
	return "- " + role + " at " + record.CreatedAt.In(time.Local).Format("2006-01-02 15:04:05 -07:00 MST") + ": " + compactTextForSummary(record.Content, 160)
}

func compactTextForSummary(content string, limit int) string {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\n", " "))
	if content == "" {
		return "(empty)"
	}
	runes := []rune(content)
	if len(runes) > limit {
		return string(runes[:limit]) + "..."
	}
	return content
}

func trimCompactedSummary(summary string, limit int) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	runes := []rune(summary)
	if len(runes) <= limit {
		return summary
	}
	return "..." + string(runes[len(runes)-limit:])
}
