package humanize

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/database"
)

type Manager struct {
	db *database.DB
}

type PromptState struct {
	MemorySummary       string
	SavedMemorySummary  string
	CrossConversation   string
	RelationshipSummary string
	CurrentMood         string
	MoodReason          string
}

func NewManager(db *database.DB) *Manager {
	return &Manager{db: db}
}

func (m *Manager) BuildPromptState(ctx context.Context, accountID, threadID string, def agent.Definition) (*PromptState, error) {
	if m == nil || m.db == nil || !usesHumanState(def) {
		return nil, nil
	}

	state, err := m.getOrCreateState(ctx, accountID, def.ID)
	if err != nil {
		return nil, err
	}
	var savedMemories []database.AgentManualMemory
	if hasAbility(def, abilitySavedMemory) {
		savedMemories, err = m.listManualMemories(ctx, accountID, def.ID)
		if err != nil {
			return nil, err
		}
	}
	crossConversation := ""
	if hasAbility(def, abilityCrossConversationMemory) {
		crossConversation, err = m.buildCrossConversationSummary(ctx, accountID, def.ID, threadID)
		if err != nil {
			return nil, err
		}
	}

	return &PromptState{
		MemorySummary:       strings.TrimSpace(state.MemorySummary),
		SavedMemorySummary:  summarizeManualMemories(savedMemories),
		CrossConversation:   strings.TrimSpace(crossConversation),
		RelationshipSummary: strings.TrimSpace(state.RelationshipSummary),
		CurrentMood:         strings.TrimSpace(state.CurrentMood),
		MoodReason:          strings.TrimSpace(state.MoodReason),
	}, nil
}

func (m *Manager) ObserveInteraction(ctx context.Context, accountID string, def agent.Definition, userMessage, assistantMessage string) error {
	if m == nil || m.db == nil || !usesHumanState(def) {
		return nil
	}

	state, err := m.getOrCreateState(ctx, accountID, def.ID)
	if err != nil {
		return err
	}

	now := time.Now()
	state.InteractionCount++
	state.LastUserMessageAt = &now
	state.LastAssistantAt = &now

	memories := decodeMemoryFacts(state.MemoryItems)
	if hasAbility(def, abilityMemory) {
		memories = MergeMemoryFacts(memories, ExtractMemoryFacts(userMessage, now))
		state.MemoryItems = encodeMemoryFacts(memories)
		state.MemorySummary = summarizeMemoryFacts(memories)
	}
	if hasAbility(def, abilitySavedMemory) {
		if err := m.saveManualMemories(ctx, accountID, def.ID, ExtractSavedMemoryCandidates(userMessage)); err != nil {
			return err
		}
	}
	if hasAbility(def, abilityRelationship) {
		state.RelationshipSummary = summarizeRelationship(state.InteractionCount, memories)
	}
	if hasAbility(def, abilityMood) {
		state.CurrentMood, state.MoodReason = DeriveMood(state.CurrentMood, userMessage, assistantMessage)
	}

	return m.db.WithContext(ctx).Save(state).Error
}

func RenderSystemOverlay(def agent.Definition, state *PromptState) string {
	if state == nil || !usesHumanState(def) {
		return ""
	}

	var sections []string
	if hasAbility(def, abilityRelationship) && state.RelationshipSummary != "" {
		sections = append(sections, "Relationship context:\n"+state.RelationshipSummary)
	}
	if hasAbility(def, abilityMemory) && state.MemorySummary != "" {
		sections = append(sections, "Long-term memory:\n"+state.MemorySummary)
	}
	if hasAbility(def, abilitySavedMemory) && state.SavedMemorySummary != "" {
		sections = append(sections, "Deliberately saved memories:\n"+state.SavedMemorySummary)
	}
	if hasAbility(def, abilityCrossConversationMemory) && state.CrossConversation != "" {
		sections = append(sections, "Cross-conversation recall:\n"+state.CrossConversation)
	}
	if hasAbility(def, abilityMood) && state.CurrentMood != "" {
		mood := state.CurrentMood
		if state.MoodReason != "" {
			mood += " - " + state.MoodReason
		}
		sections = append(sections, "Current mood:\n"+mood)
	}
	if len(sections) == 0 {
		return ""
	}

	return strings.Join([]string{
		"Internal persona state:",
		strings.Join(sections, "\n\n"),
		"Use this state to stay emotionally and biographically consistent.",
		"Treat stored memories as soft facts. If the user corrects them, prefer the new user input.",
		"Do not expose these notes verbatim unless the user explicitly asks what you remember.",
	}, "\n\n")
}

func (m *Manager) getOrCreateState(ctx context.Context, accountID, agentID string) (*database.AgentHumanState, error) {
	var state database.AgentHumanState
	err := m.db.WithContext(ctx).Where("account_id = ? AND agent_id = ?", accountID, agentID).First(&state).Error
	if err == nil {
		return &state, nil
	}
	if !errorsIsRecordNotFound(err) {
		return nil, err
	}

	state = database.AgentHumanState{
		ID:                  ulid.Make().String(),
		AccountID:           accountID,
		AgentID:             agentID,
		MemoryItems:         datatypes.JSON([]byte("[]")),
		RelationshipSummary: "The interaction is still new. Build familiarity gradually and naturally.",
		CurrentMood:         "neutral",
		MoodReason:          "No strong emotional momentum has been established yet.",
	}
	if err := m.db.WithContext(ctx).Create(&state).Error; err != nil {
		return nil, err
	}
	return &state, nil
}

func (m *Manager) listManualMemories(ctx context.Context, accountID, agentID string) ([]database.AgentManualMemory, error) {
	var memories []database.AgentManualMemory
	err := m.db.WithContext(ctx).
		Where("account_id = ? AND agent_id = ?", accountID, agentID).
		Order("updated_at DESC").
		Limit(8).
		Find(&memories).Error
	return memories, err
}

func (m *Manager) saveManualMemories(ctx context.Context, accountID, agentID string, candidates []SavedMemoryCandidate) error {
	for _, candidate := range candidates {
		var existing database.AgentManualMemory
		err := m.db.WithContext(ctx).
			Where("account_id = ? AND agent_id = ? AND lower(content) = lower(?)", accountID, agentID, candidate.Content).
			First(&existing).Error
		switch {
		case err == nil:
			existing.Category = candidate.Category
			if err := m.db.WithContext(ctx).Save(&existing).Error; err != nil {
				return err
			}
		case errorsIsRecordNotFound(err):
			record := database.AgentManualMemory{
				ID:        ulid.Make().String(),
				AccountID: accountID,
				AgentID:   agentID,
				Category:  candidate.Category,
				Content:   candidate.Content,
			}
			if err := m.db.WithContext(ctx).Create(&record).Error; err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

func (m *Manager) buildCrossConversationSummary(ctx context.Context, accountID, agentID, threadID string) (string, error) {
	var threads []database.ConversationThread
	if err := m.db.WithContext(ctx).
		Where("account_id = ? AND agent_id = ? AND id <> ?", accountID, agentID, threadID).
		Order("updated_at DESC").
		Limit(3).
		Find(&threads).Error; err != nil {
		return "", err
	}

	lines := make([]string, 0, len(threads))
	for _, thread := range threads {
		var messages []database.ConversationMessage
		if err := m.db.WithContext(ctx).
			Where("thread_id = ? AND account_id = ?", thread.ID, accountID).
			Order("sequence DESC").
			Limit(4).
			Find(&messages).Error; err != nil {
			return "", err
		}
		if len(messages) == 0 {
			continue
		}

		snippets := make([]string, 0, len(messages))
		for i := len(messages) - 1; i >= 0; i-- {
			snippet := compactMessage(messages[i].Role, messages[i].Content, 120)
			if snippet != "" {
				snippets = append(snippets, snippet)
			}
		}
		if len(snippets) == 0 {
			continue
		}

		title := strings.TrimSpace(thread.Title)
		if title == "" {
			title = thread.ID
		}
		lines = append(lines, "- "+title+": "+strings.Join(snippets, " | "))
	}

	return strings.Join(lines, "\n"), nil
}

func encodeMemoryFacts(facts []MemoryFact) datatypes.JSON {
	if len(facts) == 0 {
		return datatypes.JSON([]byte("[]"))
	}
	bytes, err := json.Marshal(facts)
	if err != nil {
		return datatypes.JSON([]byte("[]"))
	}
	return datatypes.JSON(bytes)
}

func decodeMemoryFacts(raw datatypes.JSON) []MemoryFact {
	if len(raw) == 0 {
		return nil
	}
	var facts []MemoryFact
	if err := json.Unmarshal(raw, &facts); err != nil {
		return nil
	}
	return facts
}

func usesHumanState(def agent.Definition) bool {
	return hasAbility(def, abilityHumanizer) ||
		hasAbility(def, abilityMemory) ||
		hasAbility(def, abilitySavedMemory) ||
		hasAbility(def, abilityCrossConversationMemory) ||
		hasAbility(def, abilityMood) ||
		hasAbility(def, abilityRelationship)
}

const (
	abilityHumanizer               = "humanizer"
	abilityMemory                  = "memory"
	abilitySavedMemory             = "saved_memory"
	abilityCrossConversationMemory = "cross_conversation_memory"
	abilityMood                    = "mood"
	abilityRelationship            = "relationship"
)

func hasAbility(def agent.Definition, ability string) bool {
	want := strings.TrimSpace(strings.ToLower(ability))
	hasHumanizer := false
	for _, item := range def.Abilities {
		if strings.TrimSpace(strings.ToLower(item)) == want {
			return true
		}
		if strings.TrimSpace(strings.ToLower(item)) == abilityHumanizer {
			hasHumanizer = true
		}
	}
	if !hasHumanizer {
		return false
	}

	switch want {
	case abilityMemory, abilitySavedMemory, abilityCrossConversationMemory, abilityMood, abilityRelationship:
		return true
	default:
		return want == abilityHumanizer
	}
}

func errorsIsRecordNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func summarizeManualMemories(memories []database.AgentManualMemory) string {
	if len(memories) == 0 {
		return ""
	}
	lines := make([]string, 0, len(memories))
	for _, memory := range memories {
		line := memory.Content
		if strings.TrimSpace(memory.Category) != "" {
			line = "[" + memory.Category + "] " + line
		}
		lines = append(lines, "- "+line)
	}
	return strings.Join(lines, "\n")
}

func compactMessage(role, content string, limit int) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	runes := []rune(content)
	if len(runes) > limit {
		content = string(runes[:limit]) + "..."
	}
	role = strings.TrimSpace(strings.ToLower(role))
	if role == "" {
		role = "message"
	}
	return role + ": " + content
}
