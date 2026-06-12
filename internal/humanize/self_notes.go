package humanize

import (
	"context"
	"fmt"
	"strings"

	"github.com/oklog/ulid/v2"

	"src.solsynth.dev/sosys/personality/internal/database"
)

const maxAgentSelfNotes = 12

type AgentSelfNoteInput struct {
	Key      string
	Category string
	Content  string
}

func (m *Manager) BuildAgentIdentityOverlay(ctx context.Context, agentID string) (string, error) {
	if m == nil || m.db == nil {
		return "", nil
	}

	notes, err := m.ListAgentSelfNotes(ctx, agentID, "")
	if err != nil {
		return "", err
	}
	if len(notes) == 0 {
		return "", nil
	}

	lines := make([]string, 0, len(notes))
	for _, note := range notes {
		line := note.Content
		if strings.TrimSpace(note.Category) != "" {
			line = "[" + note.Category + "] " + line
		}
		if strings.TrimSpace(note.Key) != "" {
			line = note.Key + ": " + line
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

func (m *Manager) ListAgentSelfNotes(ctx context.Context, agentID, category string) ([]database.AgentSelfNote, error) {
	if m == nil || m.db == nil {
		return nil, nil
	}
	agentID = strings.TrimSpace(agentID)
	category = strings.TrimSpace(category)
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}

	query := m.db.WithContext(ctx).
		Where("agent_id = ?", agentID).
		Order("updated_at DESC").
		Limit(maxAgentSelfNotes)
	if category != "" {
		query = query.Where("category = ?", category)
	}

	var notes []database.AgentSelfNote
	if err := query.Find(&notes).Error; err != nil {
		return nil, err
	}
	return notes, nil
}

func (m *Manager) SaveAgentSelfNote(ctx context.Context, agentID string, input AgentSelfNoteInput) (*database.AgentSelfNote, error) {
	if m == nil || m.db == nil {
		return nil, fmt.Errorf("humanize manager is unavailable")
	}

	agentID = strings.TrimSpace(agentID)
	key := normalizeAgentSelfNoteKey(input.Key)
	category := strings.TrimSpace(input.Category)
	content := strings.TrimSpace(input.Content)

	switch {
	case agentID == "":
		return nil, fmt.Errorf("agent_id is required")
	case key == "":
		return nil, fmt.Errorf("self note key is required")
	case content == "":
		return nil, fmt.Errorf("self note content is required")
	}

	var note database.AgentSelfNote
	err := m.db.WithContext(ctx).
		Where("agent_id = ? AND key = ?", agentID, key).
		First(&note).Error
	switch {
	case err == nil:
		note.Category = category
		note.Content = content
		if err := m.db.WithContext(ctx).Save(&note).Error; err != nil {
			return nil, err
		}
		return &note, nil
	case errorsIsRecordNotFound(err):
		note = database.AgentSelfNote{
			ID:       ulid.Make().String(),
			AgentID:  agentID,
			Key:      key,
			Category: category,
			Content:  content,
		}
		if err := m.db.WithContext(ctx).Create(&note).Error; err != nil {
			return nil, err
		}
		return &note, nil
	default:
		return nil, err
	}
}

func (m *Manager) DeleteAgentSelfNote(ctx context.Context, agentID, key string) (bool, error) {
	if m == nil || m.db == nil {
		return false, nil
	}
	agentID = strings.TrimSpace(agentID)
	key = normalizeAgentSelfNoteKey(key)
	if agentID == "" || key == "" {
		return false, fmt.Errorf("agent_id and key are required")
	}

	result := m.db.WithContext(ctx).Where("agent_id = ? AND key = ?", agentID, key).Delete(&database.AgentSelfNote{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func normalizeAgentSelfNoteKey(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, " ", "_")
	value = strings.ReplaceAll(value, "-", "_")
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '_' || r == '.' || r == ':':
			builder.WriteRune(r)
		}
	}
	return strings.Trim(builder.String(), "_.:")
}
