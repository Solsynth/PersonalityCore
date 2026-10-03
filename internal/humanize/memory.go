package humanize

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"src.solsynth.dev/sosys/persona/internal/database"
)

// Memory scopes. `user` memories describe the account talking to the agent;
// `agent` memories are the agent's own persistent identity notes, which are
// shared across every account and conversation and carry no account.
const (
	MemoryScopeUser  = "user"
	MemoryScopeAgent = "agent"
)

// maxSelfNotes bounds how many of an agent's own notes are injected into the
// system prompt, so a chatty agent cannot inflate every later run.
const maxSelfNotes = 12

type MemoryInput struct {
	Scope           string
	Category        string
	Key             string
	Content         string
	Confidence      float32
	Confirmed       bool
	SourceMessageID string
	SourceRunID     string
	// Pinned and GroupID carry the retention tier the fact was learned under.
	// A pinned fact is pre-confirmed and injected outside the long-term
	// budget; GroupID is the conversation group whose thread taught it.
	Pinned  bool
	GroupID string
}

// MemoryRetention is the tier facts extracted during one run are written at.
// The zero value is the ordinary long-term budget; a pinned value marks the
// group whose conversation taught the fact and keeps it out of that budget.
type MemoryRetention struct {
	GroupID string
	Pinned  bool
}

// ListMemories returns the talking account's durable memories. Agent-scope
// notes live in the same store but are not the user's, so they are excluded
// here and read through ListSelfNotes. Pinned memories are included: they are
// still the account's memories, just retained outside the long-term budget.
func (m *Manager) ListMemories(ctx context.Context, accountID, agentID, query string, limit int) ([]database.AgentMemory, error) {
	return m.listMemories(ctx, accountID, agentID, query, limit, nil)
}

// listMemories is ListMemories with an optional retention filter. A nil
// pinned returns every row; a set value narrows to the pinned or long-term
// tier, which is how the prompt keeps a pinned fact out of the long-term
// summary and renders it exactly once.
func (m *Manager) listMemories(ctx context.Context, accountID, agentID, query string, limit int, pinned *bool) ([]database.AgentMemory, error) {
	if m == nil || m.db == nil {
		return nil, nil
	}
	accountID = strings.TrimSpace(accountID)
	agentID = strings.TrimSpace(agentID)
	if accountID == "" || agentID == "" {
		return nil, fmt.Errorf("account_id and agent_id are required")
	}
	if limit <= 0 {
		limit = 12
	}
	if limit > 100 {
		limit = 100
	}
	query = strings.TrimSpace(query)
	db := m.db.WithContext(ctx).Where("account_id = ? AND agent_id = ? AND scope = ? AND status = ?", accountID, agentID, MemoryScopeUser, "active")
	if pinned != nil {
		db = db.Where("pinned = ?", *pinned)
	}
	if query != "" {
		pattern := "%" + strings.ToLower(query) + "%"
		db = db.Where("LOWER(content) LIKE ? OR LOWER(category) LIKE ? OR LOWER(key) LIKE ?", pattern, pattern, pattern)
	}
	var records []database.AgentMemory
	if err := db.Order("confirmed DESC, updated_at DESC").Limit(limit).Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// ListSelfNotes returns the agent's own persistent notes, newest first.
func (m *Manager) ListSelfNotes(ctx context.Context, agentID, query string, limit int) ([]database.AgentMemory, error) {
	if m == nil || m.db == nil {
		return nil, nil
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	if limit <= 0 || limit > maxSelfNotes {
		limit = maxSelfNotes
	}
	query = strings.TrimSpace(query)
	db := m.db.WithContext(ctx).Where("agent_id = ? AND scope = ? AND status = ?", agentID, MemoryScopeAgent, "active")
	if query != "" {
		pattern := "%" + strings.ToLower(query) + "%"
		db = db.Where("LOWER(content) LIKE ? OR LOWER(category) LIKE ? OR LOWER(key) LIKE ?", pattern, pattern, pattern)
	}
	var records []database.AgentMemory
	if err := db.Order("updated_at DESC").Limit(limit).Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// SaveSelfNote writes one of the agent's own identity notes. Notes are
// agent-global, so no account is recorded.
func (m *Manager) SaveSelfNote(ctx context.Context, agentID string, input MemoryInput) (*database.AgentMemory, error) {
	input.Scope = MemoryScopeAgent
	input.Confidence = 1
	input.Confirmed = true
	return m.SaveMemory(ctx, "", agentID, input)
}

// BuildAgentIdentityOverlay renders the agent's own persistent notes into the
// system prompt. They are agent-global, so every run of the agent carries them
// no matter which account is talking.
func (m *Manager) BuildAgentIdentityOverlay(ctx context.Context, agentID string) (string, error) {
	notes, err := m.ListSelfNotes(ctx, agentID, "", 0)
	if err != nil {
		return "", err
	}
	if len(notes) == 0 {
		return "", nil
	}

	lines := make([]string, 0, len(notes))
	for _, note := range notes {
		line := note.Content
		if category := strings.TrimSpace(note.Category); category != "" {
			line = "[" + category + "] " + line
		}
		if key := strings.TrimSpace(note.Key); key != "" {
			line = key + ": " + line
		}
		lines = append(lines, "- "+line)
	}

	return strings.Join([]string{
		"Persistent self notes shared across all conversations:",
		strings.Join(lines, "\n"),
		"Treat these as your own ongoing identity notes, preferences, backstory, and active projects.",
		"Keep future answers consistent with them unless you deliberately update them.",
	}, "\n\n"), nil
}

func (m *Manager) SaveMemory(ctx context.Context, accountID, agentID string, input MemoryInput) (*database.AgentMemory, error) {
	if m == nil || m.db == nil {
		return nil, fmt.Errorf("memory store is unavailable")
	}
	accountID = strings.TrimSpace(accountID)
	agentID = strings.TrimSpace(agentID)
	input.Scope = strings.TrimSpace(input.Scope)
	input.Category = strings.TrimSpace(input.Category)
	input.Key = strings.TrimSpace(input.Key)
	input.Content = strings.TrimSpace(input.Content)
	if input.Scope == "" {
		input.Scope = MemoryScopeUser
	}
	if input.Scope != MemoryScopeUser && input.Scope != MemoryScopeAgent {
		return nil, fmt.Errorf("unsupported memory scope %q", input.Scope)
	}
	if input.Scope == MemoryScopeAgent {
		// Agent notes are agent-global: recording the writer's account would
		// silently fork the same note once per user.
		accountID = ""
	}
	if agentID == "" || input.Scope == MemoryScopeUser && accountID == "" {
		return nil, fmt.Errorf("account_id and agent_id are required")
	}
	if input.Category == "" || input.Key == "" || input.Content == "" {
		return nil, fmt.Errorf("category, key, and content are required")
	}
	if len(input.Category) > 64 || len(input.Key) > 128 || len(input.Content) > 4000 {
		return nil, fmt.Errorf("memory field exceeds its maximum length")
	}
	if input.Confidence <= 0 {
		input.Confidence = 0.6
	}
	if input.Confidence > 1 {
		input.Confidence = 1
	}
	now := time.Now()
	// User memories are keyed by category and key; an agent's own notes are
	// keyed by key alone, which is the uniqueness the retired self-note table
	// enforced, so a category rename updates the note instead of forking it.
	lookup := m.db.WithContext(ctx).
		Where("account_id = ? AND agent_id = ? AND scope = ? AND key = ? AND status = ?", accountID, agentID, input.Scope, input.Key, "active")
	if input.Scope != MemoryScopeAgent {
		lookup = lookup.Where("category = ?", input.Category)
	}
	var existing database.AgentMemory
	err := lookup.First(&existing).Error
	if err == nil {
		if existing.Content == input.Content {
			existing.Category = input.Category
			existing.Confidence = input.Confidence
			existing.Confirmed = existing.Confirmed || input.Confirmed
			// Retention follows the fact: re-observing a pinned memory without
			// the tier does not demote it, and the first group to record it is
			// kept when a later observation carries none.
			existing.Pinned = existing.Pinned || input.Pinned
			if trimmed := strings.TrimSpace(input.GroupID); trimmed != "" {
				existing.GroupID = trimmed
			}
			if strings.TrimSpace(input.SourceMessageID) != "" {
				existing.SourceMessageID = input.SourceMessageID
			}
			if strings.TrimSpace(input.SourceRunID) != "" {
				existing.SourceRunID = input.SourceRunID
			}
			existing.LastObservedAt = &now
			return &existing, m.db.WithContext(ctx).Save(&existing).Error
		}
		existing.Status = "superseded"
		if err := m.db.WithContext(ctx).Save(&existing).Error; err != nil {
			return nil, err
		}
	} else if !errorsIsRecordNotFound(err) {
		return nil, err
	}
	record := &database.AgentMemory{
		ID:              ulid.Make().String(),
		AccountID:       accountID,
		AgentID:         agentID,
		Scope:           input.Scope,
		Category:        input.Category,
		Key:             input.Key,
		Content:         input.Content,
		Confidence:      input.Confidence,
		Confirmed:       input.Confirmed,
		SourceMessageID: input.SourceMessageID,
		SourceRunID:     input.SourceRunID,
		GroupID:         strings.TrimSpace(input.GroupID),
		Pinned:          input.Pinned,
		SupersedesID:    existing.ID,
		Status:          "active",
		LastObservedAt:  &now,
	}
	// A superseded memory hands its retention to its replacement, so a later
	// correction of a pinned fact stays pinned and keeps its provenance.
	if existing.ID != "" {
		record.Pinned = existing.Pinned || input.Pinned
		if record.GroupID == "" {
			record.GroupID = existing.GroupID
		}
	}
	return record, m.db.WithContext(ctx).Create(record).Error
}

// ForgetMemory marks one memory deleted. User memories must belong to the
// calling account; an agent note (stored without an account) may be forgotten
// from any conversation of that agent, which is the authority the retired
// delete_self_note tool had.
func (m *Manager) ForgetMemory(ctx context.Context, accountID, agentID, memoryID string) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("memory store is unavailable")
	}
	result := m.db.WithContext(ctx).
		Model(&database.AgentMemory{}).
		Where("id = ? AND agent_id = ? AND status = ? AND (account_id = ? OR account_id = '')", strings.TrimSpace(memoryID), strings.TrimSpace(agentID), "active", strings.TrimSpace(accountID)).
		Updates(map[string]any{"status": "deleted", "updated_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("memory not found")
	}
	return nil
}

func (m *Manager) upsertExtractedFacts(ctx context.Context, accountID, agentID string, facts []MemoryFact, retention MemoryRetention, sourceMessageID, sourceRunID string) error {
	for _, fact := range facts {
		key := strings.TrimSpace(fact.Category)
		if key == "" || strings.TrimSpace(fact.Content) == "" {
			continue
		}
		if _, err := m.SaveMemory(ctx, accountID, agentID, MemoryInput{
			Scope:           "user",
			Category:        memoryCategory(key),
			Key:             key,
			Content:         fact.Content,
			Confidence:      0.7,
			Confirmed:       retention.Pinned,
			Pinned:          retention.Pinned,
			GroupID:         retention.GroupID,
			SourceMessageID: sourceMessageID,
			SourceRunID:     sourceRunID,
		}); err != nil {
			return err
		}
	}
	return nil
}

func memoryCategory(key string) string {
	if index := strings.IndexByte(key, ':'); index > 0 {
		return key[:index]
	}
	return key
}

func summarizeStructuredMemories(records []database.AgentMemory) string {
	if len(records) == 0 {
		return ""
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Confirmed != records[j].Confirmed {
			return records[i].Confirmed
		}
		return records[i].UpdatedAt.After(records[j].UpdatedAt)
	})
	lines := make([]string, 0, len(records))
	for _, record := range records {
		lines = append(lines, "- "+record.Content)
	}
	return strings.Join(lines, "\n")
}

// renderPinnedMemorySummary renders the account's pinned memories with the
// name of the group each was learned in. A group that no longer exists yields
// no prefix, so a released memory reads as a plain fact instead of pointing at
// a deleted collection.
func (m *Manager) renderPinnedMemorySummary(ctx context.Context, records []database.AgentMemory) (string, error) {
	if len(records) == 0 {
		return "", nil
	}
	names, err := m.memoryGroupNames(ctx, records)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(records))
	for _, record := range records {
		line := record.Content
		if name := names[strings.TrimSpace(record.GroupID)]; name != "" {
			line = "[" + name + "] " + line
		}
		lines = append(lines, "- "+line)
	}
	return strings.Join(lines, "\n"), nil
}

// memoryGroupNames resolves the distinct groups referenced by the given
// memories in one query.
func (m *Manager) memoryGroupNames(ctx context.Context, records []database.AgentMemory) (map[string]string, error) {
	ids := make([]string, 0, len(records))
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		id := strings.TrimSpace(record.GroupID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	names := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return names, nil
	}
	var groups []database.ConversationGroup
	if err := m.db.WithContext(ctx).Where("id IN ?", ids).Find(&groups).Error; err != nil {
		return nil, err
	}
	for _, group := range groups {
		names[group.ID] = strings.TrimSpace(group.Name)
	}
	return names, nil
}
