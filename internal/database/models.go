package database

import (
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type ConversationThread struct {
	ID             string         `gorm:"primaryKey;size:26" json:"id"`
	AccountID      string         `gorm:"size:128;index:idx_threads_account_deleted,priority:1" json:"account_id"`
	AgentID        string         `gorm:"size:64;index" json:"agent_id"`
	Title          string         `gorm:"size:255" json:"title"`
	ContextSummary string         `gorm:"type:text" json:"context_summary"`
	SummarySeq     int64          `json:"summary_seq"`
	SummaryAt      *time.Time     `json:"summary_at"`
	LastMessageAt  *time.Time     `json:"last_message_at"`
	DeletedAt      gorm.DeletedAt `gorm:"index:idx_threads_account_deleted,priority:2" json:"deleted_at"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
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

type AgentHumanState struct {
	ID                  string         `gorm:"primaryKey;size:26" json:"id"`
	AccountID           string         `gorm:"size:128;uniqueIndex:idx_human_states_account_agent,priority:1" json:"account_id"`
	AgentID             string         `gorm:"size:64;uniqueIndex:idx_human_states_account_agent,priority:2" json:"agent_id"`
	MemorySummary       string         `gorm:"type:text" json:"memory_summary"`
	MemoryItems         datatypes.JSON `gorm:"type:jsonb" json:"memory_items"`
	RelationshipSummary string         `gorm:"type:text" json:"relationship_summary"`
	CurrentMood         string         `gorm:"size:128" json:"current_mood"`
	MoodReason          string         `gorm:"type:text" json:"mood_reason"`
	InteractionCount    int64          `json:"interaction_count"`
	LastUserMessageAt   *time.Time     `json:"last_user_message_at"`
	LastAssistantAt     *time.Time     `json:"last_assistant_at"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

type AgentManualMemory struct {
	ID        string         `gorm:"primaryKey;size:26" json:"id"`
	AccountID string         `gorm:"size:128;index:idx_manual_memories_account_agent_deleted,priority:1" json:"account_id"`
	AgentID   string         `gorm:"size:64;index:idx_manual_memories_account_agent_deleted,priority:2" json:"agent_id"`
	Category  string         `gorm:"size:64" json:"category"`
	Content   string         `gorm:"type:text" json:"content"`
	DeletedAt gorm.DeletedAt `gorm:"index:idx_manual_memories_account_agent_deleted,priority:3" json:"deleted_at"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type AgentSelfNote struct {
	ID        string         `gorm:"primaryKey;size:26" json:"id"`
	AgentID   string         `gorm:"size:64;uniqueIndex:idx_agent_self_notes_agent_key,priority:1;index:idx_agent_self_notes_agent_deleted,priority:1" json:"agent_id"`
	Key       string         `gorm:"size:128;uniqueIndex:idx_agent_self_notes_agent_key,priority:2" json:"key"`
	Category  string         `gorm:"size:64;index:idx_agent_self_notes_agent_deleted,priority:2" json:"category"`
	Content   string         `gorm:"type:text" json:"content"`
	DeletedAt gorm.DeletedAt `gorm:"index:idx_agent_self_notes_agent_deleted,priority:3" json:"deleted_at"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type ExternalChatBinding struct {
	ID              string         `gorm:"primaryKey;size:26" json:"id"`
	AgentID         string         `gorm:"size:64;uniqueIndex:idx_external_chat_bindings_agent_room,priority:1" json:"agent_id"`
	RemoteRoomID    string         `gorm:"size:128;uniqueIndex:idx_external_chat_bindings_agent_room,priority:2" json:"remote_room_id"`
	RemoteRoomType  *int           `json:"remote_room_type"`
	EngagementState string         `gorm:"size:32" json:"engagement_state"`
	EngagedUntil    *time.Time     `json:"engaged_until"`
	ThreadID        string         `gorm:"size:26;index" json:"thread_id"`
	AccountID       string         `gorm:"size:128;index" json:"account_id"`
	RemoteAccountID string         `gorm:"size:128" json:"remote_account_id"`
	RemoteAccount   string         `gorm:"size:128" json:"remote_account"`
	LastMessageAt   *time.Time     `json:"last_message_at"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"deleted_at"`
}

type FileSummary struct {
	ID           string    `gorm:"primaryKey;size:26" json:"id"`
	AttachmentID string    `gorm:"size:128;uniqueIndex" json:"attachment_id"`
	Summary      string    `gorm:"type:text" json:"summary"`
	Model        string    `gorm:"size:128" json:"model"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
