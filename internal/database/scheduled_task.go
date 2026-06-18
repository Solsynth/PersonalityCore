package database

import (
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type ScheduledTask struct {
	ID             string         `gorm:"primaryKey;size:26" json:"id"`
	AgentID        string         `gorm:"size:64;index:idx_scheduled_tasks_agent_deleted,priority:1" json:"agent_id"`
	AccountID      string         `gorm:"size:128;index:idx_scheduled_tasks_agent_deleted,priority:2" json:"account_id"`
	Description    string         `gorm:"type:text" json:"description"`
	ScheduleType   string         `gorm:"size:16" json:"schedule_type"` // "once" or "interval"
	RunAt          *time.Time     `json:"run_at"`                      // for "once" tasks
	IntervalSecs   int            `json:"interval_secs"`               // for "interval" tasks
	Payload        datatypes.JSON `gorm:"type:jsonb" json:"payload"`
	Status         string         `gorm:"size:32;default:pending" json:"status"` // pending, completed, failed, cancelled
	Enabled        bool           `gorm:"default:true" json:"enabled"`
	LastRunAt      *time.Time     `json:"last_run_at"`
	NextRunAt      *time.Time     `gorm:"index" json:"next_run_at"`
	RunCount       int            `json:"run_count"`
	LastError      *string        `gorm:"type:text" json:"last_error"`
	DeletedAt      gorm.DeletedAt `gorm:"index:idx_scheduled_tasks_agent_deleted,priority:3" json:"deleted_at"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}
