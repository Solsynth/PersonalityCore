package database

import (
	"fmt"

	"src.solsynth.dev/sosys/persona/internal/config"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type DB struct {
	*gorm.DB
}

func Open(cfg *config.Config) (*DB, error) {
	if cfg.Database.DSN == "" {
		return nil, fmt.Errorf("database dsn is required")
	}

	db, err := gorm.Open(postgres.Open(cfg.Database.DSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, err
	}

	return &DB{DB: db}, nil
}

func (d *DB) AutoMigrate() error {
	if err := d.DB.AutoMigrate(
		&ConversationThread{},
		&ConversationMessage{},
		&ConversationRun{},
		&BillingAccountPolicy{},
		&BillingUsage{},
		&BillingPayment{},
		&OpenAIAccessCredential{},
		&OpenAICredentialUsage{},
		&AgentHumanState{},
		&AgentManualMemory{},
		&AgentMemory{},
		&ExternalChatBinding{},
		&FileSummary{},
		&ScheduledTask{},
		&AgentOAuthSession{},
		&WebSearchPage{},
	); err != nil {
		return err
	}
	if err := d.purgeRemovedPetState(); err != nil {
		return err
	}
	if err := d.migrateAgentSelfNotes(); err != nil {
		return err
	}
	return d.normalizeMemoryScopes()
}

// normalizeMemoryScopes repairs rows written while `scope` was a free-form
// label that reads ignored: agent-scope rows are now agent-global notes, which
// would silently drop an account's own memory out of its list. Every legacy
// row carries an account, because the store has always required one, so any
// such row is a user memory. Idempotent.
func (d *DB) normalizeMemoryScopes() error {
	return d.Exec(`UPDATE agent_memories SET scope = 'user' WHERE account_id <> '' AND scope <> 'user'`).Error
}

// migrateAgentSelfNotes folds the retired agent_self_notes table into the
// unified agent_memories store: each note becomes one agent-scope memory, the
// same row the memory tools now read and write. It is guarded by the table's
// existence, so it runs once and is skipped on fresh databases.
func (d *DB) migrateAgentSelfNotes() error {
	if !d.Migrator().HasTable("agent_self_notes") {
		return nil
	}
	if err := d.Exec(`INSERT INTO agent_memories
			(id, account_id, agent_id, scope, category, key, content, confidence, confirmed,
			 source_message_id, source_run_id, supersedes_id, status, last_observed_at, created_at, updated_at)
		SELECT n.id, '', n.agent_id, 'agent', n.category, n.key, n.content, 1, TRUE,
			'', '', '', 'active', n.updated_at, n.created_at, n.updated_at
		FROM agent_self_notes n
		WHERE n.deleted_at IS NULL
			AND NOT EXISTS (SELECT 1 FROM agent_memories m WHERE m.id = n.id)`).Error; err != nil {
		return err
	}
	return d.Migrator().DropTable("agent_self_notes")
}

// purgeRemovedPetState removes the leftovers of the retired pet feature. Pet
// threads were hidden from conversation listings by their kind, so leaving
// them behind would surface them as unlabeled conversations; the pet_sessions
// table only bound an account to its pet thread and is dead. Both steps are
// idempotent and safe on a fresh database, where the kind column never exists.
func (d *DB) purgeRemovedPetState() error {
	if d.Migrator().HasColumn(&ConversationThread{}, "kind") {
		for _, statement := range []string{
			`DELETE FROM conversation_messages WHERE thread_id IN (SELECT id FROM conversation_threads WHERE kind = 'pet')`,
			`DELETE FROM conversation_runs WHERE thread_id IN (SELECT id FROM conversation_threads WHERE kind = 'pet')`,
			`DELETE FROM conversation_threads WHERE kind = 'pet'`,
		} {
			if err := d.Exec(statement).Error; err != nil {
				return err
			}
		}
		if err := d.Migrator().DropColumn(&ConversationThread{}, "kind"); err != nil {
			return err
		}
	}
	return d.Exec(`DROP TABLE IF EXISTS pet_sessions`).Error
}
