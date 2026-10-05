package database

import (
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ConversationGroup is a named collection of an account's threads. Threads in a
// group are the ones the account called important: what the agent learns in them
// is pinned in the memory store instead of competing for the long-term budget. An
// archived group leaves the account's list while keeping the retention it
// granted: its threads stay filed and their memories stay pinned, so archiving
// hides the collection without releasing what it taught the agent.
type ConversationGroup struct {
	ID          string         `gorm:"primaryKey;size:26" json:"id"`
	AccountID   string         `gorm:"size:128;index:idx_groups_account_deleted,priority:1" json:"account_id"`
	Name        string         `gorm:"size:128" json:"name"`
	Description string         `gorm:"type:text" json:"description"`
	Archived    bool           `gorm:"default:false" json:"archived"`
	DeletedAt   gorm.DeletedAt `gorm:"index:idx_groups_account_deleted,priority:2" json:"deleted_at"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type ConversationThread struct {
	ID        string `gorm:"primaryKey;size:26" json:"id"`
	AccountID string `gorm:"size:128;index:idx_threads_account_deleted,priority:1" json:"account_id"`
	AgentID   string `gorm:"size:64;index" json:"agent_id"`
	// GroupID is the account-owned group this thread was filed under. It is a
	// pointer so "ungrouped" is distinguishable from any real group id, and
	// because clearing membership is a null update, not an empty string.
	GroupID        *string `gorm:"size:26;index" json:"group_id,omitempty"`
	Title          string  `gorm:"size:255" json:"title"`
	PerkLevel      int32   `gorm:"default:0" json:"perk_level"`
	ContextSummary string  `gorm:"type:text" json:"context_summary"`
	// ActivatedSkills names the server-owned skills this conversation has
	// switched on, so a run rebuilds the same tool set it had last time
	// instead of making the model ask again. Caller-owned capabilities are
	// not stored here: only their client holds their definitions, and it
	// declares the ones it has loaded on every run.
	ActivatedSkills datatypes.JSON `gorm:"type:jsonb" json:"activated_skills"`
	SummarySeq      int64          `json:"summary_seq"`
	SummaryAt       *time.Time     `json:"summary_at"`
	LastMessageAt   *time.Time     `json:"last_message_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index:idx_threads_account_deleted,priority:2" json:"deleted_at"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
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
	BillingUsageID    string         `gorm:"size:26;index" json:"billing_usage_id"`
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

// BillingAccountPolicy contains the per-account overrides. Nil values inherit
// the service defaults; zero is a deliberate unlimited override for run limits.
type BillingAccountPolicy struct {
	AccountID          string         `gorm:"primaryKey;size:128" json:"account_id"`
	HourlyRunLimit     *int           `json:"hourly_run_limit"`
	DailyRunLimit      *int           `json:"daily_run_limit"`
	HourlyUsageLimits  datatypes.JSON `gorm:"type:jsonb" json:"hourly_usage_limits"`
	DailyUsageLimits   datatypes.JSON `gorm:"type:jsonb" json:"daily_usage_limits"`
	InstantBillingWall *string        `gorm:"size:64" json:"instant_billing_wall"`
	Blacklisted        bool           `gorm:"default:false;index" json:"blacklisted"`
	BlacklistReason    string         `gorm:"type:text" json:"blacklist_reason"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

// BillingUsage is one billable action. It is retained as the audit ledger and
// linked to a payment once its UTC-day (or instant) charge succeeds.
//
// A row records two orthogonal things: what was consumed (Action, Model,
// tokens, Amount) and who consumed it (AccountID, RunID, and the request
// attribution in Surface/CredentialID/ClientIP/DeviceID/UserAgent). The
// attribution is copied from the request that caused the charge, so the ledger
// can answer "which call from which address spent this" without a join back to
// a request log that does not exist.
type BillingUsage struct {
	ID        string  `gorm:"primaryKey;size:26" json:"id"`
	RunID     *string `gorm:"size:26;uniqueIndex" json:"run_id"`
	AccountID string  `gorm:"size:128;index:idx_billing_usage_account_created,priority:1;index:idx_billing_usage_account_payment,priority:1" json:"account_id"`
	// Action is the canonical billable operation: "generation" for a model
	// call, or the charge name a non-generation action was written under
	// (for example "web_search/tavily"). Model keeps the historical label for
	// those non-generation rows so existing ledger readers keep working.
	Action string `gorm:"size:64;index:idx_billing_usage_account_action,priority:2" json:"action"`
	Model  string `gorm:"size:128" json:"model"`
	// Surface is the API endpoint or RPC that admitted the caller, for
	// example "/api/conversations/:id/runs" or a gRPC full method name.
	Surface string `gorm:"size:128" json:"surface"`
	// CredentialID is the AI access credential (sat_ token) the call was made
	// with, nil when it used the account's own session.
	CredentialID *string `gorm:"size:26;index" json:"credential_id,omitempty"`
	ClientIP     string  `gorm:"size:64;index:idx_billing_usage_account_ip,priority:2" json:"client_ip"`
	DeviceID     string  `gorm:"size:128;index:idx_billing_usage_account_device,priority:2" json:"device_id"`
	UserAgent    string  `gorm:"size:256" json:"user_agent"`
	Currency     string  `gorm:"size:32;index:idx_billing_usage_account_payment,priority:2" json:"currency"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	// Amount is the outstanding balance on the row; a partial payment reduces
	// it while OriginalAmount keeps the price the usage was incurred at.
	Amount         string    `gorm:"size:64" json:"amount"`
	OriginalAmount string    `gorm:"size:64" json:"original_amount"`
	PaymentID      *string   `gorm:"size:26;index:idx_billing_usage_account_payment,priority:3" json:"payment_id"`
	CreatedAt      time.Time `gorm:"index:idx_billing_usage_account_created,priority:2" json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type OpenAIAccessCredential struct {
	ID            string         `gorm:"primaryKey;size:26" json:"id"`
	AccountID     string         `gorm:"size:128;index:idx_openai_credentials_account_revoked,priority:1" json:"account_id"`
	Name          string         `gorm:"size:128" json:"name"`
	TokenHash     string         `gorm:"size:64;uniqueIndex" json:"-"`
	TokenPrefix   string         `gorm:"size:24" json:"token_prefix"`
	AgentIDs      datatypes.JSON `gorm:"type:jsonb" json:"agent_ids"`
	Providers     datatypes.JSON `gorm:"type:jsonb" json:"providers"`
	Models        datatypes.JSON `gorm:"type:jsonb" json:"models"`
	UsageLimit    string         `gorm:"size:64" json:"usage_limit"`
	UsageUsed     string         `gorm:"size:64" json:"usage_used"`
	UsageCurrency string         `gorm:"size:32" json:"usage_currency"`
	Enabled       bool           `gorm:"default:true;index:idx_openai_credentials_account_revoked,priority:2" json:"enabled"`
	RevokedAt     *time.Time     `json:"revoked_at,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type OpenAICredentialUsage struct {
	ID           string    `gorm:"primaryKey;size:26" json:"id"`
	CredentialID string    `gorm:"size:26;index:idx_openai_credential_usage_credential_created,priority:1" json:"credential_id"`
	AccountID    string    `gorm:"size:128;index" json:"account_id"`
	Model        string    `gorm:"size:128" json:"model"`
	Currency     string    `gorm:"size:32" json:"currency"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	Amount       string    `gorm:"size:64" json:"amount"`
	CreatedAt    time.Time `gorm:"index:idx_openai_credential_usage_credential_created,priority:2" json:"created_at"`
}

type BillingPayment struct {
	ID          string    `gorm:"primaryKey;size:26" json:"id"`
	AccountID   string    `gorm:"size:128;index" json:"account_id"`
	Amount      string    `gorm:"size:64" json:"amount"`
	Currency    string    `gorm:"size:32" json:"currency"`
	WalletTxID  string    `gorm:"size:128;uniqueIndex" json:"wallet_transaction_id"`
	PeriodStart time.Time `gorm:"index" json:"period_start"`
	PeriodEnd   time.Time `gorm:"index" json:"period_end"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// WebSearchPreference is the account's choice of where its web searches run:
// the single engine its searches are restricted to. It exists so a caller can
// keep searches on the engine whose price it is willing to pay instead of the
// server's own order, which may reach a metered provider. One row per account;
// an absent row, or an empty engine, leaves the choice to the server.
type WebSearchPreference struct {
	AccountID string    `gorm:"primaryKey;size:128" json:"account_id"`
	Engine    string    `gorm:"size:64" json:"engine"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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

type AgentMemory struct {
	ID              string  `gorm:"primaryKey;size:26" json:"id"`
	AccountID       string  `gorm:"size:128;index:idx_agent_memories_scope_status,priority:1;index:idx_agent_memories_lookup,priority:1" json:"account_id"`
	AgentID         string  `gorm:"size:64;index:idx_agent_memories_scope_status,priority:2;index:idx_agent_memories_lookup,priority:2" json:"agent_id"`
	Scope           string  `gorm:"size:32;index:idx_agent_memories_scope_status,priority:3" json:"scope"`
	Category        string  `gorm:"size:64;index:idx_agent_memories_lookup,priority:3" json:"category"`
	Key             string  `gorm:"size:128;index:idx_agent_memories_lookup,priority:4" json:"key"`
	Content         string  `gorm:"type:text" json:"content"`
	Confidence      float32 `json:"confidence"`
	Confirmed       bool    `json:"confirmed"`
	SourceMessageID string  `gorm:"size:26;index" json:"source_message_id"`
	SourceRunID     string  `gorm:"size:26;index" json:"source_run_id"`
	SupersedesID    string  `gorm:"size:26;index" json:"supersedes_id"`
	// GroupID records which conversation group taught the agent this fact; it
	// is provenance for pinned memories and is cleared when that group dies.
	GroupID string `gorm:"size:26;index" json:"group_id"`
	// Pinned marks the retention tier: pinned facts are written pre-confirmed
	// and injected outside the long-term budget, so an important conversation
	// keeps influencing the agent long after its messages scroll away.
	Pinned         bool       `gorm:"default:false;index" json:"pinned"`
	Status         string     `gorm:"size:24;index:idx_agent_memories_scope_status,priority:4" json:"status"`
	LastObservedAt *time.Time `json:"last_observed_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
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

// WebSearchPage is one crawled document kept by the local web search index.
// Pages are server-global (search is not account-scoped) and keyed by URL so a
// re-crawl refreshes the stored text instead of adding a duplicate.
type WebSearchPage struct {
	ID        string    `gorm:"primaryKey;size:26" json:"id"`
	URL       string    `gorm:"size:768;uniqueIndex" json:"url"`
	Host      string    `gorm:"size:255;index" json:"host"`
	Title     string    `gorm:"type:text" json:"title"`
	Text      string    `gorm:"type:text" json:"text"`
	FetchedAt time.Time `json:"fetched_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
