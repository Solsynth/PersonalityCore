package database

import (
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The legacy types below recreate the schema a pre-removal deployment still
// holds: pet threads carrying a `kind` (with the same soft-delete layout as
// today's model) and the pet_sessions table that bound an account to its pet
// thread.
type legacyThread struct {
	ID        string `gorm:"primaryKey;size:26"`
	AccountID string `gorm:"size:128;index:idx_threads_account_deleted,priority:1"`
	AgentID   string `gorm:"size:64;index"`
	Kind      string `gorm:"size:32;index"`
	Title     string `gorm:"size:255"`
	DeletedAt gorm.DeletedAt `gorm:"index:idx_threads_account_deleted,priority:2"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (legacyThread) TableName() string { return "conversation_threads" }

type legacyMessage struct {
	ID        string `gorm:"primaryKey;size:26"`
	ThreadID  string `gorm:"size:26;index"`
	AccountID string `gorm:"size:128;index"`
	Role      string `gorm:"size:32"`
	Content   string `gorm:"type:text"`
	Sequence  int64
	DeletedAt gorm.DeletedAt
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (legacyMessage) TableName() string { return "conversation_messages" }

type legacyRun struct {
	ID        string `gorm:"primaryKey;size:26"`
	ThreadID  string `gorm:"size:26;index"`
	AccountID string `gorm:"size:128;index"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (legacyRun) TableName() string { return "conversation_runs" }

type legacyPetSession struct {
	ID              string `gorm:"primaryKey;size:26"`
	AccountID       string `gorm:"size:128;uniqueIndex"`
	AgentID         string `gorm:"size:64;uniqueIndex"`
	ThreadID        string `gorm:"size:26;uniqueIndex"`
	Affection       int    `gorm:"default:50"`
	AffectionReason string `gorm:"type:text"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (legacyPetSession) TableName() string { return "pet_sessions" }

func seedLegacyPetState(t *testing.T, raw *gorm.DB) {
	t.Helper()
	if err := raw.AutoMigrate(&legacyThread{}, &legacyMessage{}, &legacyRun{}, &legacyPetSession{}); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	rows := []any{
		&legacyThread{ID: "pet-thread", AccountID: "acct-1", AgentID: "mochi", Kind: "pet", Title: "Pet"},
		&legacyThread{ID: "normal-thread", AccountID: "acct-1", AgentID: "general", Title: "Chat"},
		&legacyMessage{ID: "pet-message", ThreadID: "pet-thread", AccountID: "acct-1", Role: "user", Content: "hello", Sequence: 1},
		&legacyMessage{ID: "normal-message", ThreadID: "normal-thread", AccountID: "acct-1", Role: "user", Content: "hello", Sequence: 1},
		&legacyRun{ID: "pet-run", ThreadID: "pet-thread", AccountID: "acct-1"},
		&legacyRun{ID: "normal-run", ThreadID: "normal-thread", AccountID: "acct-1"},
		&legacyPetSession{ID: "pet-session", AccountID: "acct-1", AgentID: "mochi", ThreadID: "pet-thread", Affection: 62},
	}
	for _, row := range rows {
		if err := raw.Create(row).Error; err != nil {
			t.Fatalf("seed legacy rows: %v", err)
		}
	}
}

func TestAutoMigratePurgesRetiredPetState(t *testing.T) {
	raw, err := gorm.Open(sqlite.Open("file:"+ulid.Make().String()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	seedLegacyPetState(t, raw)
	db := &DB{DB: raw}

	if err := db.AutoMigrate(); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"conversation_threads", "conversation_messages", "conversation_runs"} {
		var petRows, normalRows int64
		if err := db.Raw("SELECT COUNT(*) FROM " + table + " WHERE id LIKE 'pet-%'").Scan(&petRows).Error; err != nil {
			t.Fatalf("count pet rows in %s: %v", table, err)
		}
		if err := db.Raw("SELECT COUNT(*) FROM " + table + " WHERE id LIKE 'normal-%'").Scan(&normalRows).Error; err != nil {
			t.Fatalf("count normal rows in %s: %v", table, err)
		}
		if petRows != 0 {
			t.Fatalf("%s still holds %d pet rows", table, petRows)
		}
		if normalRows != 1 {
			t.Fatalf("%s lost its non-pet rows: %d", table, normalRows)
		}
	}
	if db.Migrator().HasColumn(&ConversationThread{}, "kind") {
		t.Fatal("retired conversation kind column survived migration")
	}
	if db.Migrator().HasTable("pet_sessions") {
		t.Fatal("pet_sessions table survived migration")
	}

	// Re-running must be a no-op, not an error.
	if err := db.AutoMigrate(); err != nil {
		t.Fatalf("second AutoMigrate() error = %v", err)
	}
}

// legacySelfNote is the retired agent_self_notes table: one agent-global note
// per (agent, key), with soft deletes.
type legacySelfNote struct {
	ID        string `gorm:"primaryKey;size:26"`
	AgentID   string `gorm:"size:64"`
	Key       string `gorm:"size:128"`
	Category  string `gorm:"size:64"`
	Content   string `gorm:"type:text"`
	DeletedAt gorm.DeletedAt
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (legacySelfNote) TableName() string { return "agent_self_notes" }

func TestAutoMigrateFoldsSelfNotesIntoMemories(t *testing.T) {
	raw, err := gorm.Open(sqlite.Open("file:"+ulid.Make().String()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.AutoMigrate(&legacySelfNote{}); err != nil {
		t.Fatalf("create legacy self-note table: %v", err)
	}
	notes := []legacySelfNote{
		{ID: "note-active", AgentID: "michan", Key: "favorite_drink", Category: "preference", Content: "I like hojicha lattes."},
		{ID: "note-deleted", AgentID: "michan", Key: "old_note", Category: "identity", Content: "Dropped.", DeletedAt: gorm.DeletedAt{Time: time.Now(), Valid: true}},
	}
	if err := raw.Create(&notes).Error; err != nil {
		t.Fatalf("seed legacy self notes: %v", err)
	}
	db := &DB{DB: raw}

	if err := db.AutoMigrate(); err != nil {
		t.Fatal(err)
	}

	var memories []AgentMemory
	if err := db.Where("agent_id = ?", "michan").Order("key ASC").Find(&memories).Error; err != nil {
		t.Fatalf("load migrated memories: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("expected only the live note to migrate, got %#v", memories)
	}
	migrated := memories[0]
	if migrated.ID != "note-active" || migrated.Scope != "agent" || migrated.AccountID != "" {
		t.Fatalf("note did not become an agent-scope memory: %#v", migrated)
	}
	if migrated.Key != "favorite_drink" || migrated.Category != "preference" || migrated.Content != "I like hojicha lattes." {
		t.Fatalf("note content was not carried over: %#v", migrated)
	}
	if migrated.Status != "active" || !migrated.Confirmed || migrated.Confidence != 1 {
		t.Fatalf("migrated note lost its standing: %#v", migrated)
	}
	if db.Migrator().HasTable("agent_self_notes") {
		t.Fatal("retired agent_self_notes table survived migration")
	}

	// Re-running must be a no-op, not a duplicate insert.
	if err := db.AutoMigrate(); err != nil {
		t.Fatalf("second AutoMigrate() error = %v", err)
	}
	var count int64
	db.Model(&AgentMemory{}).Where("agent_id = ?", "michan").Count(&count)
	if count != 1 {
		t.Fatalf("memory count after second migration = %d, want 1", count)
	}
}

func TestAutoMigrateNormalizesLegacyMemoryScopes(t *testing.T) {
	raw, err := gorm.Open(sqlite.Open("file:"+ulid.Make().String()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	db := &DB{DB: raw}
	if err := db.AutoMigrate(); err != nil {
		t.Fatal(err)
	}

	// A memory written by an older client that used `scope` as its own label
	// was still the account's memory, and must stay one.
	legacy := AgentMemory{ID: "legacy-1", AccountID: "acct-1", AgentID: "michan", Scope: "thread", Category: "preference", Key: "favorite_drink", Content: "Tea.", Status: "active"}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	note := AgentMemory{ID: "note-1", AgentID: "michan", Scope: "agent", Category: "identity", Key: "speaking_style", Content: "Short sentences.", Status: "active"}
	if err := db.Create(&note).Error; err != nil {
		t.Fatal(err)
	}

	if err := db.AutoMigrate(); err != nil {
		t.Fatal(err)
	}

	var memory AgentMemory
	if err := db.First(&memory, "id = ?", "legacy-1").Error; err != nil {
		t.Fatal(err)
	}
	if memory.Scope != "user" {
		t.Fatalf("legacy memory scope = %q, want user", memory.Scope)
	}
	var agentNote AgentMemory
	if err := db.First(&agentNote, "id = ?", "note-1").Error; err != nil {
		t.Fatal(err)
	}
	if agentNote.Scope != "agent" {
		t.Fatalf("agent note scope = %q, want agent", agentNote.Scope)
	}
}

func TestAutoMigrateLeavesFreshDatabaseAlone(t *testing.T) {
	raw, err := gorm.Open(sqlite.Open("file:"+ulid.Make().String()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	db := &DB{DB: raw}

	if err := db.AutoMigrate(); err != nil {
		t.Fatalf("AutoMigrate() on fresh database error = %v", err)
	}
	if db.Migrator().HasTable("pet_sessions") {
		t.Fatal("fresh migration created the retired pet_sessions table")
	}
	if db.Migrator().HasTable("agent_self_notes") {
		t.Fatal("fresh migration created the retired agent_self_notes table")
	}
}
