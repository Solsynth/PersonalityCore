package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/persona/internal/database"
	"src.solsynth.dev/sosys/persona/internal/humanize"
)

// maxConversationBatch bounds one batch-delete or group-assign call. A client
// selecting more than this in one request is asking to move a whole history at
// once; the cap keeps the transaction and the id list finite.
const maxConversationBatch = 200

// ConversationGroupInput is the create body. The name is required; the
// description is free text and may be empty.
type ConversationGroupInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ConversationGroupUpdateInput is the PATCH body. Its fields are pointers so
// an absent field is distinguishable from one sent empty: a missing field is
// left untouched, while a present field is validated and applied.
type ConversationGroupUpdateInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// ConversationGroupView is a group as the API returns it: its own fields plus
// the number of live conversations filed under it.
type ConversationGroupView struct {
	database.ConversationGroup
	ConversationCount int64 `json:"conversation_count"`
}

// memoryRetentionFor maps a thread's group membership onto the memory
// retention tier: facts learned in a grouped conversation are pinned under
// that group, while an ungrouped thread leaves them in the ordinary
// long-term store competing for its budget.
func memoryRetentionFor(thread *database.ConversationThread) humanize.MemoryRetention {
	if thread == nil || thread.GroupID == nil {
		return humanize.MemoryRetention{}
	}
	groupID := strings.TrimSpace(*thread.GroupID)
	if groupID == "" {
		return humanize.MemoryRetention{}
	}
	return humanize.MemoryRetention{Pinned: true, GroupID: groupID}
}

// cleanConversationIDs trims and de-duplicates an id list, preserving order
// and stopping at the batch cap. Empty entries are dropped, so a list that is
// empty after cleaning is the caller's error rather than a silent no-op.
func cleanConversationIDs(ids []string) []string {
	cleaned := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		cleaned = append(cleaned, id)
		if len(cleaned) == maxConversationBatch {
			break
		}
	}
	return cleaned
}

func (s *ConversationService) ListConversationGroups(ctx context.Context, accountID string) ([]ConversationGroupView, error) {
	var groups []database.ConversationGroup
	if err := s.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("name ASC, created_at ASC").
		Find(&groups).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(groups))
	if len(groups) > 0 {
		ids := make([]string, 0, len(groups))
		for _, group := range groups {
			ids = append(ids, group.ID)
		}
		// One grouped count covers every group; counting per group would be N
		// queries for a list that is small by nature. Deleted threads are
		// excluded by GORM's soft-delete scope, so this is a live count.
		type groupCount struct {
			GroupID string
			Total   int64
		}
		var rows []groupCount
		if err := s.db.WithContext(ctx).
			Model(&database.ConversationThread{}).
			Select("group_id, count(*) AS total").
			Where("account_id = ? AND group_id IN ?", accountID, ids).
			Group("group_id").
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			counts[row.GroupID] = row.Total
		}
	}
	views := make([]ConversationGroupView, 0, len(groups))
	for _, group := range groups {
		views = append(views, ConversationGroupView{ConversationGroup: group, ConversationCount: counts[group.ID]})
	}
	return views, nil
}

func (s *ConversationService) CreateConversationGroup(ctx context.Context, accountID string, input ConversationGroupInput) (*ConversationGroupView, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if len([]rune(name)) > 128 {
		return nil, fmt.Errorf("name must be at most 128 characters")
	}
	group := &database.ConversationGroup{
		ID:          newID(),
		AccountID:   accountID,
		Name:        name,
		Description: strings.TrimSpace(input.Description),
	}
	if err := s.db.WithContext(ctx).Create(group).Error; err != nil {
		return nil, err
	}
	return &ConversationGroupView{ConversationGroup: *group}, nil
}

func (s *ConversationService) UpdateConversationGroup(ctx context.Context, accountID, groupID string, input ConversationGroupUpdateInput) (*ConversationGroupView, error) {
	group, err := s.getOwnedConversationGroup(ctx, accountID, groupID)
	if err != nil {
		return nil, err
	}
	updates := make(map[string]any, 2)
	changed := false
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			return nil, fmt.Errorf("name must not be empty")
		}
		if len([]rune(name)) > 128 {
			return nil, fmt.Errorf("name must be at most 128 characters")
		}
		updates["name"] = name
		changed = true
	}
	if input.Description != nil {
		description := strings.TrimSpace(*input.Description)
		updates["description"] = description
		// A cleared description alone is not a change worth a request; paired
		// with a name it is applied, which is how a client empties the field.
		if description != "" {
			changed = true
		}
	}
	if !changed {
		return nil, fmt.Errorf("at least one field is required")
	}
	if err := s.db.WithContext(ctx).
		Model(&database.ConversationGroup{}).
		Where("id = ?", group.ID).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	refreshed, err := s.getOwnedConversationGroup(ctx, accountID, group.ID)
	if err != nil {
		return nil, err
	}
	return &ConversationGroupView{ConversationGroup: *refreshed}, nil
}

func (s *ConversationService) DeleteConversationGroup(ctx context.Context, accountID, groupID string) error {
	group, err := s.getOwnedConversationGroup(ctx, accountID, groupID)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Threads survive their group: they simply become ungrouped.
		if err := tx.Model(&database.ConversationThread{}).
			Where("account_id = ? AND group_id = ?", accountID, group.ID).
			Update("group_id", nil).Error; err != nil {
			return err
		}
		// The retention the group granted is released. The memories stay
		// active and confirmed; they just go back to competing for the
		// long-term budget instead of being injected unconditionally.
		if err := tx.Model(&database.AgentMemory{}).
			Where("account_id = ? AND group_id = ?", accountID, group.ID).
			Updates(map[string]any{"pinned": false, "group_id": ""}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", group.ID).Delete(&database.ConversationGroup{}).Error
	})
}

// SetConversationGroup files the account's threads under a group, or clears
// their membership when groupID is empty. An empty group is not an error: it
// is the "ungrouped" action. A non-empty one must be a live group the caller
// owns, so a stale client cannot file threads under someone else's collection.
func (s *ConversationService) SetConversationGroup(ctx context.Context, accountID string, ids []string, groupID string) (int, error) {
	cleaned := cleanConversationIDs(ids)
	if len(cleaned) == 0 {
		return 0, fmt.Errorf("ids is required")
	}
	groupID = strings.TrimSpace(groupID)
	updated := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if groupID != "" {
			var group database.ConversationGroup
			if err := tx.Where("account_id = ? AND id = ?", accountID, groupID).First(&group).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return fmt.Errorf("unknown conversation group")
				}
				return err
			}
		}
		var threads []database.ConversationThread
		if err := tx.Where("account_id = ? AND id IN ?", accountID, cleaned).Find(&threads).Error; err != nil {
			return err
		}
		if len(threads) == 0 {
			return nil
		}
		threadIDs := make([]string, 0, len(threads))
		for _, thread := range threads {
			threadIDs = append(threadIDs, thread.ID)
		}
		var target any
		if groupID != "" {
			target = groupID
		}
		result := tx.Model(&database.ConversationThread{}).
			Where("account_id = ? AND id IN ?", accountID, threadIDs).
			Update("group_id", target)
		if result.Error != nil {
			return result.Error
		}
		updated = int(result.RowsAffected)
		if groupID == "" {
			return nil
		}
		// Retroactive pinning: memories already learned from these threads
		// join the group's retention immediately, so assigning a conversation
		// reclassifies what it has taught rather than waiting for the next
		// message. Already-pinned rows are left alone: they belong to whatever
		// group pinned them first.
		messageIDs := tx.Model(&database.ConversationMessage{}).
			Select("id").
			Where("account_id = ? AND thread_id IN ?", accountID, threadIDs)
		return tx.Model(&database.AgentMemory{}).
			Where("account_id = ? AND scope = ? AND status = ? AND pinned = ?", accountID, humanize.MemoryScopeUser, "active", false).
			Where("source_message_id IN (?)", messageIDs).
			Updates(map[string]any{"pinned": true, "group_id": groupID}).Error
	})
	if err != nil {
		return 0, err
	}
	return updated, nil
}

// DeleteConversation soft-deletes one of the account's threads together with
// its messages and runs. A thread that is not the caller's live thread is
// reported as missing, not forbidden: the endpoint's contract is a plain 404,
// and distinguishing the two would confirm another account's id.
func (s *ConversationService) DeleteConversation(ctx context.Context, accountID, threadID string) error {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ErrNotFound
	}
	var thread database.ConversationThread
	if err := s.db.WithContext(ctx).Where("id = ?", threadID).First(&thread).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if thread.AccountID != accountID {
		return ErrNotFound
	}
	return s.softDeleteThreads(ctx, accountID, []string{thread.ID})
}

// DeleteConversations soft-deletes every named thread the caller owns and
// returns how many were removed. Ids that are unknown or belong to another
// account are skipped, not errors: a multi-select delete should not fail
// because one row was already gone.
func (s *ConversationService) DeleteConversations(ctx context.Context, accountID string, ids []string) (int, error) {
	cleaned := cleanConversationIDs(ids)
	if len(cleaned) == 0 {
		return 0, fmt.Errorf("ids is required")
	}
	var threads []database.ConversationThread
	if err := s.db.WithContext(ctx).
		Where("account_id = ? AND id IN ?", accountID, cleaned).
		Find(&threads).Error; err != nil {
		return 0, err
	}
	if len(threads) == 0 {
		return 0, nil
	}
	owned := make([]string, 0, len(threads))
	for _, thread := range threads {
		owned = append(owned, thread.ID)
	}
	if err := s.softDeleteThreads(ctx, accountID, owned); err != nil {
		return 0, err
	}
	return len(owned), nil
}

// softDeleteThreads removes a set of already-owned threads with their messages
// and runs in one transaction, so a conversation never half-disappears. The
// rows stay on disk for audit, hidden by GORM's soft-delete scope.
func (s *ConversationService) softDeleteThreads(ctx context.Context, accountID string, threadIDs []string) error {
	if len(threadIDs) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("account_id = ? AND thread_id IN ?", accountID, threadIDs).
			Delete(&database.ConversationMessage{}).Error; err != nil {
			return err
		}
		if err := tx.Where("account_id = ? AND thread_id IN ?", accountID, threadIDs).
			Delete(&database.ConversationRun{}).Error; err != nil {
			return err
		}
		return tx.Where("account_id = ? AND id IN ?", accountID, threadIDs).
			Delete(&database.ConversationThread{}).Error
	})
}

// getOwnedConversationGroup loads a live group and enforces ownership: an
// unknown group is ErrNotFound, another account's is ErrForbidden.
func (s *ConversationService) getOwnedConversationGroup(ctx context.Context, accountID, groupID string) (*database.ConversationGroup, error) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return nil, ErrNotFound
	}
	var group database.ConversationGroup
	if err := s.db.WithContext(ctx).Where("id = ?", groupID).First(&group).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if group.AccountID != accountID {
		return nil, ErrForbidden
	}
	return &group, nil
}
