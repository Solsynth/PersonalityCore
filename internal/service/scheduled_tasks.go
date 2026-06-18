package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/personality/internal/database"
)

var (
	ErrTaskNotFound = errors.New("scheduled task not found")
)

const createTaskToolName = "create_task"
const listTasksToolName = "list_tasks"
const updateTaskToolName = "update_task"
const deleteTaskToolName = "delete_task"

type CreateTaskInput struct {
	Description  string         `json:"description"`
	ScheduleType string         `json:"schedule_type"` // "once" or "interval"
	RunAt        *time.Time     `json:"run_at"`        // for "once"
	IntervalSecs int            `json:"interval_secs"` // for "interval"
	Payload      map[string]any `json:"payload"`
}

type UpdateTaskInput struct {
	Description  *string        `json:"description"`
	ScheduleType *string        `json:"schedule_type"`
	RunAt        *time.Time     `json:"run_at"`
	IntervalSecs *int           `json:"interval_secs"`
	Enabled      *bool          `json:"enabled"`
	Payload      map[string]any `json:"payload"`
}

// --- Tool info ---

func (s *ConversationService) createTaskToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: createTaskToolName,
		Desc: "Create a scheduled task that runs automatically. Use 'once' for a one-time task at a specific time, or 'interval' for a repeating task.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"description": {
				Type:     schema.String,
				Desc:     "What the agent should do when this task fires. Be specific and actionable.",
				Required: true,
			},
			"schedule_type": {
				Type:     schema.String,
				Desc:     "'once' for a one-time task, 'interval' for repeating.",
				Required: true,
			},
			"run_at": {
				Type: schema.String,
				Desc: "ISO 8601 timestamp for when to run (required for 'once' tasks). Example: 2025-06-18T15:00:00Z",
			},
			"interval_secs": {
				Type: schema.Integer,
				Desc: "Seconds between runs (required for 'interval' tasks). Minimum 60.",
			},
		}),
	}
}

func (s *ConversationService) listTasksToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: listTasksToolName,
		Desc: "List your scheduled tasks. Shows status, schedule, and next run time.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func (s *ConversationService) updateTaskToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: updateTaskToolName,
		Desc: "Update a scheduled task. You can change its description, schedule, or enable/disable it.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"task_id": {
				Type:     schema.String,
				Desc:     "The task ID to update.",
				Required: true,
			},
			"description": {Type: schema.String, Desc: "New description."},
			"enabled":     {Type: schema.Boolean, Desc: "Enable or disable the task."},
			"interval_secs": {Type: schema.Integer, Desc: "New interval in seconds (for interval tasks)."},
			"run_at":      {Type: schema.String, Desc: "New run time (for once tasks)."},
		}),
	}
}

func (s *ConversationService) deleteTaskToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: deleteTaskToolName,
		Desc: "Delete a scheduled task permanently.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"task_id": {
				Type:     schema.String,
				Desc:     "The task ID to delete.",
				Required: true,
			},
		}),
	}
}

// --- Tool execution ---

type createTaskToolInput struct {
	Description  string  `json:"description"`
	ScheduleType string  `json:"schedule_type"`
	RunAt        *string `json:"run_at"`
	IntervalSecs int     `json:"interval_secs"`
}

type updateTaskToolInput struct {
	TaskID       string  `json:"task_id"`
	Description  *string `json:"description"`
	Enabled      *bool   `json:"enabled"`
	IntervalSecs *int    `json:"interval_secs"`
	RunAt        *string `json:"run_at"`
}

type deleteTaskToolInput struct {
	TaskID string `json:"task_id"`
}

func isTaskToolName(name string) bool {
	switch name {
	case createTaskToolName, listTasksToolName, updateTaskToolName, deleteTaskToolName:
		return true
	}
	return false
}

func (s *ConversationService) executeTaskToolCall(ctx context.Context, agentID, accountID string, call schema.ToolCall) (*executedChatToolResult, error) {
	switch call.Function.Name {
	case createTaskToolName:
		return s.executeCreateTaskToolCall(ctx, agentID, accountID, call)
	case listTasksToolName:
		return s.executeListTasksToolCall(ctx, agentID, accountID, call)
	case updateTaskToolName:
		return s.executeUpdateTaskToolCall(ctx, agentID, accountID, call)
	case deleteTaskToolName:
		return s.executeDeleteTaskToolCall(ctx, agentID, accountID, call)
	default:
		return nil, fmt.Errorf("unsupported task tool %q", call.Function.Name)
	}
}

func (s *ConversationService) executeCreateTaskToolCall(ctx context.Context, agentID, accountID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input createTaskToolInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return nil, fmt.Errorf("decode %s: %w", createTaskToolName, err)
	}

	createInput := CreateTaskInput{
		Description:  input.Description,
		ScheduleType: input.ScheduleType,
		IntervalSecs: input.IntervalSecs,
	}
	if input.RunAt != nil {
		t, err := time.Parse(time.RFC3339, *input.RunAt)
		if err != nil {
			return nil, fmt.Errorf("invalid run_at format, use RFC3339: %w", err)
		}
		createInput.RunAt = &t
	}

	task, err := s.CreateTask(ctx, agentID, accountID, createInput)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{
		"ok":         true,
		"task_id":    task.ID,
		"status":     task.Status,
		"next_run_at": task.NextRunAt,
	})
}

func (s *ConversationService) executeListTasksToolCall(ctx context.Context, agentID, accountID string, call schema.ToolCall) (*executedChatToolResult, error) {
	tasks, total, err := s.ListTasks(ctx, agentID, accountID, ListInput{Take: 50, Offset: 0})
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}

	var items []map[string]any
	for _, t := range tasks {
		item := map[string]any{
			"id":            t.ID,
			"description":   t.Description,
			"schedule_type": t.ScheduleType,
			"status":        t.Status,
			"enabled":       t.Enabled,
			"run_count":     t.RunCount,
			"next_run_at":   t.NextRunAt,
		}
		if t.ScheduleType == "interval" {
			item["interval_secs"] = t.IntervalSecs
		}
		items = append(items, item)
	}
	return toolResultJSON(call, map[string]any{"ok": true, "total": total, "tasks": items})
}

func (s *ConversationService) executeUpdateTaskToolCall(ctx context.Context, agentID, accountID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input updateTaskToolInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return nil, fmt.Errorf("decode %s: %w", updateTaskToolName, err)
	}

	if input.TaskID == "" {
		return toolResultJSON(call, map[string]any{"ok": false, "error": "task_id is required"})
	}

	updateInput := UpdateTaskInput{
		Description:  input.Description,
		Enabled:      input.Enabled,
		IntervalSecs: input.IntervalSecs,
	}
	if input.RunAt != nil {
		t, err := time.Parse(time.RFC3339, *input.RunAt)
		if err != nil {
			return toolResultJSON(call, map[string]any{"ok": false, "error": "invalid run_at format, use RFC3339"})
		}
		updateInput.RunAt = &t
	}

	task, err := s.UpdateTask(ctx, agentID, input.TaskID, updateInput)
	if err != nil {
		if errors.Is(err, ErrTaskNotFound) {
			return toolResultJSON(call, map[string]any{"ok": false, "error": "task not found"})
		}
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{
		"ok":         true,
		"task_id":    task.ID,
		"enabled":    task.Enabled,
		"next_run_at": task.NextRunAt,
	})
}

func (s *ConversationService) executeDeleteTaskToolCall(ctx context.Context, agentID, accountID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input deleteTaskToolInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return nil, fmt.Errorf("decode %s: %w", deleteTaskToolName, err)
	}
	if input.TaskID == "" {
		return toolResultJSON(call, map[string]any{"ok": false, "error": "task_id is required"})
	}

	if err := s.DeleteTask(ctx, agentID, input.TaskID); err != nil {
		if errors.Is(err, ErrTaskNotFound) {
			return toolResultJSON(call, map[string]any{"ok": false, "error": "task not found"})
		}
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "deleted": input.TaskID})
}

// --- Service CRUD ---

func (s *ConversationService) CreateTask(ctx context.Context, agentID, accountID string, input CreateTaskInput) (*database.ScheduledTask, error) {
	if input.Description == "" {
		return nil, fmt.Errorf("description is required")
	}
	if input.ScheduleType != "once" && input.ScheduleType != "interval" {
		return nil, fmt.Errorf("schedule_type must be 'once' or 'interval'")
	}

	now := time.Now()
	task := database.ScheduledTask{
		ID:           ulid.Make().String(),
		AgentID:      agentID,
		AccountID:    accountID,
		Description:  input.Description,
		ScheduleType: input.ScheduleType,
		Status:       "pending",
		Enabled:      true,
		RunCount:     0,
	}

	if input.ScheduleType == "once" {
		if input.RunAt == nil {
			return nil, fmt.Errorf("run_at is required for 'once' tasks")
		}
		if input.RunAt.Before(now) {
			return nil, fmt.Errorf("run_at must be in the future")
		}
		task.RunAt = input.RunAt
		task.NextRunAt = input.RunAt
	} else {
		if input.IntervalSecs < 60 {
			return nil, fmt.Errorf("interval_secs must be at least 60")
		}
		task.IntervalSecs = input.IntervalSecs
		nextRun := now.Add(time.Duration(input.IntervalSecs) * time.Second)
		task.NextRunAt = &nextRun
	}

	if input.Payload != nil {
		payloadJSON, err := json.Marshal(input.Payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}
		task.Payload = payloadJSON
	}

	if err := s.db.DB.WithContext(ctx).Create(&task).Error; err != nil {
		return nil, fmt.Errorf("failed to create task: %w", err)
	}
	return &task, nil
}

func (s *ConversationService) ListTasks(ctx context.Context, agentID, accountID string, input ListInput) ([]database.ScheduledTask, int64, error) {
	var tasks []database.ScheduledTask
	var total int64

	q := s.db.DB.WithContext(ctx).
		Where("agent_id = ? AND account_id = ?", agentID, accountID).
		Order("next_run_at ASC NULLS LAST, created_at DESC")

	q.Model(&database.ScheduledTask{}).Count(&total)

	if err := q.Offset(input.Offset).Limit(input.Take).Find(&tasks).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list tasks: %w", err)
	}
	return tasks, total, nil
}

func (s *ConversationService) GetTask(ctx context.Context, agentID, taskID string) (*database.ScheduledTask, error) {
	var task database.ScheduledTask
	err := s.db.DB.WithContext(ctx).
		Where("id = ? AND agent_id = ?", taskID, agentID).
		First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTaskNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	return &task, nil
}

func (s *ConversationService) UpdateTask(ctx context.Context, agentID, taskID string, input UpdateTaskInput) (*database.ScheduledTask, error) {
	task, err := s.GetTask(ctx, agentID, taskID)
	if err != nil {
		return nil, err
	}

	if input.Description != nil {
		task.Description = *input.Description
	}
	if input.Enabled != nil {
		task.Enabled = *input.Enabled
	}
	if input.Payload != nil {
		payloadJSON, err := json.Marshal(input.Payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}
		task.Payload = payloadJSON
	}
	if input.ScheduleType != nil {
		task.ScheduleType = *input.ScheduleType
	}
	if input.RunAt != nil && task.ScheduleType == "once" {
		task.RunAt = input.RunAt
		if task.Status != "completed" {
			task.NextRunAt = input.RunAt
		}
	}
	if input.IntervalSecs != nil && task.ScheduleType == "interval" {
		task.IntervalSecs = *input.IntervalSecs
		nextRun := time.Now().Add(time.Duration(*input.IntervalSecs) * time.Second)
		task.NextRunAt = &nextRun
	}

	if err := s.db.DB.WithContext(ctx).Save(task).Error; err != nil {
		return nil, fmt.Errorf("failed to update task: %w", err)
	}
	return task, nil
}

func (s *ConversationService) DeleteTask(ctx context.Context, agentID, taskID string) error {
	task, err := s.GetTask(ctx, agentID, taskID)
	if err != nil {
		return err
	}
	return s.db.DB.WithContext(ctx).Delete(task).Error
}

// --- Helpers ---

func toolResultJSON(call schema.ToolCall, data map[string]any) (*executedChatToolResult, error) {
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return &executedChatToolResult{
		Content:    string(payload),
		ToolName:   call.Function.Name,
		ToolCallID: call.ID,
	}, nil
}
