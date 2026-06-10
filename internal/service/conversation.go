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
)

var ErrNotFound = errors.New("not found")
var ErrForbidden = errors.New("forbidden")

type ConversationService struct {
	db       *database.DB
	cfg      *config.Config
	registry *agent.Registry
	executor *agent.Executor
}

type CreateConversationInput struct {
	AgentID string `json:"agent_id"`
	Title   string `json:"title"`
}

type AddMessageInput struct {
	Content string `json:"content"`
}

type RunInput struct {
	Message string `json:"message"`
	Stream  bool   `json:"stream"`
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
	return &ConversationService{db: db, cfg: cfg, registry: registry, executor: executor}
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
	content := strings.TrimSpace(input.Message)
	if content == "" {
		return nil, nil, nil, fmt.Errorf("message is required")
	}

	requestMessage, err := s.createMessage(ctx, thread, nil, "user", content, nil)
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

	limit := s.cfg.Personality.MaxHistoryMessages
	if limit < 1 {
		limit = 24
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
	if strings.TrimSpace(def.SystemPrompt) != "" {
		messages = append(messages, schema.SystemMessage(def.SystemPrompt))
	}

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
		messages = append(messages, &schema.Message{Role: role, Content: record.Content})
	}
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
	return responseMessage, s.db.WithContext(ctx).Save(run).Error
}

func (s *ConversationService) FailRun(ctx context.Context, run *database.ConversationRun, failure error) error {
	now := time.Now()
	message := failure.Error()
	run.Status = "failed"
	run.Error = &message
	run.CompletedAt = &now
	return s.db.WithContext(ctx).Save(run).Error
}

func (s *ConversationService) ExecuteRun(ctx context.Context, accountID, threadID string, input RunInput) (*RunResult, error) {
	thread, run, requestMessage, err := s.CreateRun(ctx, accountID, threadID, input)
	if err != nil {
		return nil, err
	}

	modelMessages, agentDef, err := s.BuildModelMessages(ctx, accountID, threadID)
	if err != nil {
		_ = s.FailRun(ctx, run, err)
		return nil, err
	}

	run.Model = agentDef.Model
	response, err := s.executor.Generate(ctx, agent.RunRequest{Agent: agentDef, Messages: modelMessages})
	if err != nil {
		_ = s.FailRun(ctx, run, err)
		return nil, err
	}

	responseMessage, err := s.CompleteRun(ctx, run, response.Content)
	if err != nil {
		return nil, err
	}

	return &RunResult{
		Thread:          thread,
		Run:             run,
		RequestMessage:  requestMessage,
		ResponseMessage: responseMessage,
		ResponseContent: response.Content,
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

	modelMessages, agentDef, err := s.BuildModelMessages(ctx, accountID, threadID)
	if err != nil {
		_ = s.FailRun(ctx, run, err)
		return nil, err
	}

	run.Model = agentDef.Model
	stream, err := s.executor.Stream(ctx, agent.RunRequest{Agent: agentDef, Messages: modelMessages})
	if err != nil {
		_ = s.FailRun(ctx, run, err)
		return nil, err
	}
	defer stream.Close()

	var builder strings.Builder
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
		builder.WriteString(chunk.Content)
		if callbacks.OnChunk != nil {
			if err := callbacks.OnChunk(chunk.Content); err != nil {
				_ = s.FailRun(ctx, run, err)
				return nil, err
			}
		}
	}

	responseMessage, err := s.CompleteRun(ctx, run, builder.String())
	if err != nil {
		return nil, err
	}

	return &RunResult{
		Thread:          thread,
		Run:             run,
		RequestMessage:  requestMessage,
		ResponseMessage: responseMessage,
		ResponseContent: builder.String(),
	}, nil
}

func (s *ConversationService) createMessage(ctx context.Context, thread *database.ConversationThread, runID *string, role, content string, model *string) (*database.ConversationMessage, error) {
	sequence, err := s.nextSequence(ctx, thread.ID)
	if err != nil {
		return nil, err
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
		Metadata:  datatypes.JSON([]byte("{}")),
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
