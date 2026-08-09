package humanize

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"src.solsynth.dev/sosys/personality/internal/database"
)

type MemoryInput struct {
	Scope           string
	Category        string
	Key             string
	Content         string
	Confidence      float32
	Confirmed       bool
	SourceMessageID string
	SourceRunID     string
}

func (m *Manager) ListMemories(ctx context.Context, accountID, agentID, query string, limit int) ([]database.AgentMemory, error) {
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
	db := m.db.WithContext(ctx).Where("account_id = ? AND agent_id = ? AND status = ?", accountID, agentID, "active")
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
	if accountID == "" || agentID == "" || input.Category == "" || input.Key == "" || input.Content == "" {
		return nil, fmt.Errorf("account_id, agent_id, category, key, and content are required")
	}
	if len(input.Category) > 64 || len(input.Key) > 128 || len(input.Content) > 4000 {
		return nil, fmt.Errorf("memory field exceeds its maximum length")
	}
	if input.Scope == "" {
		input.Scope = "user"
	}
	if input.Confidence <= 0 {
		input.Confidence = 0.6
	}
	if input.Confidence > 1 {
		input.Confidence = 1
	}
	now := time.Now()
	var existing database.AgentMemory
	err := m.db.WithContext(ctx).
		Where("account_id = ? AND agent_id = ? AND scope = ? AND category = ? AND key = ? AND status = ?", accountID, agentID, input.Scope, input.Category, input.Key, "active").
		First(&existing).Error
	if err == nil {
		if existing.Content == input.Content {
			existing.Confidence = input.Confidence
			existing.Confirmed = existing.Confirmed || input.Confirmed
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
		SupersedesID:    existing.ID,
		Status:          "active",
		LastObservedAt:  &now,
	}
	return record, m.db.WithContext(ctx).Create(record).Error
}

func (m *Manager) ForgetMemory(ctx context.Context, accountID, agentID, memoryID string) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("memory store is unavailable")
	}
	result := m.db.WithContext(ctx).
		Model(&database.AgentMemory{}).
		Where("id = ? AND account_id = ? AND agent_id = ? AND status = ?", strings.TrimSpace(memoryID), strings.TrimSpace(accountID), strings.TrimSpace(agentID), "active").
		Updates(map[string]any{"status": "deleted", "updated_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("memory not found")
	}
	return nil
}

func (m *Manager) upsertExtractedFacts(ctx context.Context, accountID, agentID string, facts []MemoryFact, sourceMessageID, sourceRunID string) error {
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
