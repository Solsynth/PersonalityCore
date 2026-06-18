package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"src.solsynth.dev/sosys/personality/internal/database"
)

type TaskScheduler struct {
	db             *database.DB
	conversations  *ConversationService
	checkInterval  time.Duration
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
}

func NewTaskScheduler(db *database.DB, conversations *ConversationService, checkInterval time.Duration) *TaskScheduler {
	if checkInterval <= 0 {
		checkInterval = 30 * time.Second
	}
	return &TaskScheduler{
		db:            db,
		conversations: conversations,
		checkInterval: checkInterval,
	}
}

func (ts *TaskScheduler) Start(ctx context.Context) {
	ts.ctx, ts.cancel = context.WithCancel(ctx)
	ts.wg.Add(1)
	go ts.run()
	slog.Info("task scheduler started", "interval", ts.checkInterval)
}

func (ts *TaskScheduler) Stop() {
	if ts.cancel != nil {
		ts.cancel()
	}
	ts.wg.Wait()
	slog.Info("task scheduler stopped")
}

func (ts *TaskScheduler) run() {
	defer ts.wg.Done()
	ticker := time.NewTicker(ts.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ts.ctx.Done():
			return
		case <-ticker.C:
			ts.tick()
		}
	}
}

func (ts *TaskScheduler) tick() {
	ctx, cancel := context.WithTimeout(ts.ctx, 5*time.Minute)
	defer cancel()

	var tasks []database.ScheduledTask
	now := time.Now()
	err := ts.db.DB.WithContext(ctx).
		Where("enabled = ? AND next_run_at <= ? AND status = ?", true, now, "pending").
		Order("next_run_at ASC").
		Limit(20).
		Find(&tasks).Error
	if err != nil {
		slog.Error("failed to fetch due tasks", "err", err)
		return
	}

	for i := range tasks {
		ts.executeTask(ctx, &tasks[i])
	}
}

func (ts *TaskScheduler) executeTask(ctx context.Context, task *database.ScheduledTask) {
	log := slog.With("task_id", task.ID, "agent_id", task.AgentID, "account_id", task.AccountID)
	log.Info("executing scheduled task", "description", task.Description)

	err := ts.conversations.ExecuteScheduledTask(ctx, task)

	now := time.Now()
	task.LastRunAt = &now
	task.RunCount++

	if err != nil {
		errStr := err.Error()
		task.LastError = &errStr
		if task.ScheduleType == "once" {
			task.Status = "failed"
			task.Enabled = false
			task.NextRunAt = nil
		}
		log.Error("scheduled task execution failed", "err", err)
	} else {
		task.LastError = nil
		if task.ScheduleType == "once" {
			task.Status = "completed"
			task.Enabled = false
			task.NextRunAt = nil
		} else {
			next := now.Add(time.Duration(task.IntervalSecs) * time.Second)
			task.NextRunAt = &next
		}
		log.Info("scheduled task completed")
	}

	if err := ts.db.DB.WithContext(ctx).Save(task).Error; err != nil {
		log.Error("failed to update task after execution", "err", err)
	}
}

// ExecuteScheduledTask creates an autonomous run for the scheduled task.
func (s *ConversationService) ExecuteScheduledTask(ctx context.Context, task *database.ScheduledTask) error {
	prompt := fmt.Sprintf("[Scheduled Task]\n%s", task.Description)

	_, err := s.TriggerAutonomousRun(ctx, task.AgentID, AutonomousRunInput{
		TargetAccountID: task.AccountID,
		Prompt:          prompt,
		Trigger:         "scheduled_task",
	})
	return err
}
