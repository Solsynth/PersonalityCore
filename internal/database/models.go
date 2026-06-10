package database

import (
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type ConversationThread struct {
	ID            string         `gorm:"primaryKey;size:26" json:"id"`
	AccountID     string         `gorm:"size:128;index:idx_threads_account_deleted,priority:1" json:"account_id"`
	AgentID       string         `gorm:"size:64;index" json:"agent_id"`
	Title         string         `gorm:"size:255" json:"title"`
	LastMessageAt *time.Time     `json:"last_message_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index:idx_threads_account_deleted,priority:2" json:"deleted_at"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type ConversationMessage struct {
	ID        string         `gorm:"primaryKey;size:26" json:"id"`
	ThreadID  string         `gorm:"size:26;index:idx_messages_thread_deleted_seq,priority:1" json:"thread_id"`
	RunID     *string        `gorm:"size:26;index" json:"run_id"`
	AccountID string         `gorm:"size:128;index" json:"account_id"`
	Role      string         `gorm:"size:32" json:"role"`
	Content   string         `gorm:"type:text" json:"content"`
	Sequence  int64          `gorm:"index:idx_messages_thread_deleted_seq,priority:3" json:"sequence"`
	Model     *string        `gorm:"size:128" json:"model"`
	Metadata  datatypes.JSON `gorm:"type:jsonb" json:"metadata"`
	DeletedAt gorm.DeletedAt `gorm:"index:idx_messages_thread_deleted_seq,priority:2" json:"deleted_at"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type ConversationRun struct {
	ID                string         `gorm:"primaryKey;size:26" json:"id"`
	ThreadID          string         `gorm:"size:26;index:idx_runs_thread_deleted_created,priority:1" json:"thread_id"`
	AccountID         string         `gorm:"size:128;index" json:"account_id"`
	AgentID           string         `gorm:"size:64;index" json:"agent_id"`
	Status            string         `gorm:"size:32;index" json:"status"`
	Model             string         `gorm:"size:128" json:"model"`
	RequestMessageID  string         `gorm:"size:26" json:"request_message_id"`
	ResponseMessageID *string        `gorm:"size:26" json:"response_message_id"`
	Stream            bool           `json:"stream"`
	Error             *string        `gorm:"type:text" json:"error"`
	Usage             datatypes.JSON `gorm:"type:jsonb" json:"usage"`
	Settings          datatypes.JSON `gorm:"type:jsonb" json:"settings"`
	StartedAt         time.Time      `json:"started_at"`
	CompletedAt       *time.Time     `json:"completed_at"`
	DeletedAt         gorm.DeletedAt `gorm:"index:idx_runs_thread_deleted_created,priority:2" json:"deleted_at"`
	CreatedAt         time.Time      `gorm:"index:idx_runs_thread_deleted_created,priority:3" json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}
