package service

import (
	"testing"

	"src.solsynth.dev/sosys/persona/internal/config"
	"src.solsynth.dev/sosys/persona/internal/database"
)

func newConversationGroupTestService(t *testing.T) *ConversationService {
	t.Helper()
	return NewConversationService(openTestDB(t), &config.Config{}, nil, nil)
}

func createTestThread(t *testing.T, svc *ConversationService, accountID, id string) *database.ConversationThread {
	t.Helper()
	thread := &database.ConversationThread{ID: id, AccountID: accountID, AgentID: "mochi", Title: "Chat"}
	if err := svc.db.Create(thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	return thread
}

func mustCreateGroup(t *testing.T, svc *ConversationService, accountID, name string) *ConversationGroupView {
	t.Helper()
	group, err := svc.CreateConversationGroup(t.Context(), accountID, ConversationGroupInput{Name: name})
	if err != nil {
		t.Fatalf("CreateConversationGroup(%q) error = %v", name, err)
	}
	return group
}

func TestDeleteConversationRemovesThreadMessagesAndRuns(t *testing.T) {
	svc := newConversationGroupTestService(t)
	ctx := t.Context()
	thread := createTestThread(t, svc, "acct-1", newID())
	if err := svc.db.Create(&database.ConversationMessage{
		ID: newID(), ThreadID: thread.ID, AccountID: "acct-1", Role: "user", Content: "hi", Sequence: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.db.Create(&database.ConversationRun{
		ID: newID(), ThreadID: thread.ID, AccountID: "acct-1", AgentID: "mochi", Status: "completed",
	}).Error; err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteConversation(ctx, "acct-1", thread.ID); err != nil {
		t.Fatalf("DeleteConversation() error = %v", err)
	}
	if _, err := svc.GetConversation(ctx, "acct-1", thread.ID); err != ErrNotFound {
		t.Fatalf("deleted thread still visible: %v", err)
	}
	// Soft delete: the rows are hidden from the live queries but kept on disk.
	assertSoftDeletedCount(t, svc, &database.ConversationMessage{}, "thread_id = ?", thread.ID)
	assertSoftDeletedCount(t, svc, &database.ConversationRun{}, "thread_id = ?", thread.ID)
	assertSoftDeletedCount(t, svc, &database.ConversationThread{}, "id = ?", thread.ID)

	if err := svc.DeleteConversation(ctx, "acct-1", thread.ID); err != ErrNotFound {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}

func TestDeleteConversationHidesAnotherAccountsThreadAsNotFound(t *testing.T) {
	svc := newConversationGroupTestService(t)
	thread := createTestThread(t, svc, "acct-2", newID())

	if err := svc.DeleteConversation(t.Context(), "acct-1", thread.ID); err != ErrNotFound {
		t.Fatalf("foreign delete = %v, want ErrNotFound", err)
	}
	if _, err := svc.GetConversation(t.Context(), "acct-2", thread.ID); err != nil {
		t.Fatalf("foreign thread was deleted: %v", err)
	}
}

func TestDeleteConversationsSkipsForeignAndUnknownIDs(t *testing.T) {
	svc := newConversationGroupTestService(t)
	ctx := t.Context()
	mine := createTestThread(t, svc, "acct-1", newID())
	theirs := createTestThread(t, svc, "acct-2", newID())

	deleted, err := svc.DeleteConversations(ctx, "acct-1", []string{mine.ID, theirs.ID, "missing", mine.ID})
	if err != nil {
		t.Fatalf("DeleteConversations() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, err := svc.GetConversation(ctx, "acct-1", mine.ID); err != ErrNotFound {
		t.Fatalf("owned thread survived batch delete: %v", err)
	}
	if _, err := svc.GetConversation(ctx, "acct-2", theirs.ID); err != nil {
		t.Fatalf("another account's thread was deleted: %v", err)
	}
	if _, err := svc.DeleteConversations(ctx, "acct-1", []string{"", "  "}); err == nil {
		t.Fatal("expected an empty id list to be rejected")
	}
}

func TestConversationGroupCRUDEnforcesOwnership(t *testing.T) {
	svc := newConversationGroupTestService(t)
	ctx := t.Context()

	if _, err := svc.CreateConversationGroup(ctx, "acct-1", ConversationGroupInput{Name: "   "}); err == nil {
		t.Fatal("expected an empty name to be rejected")
	}
	group := mustCreateGroup(t, svc, "acct-1", "  Work  ")
	if group.Name != "Work" {
		t.Fatalf("created name = %q, want trimmed %q", group.Name, "Work")
	}

	// Another account can neither read, update, nor delete it.
	if _, err := svc.UpdateConversationGroup(ctx, "acct-2", group.ID, ConversationGroupUpdateInput{Name: new("Hijacked")}); err != ErrForbidden {
		t.Fatalf("foreign update = %v, want ErrForbidden", err)
	}
	if err := svc.DeleteConversationGroup(ctx, "acct-2", group.ID); err != ErrForbidden {
		t.Fatalf("foreign delete = %v, want ErrForbidden", err)
	}
	if _, err := svc.UpdateConversationGroup(ctx, "acct-1", "missing", ConversationGroupUpdateInput{Name: new("X")}); err != ErrNotFound {
		t.Fatalf("unknown update = %v, want ErrNotFound", err)
	}
	if err := svc.DeleteConversationGroup(ctx, "acct-1", "missing"); err != ErrNotFound {
		t.Fatalf("unknown delete = %v, want ErrNotFound", err)
	}

	// PATCH needs at least one usable field, and an empty name is rejected.
	if _, err := svc.UpdateConversationGroup(ctx, "acct-1", group.ID, ConversationGroupUpdateInput{}); err == nil {
		t.Fatal("expected an empty patch to be rejected")
	}
	if _, err := svc.UpdateConversationGroup(ctx, "acct-1", group.ID, ConversationGroupUpdateInput{Name: new("  ")}); err == nil {
		t.Fatal("expected an empty name to be rejected")
	}
	updated, err := svc.UpdateConversationGroup(ctx, "acct-1", group.ID, ConversationGroupUpdateInput{Name: new("Travel"), Description: new("Trips")})
	if err != nil {
		t.Fatalf("UpdateConversationGroup() error = %v", err)
	}
	if updated.Name != "Travel" || updated.Description != "Trips" {
		t.Fatalf("updated group = %#v", updated)
	}

	groups, err := svc.ListConversationGroups(ctx, "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].ID != group.ID {
		t.Fatalf("listed groups = %#v, want the one created", groups)
	}
	other, err := svc.ListConversationGroups(ctx, "acct-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("another account sees groups: %#v", other)
	}

	if err := svc.DeleteConversationGroup(ctx, "acct-1", group.ID); err != nil {
		t.Fatalf("DeleteConversationGroup() error = %v", err)
	}
	if err := svc.DeleteConversationGroup(ctx, "acct-1", group.ID); err != ErrNotFound {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}

func TestSetConversationGroupAssignsClearsAndPinsRetroactively(t *testing.T) {
	svc := newConversationGroupTestService(t)
	ctx := t.Context()
	thread := createTestThread(t, svc, "acct-1", newID())
	message := &database.ConversationMessage{ID: newID(), ThreadID: thread.ID, AccountID: "acct-1", Role: "user", Content: "My name is Jamie.", Sequence: 1}
	if err := svc.db.Create(message).Error; err != nil {
		t.Fatal(err)
	}
	memory := &database.AgentMemory{
		ID: newID(), AccountID: "acct-1", AgentID: "mochi", Scope: "user",
		Category: "name", Key: "name", Content: "The user's name is Jamie.",
		Status: "active", SourceMessageID: message.ID,
	}
	if err := svc.db.Create(memory).Error; err != nil {
		t.Fatal(err)
	}

	group := mustCreateGroup(t, svc, "acct-1", "Important")

	updated, err := svc.SetConversationGroup(ctx, "acct-1", []string{thread.ID, thread.ID, "missing"}, group.ID)
	if err != nil {
		t.Fatalf("SetConversationGroup() error = %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}
	stored, err := svc.GetConversation(ctx, "acct-1", thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.GroupID == nil || *stored.GroupID != group.ID {
		t.Fatalf("thread group_id = %v, want %q", stored.GroupID, group.ID)
	}
	// Assigning the thread retroactively pins memories learned from it.
	var pinned database.AgentMemory
	if err := svc.db.First(&pinned, "id = ?", memory.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !pinned.Pinned || pinned.GroupID != group.ID {
		t.Fatalf("memory not pinned retroactively: %#v", pinned)
	}

	// A group that is not the caller's is rejected before anything is written.
	if _, err := svc.SetConversationGroup(ctx, "acct-1", []string{thread.ID}, "not-a-group"); err == nil {
		t.Fatal("expected an unknown group to be rejected")
	}

	// Clearing membership leaves the thread alive and does not release the pin.
	if _, err := svc.SetConversationGroup(ctx, "acct-1", []string{thread.ID}, ""); err != nil {
		t.Fatalf("clear group error = %v", err)
	}
	stored, err = svc.GetConversation(ctx, "acct-1", thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.GroupID != nil {
		t.Fatalf("thread group_id = %v, want nil after clearing", stored.GroupID)
	}
	if err := svc.db.First(&pinned, "id = ?", memory.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !pinned.Pinned {
		t.Fatal("clearing membership must not release the retention")
	}

	// Deleting the group releases the retention and ungroups the thread.
	if _, err := svc.SetConversationGroup(ctx, "acct-1", []string{thread.ID}, group.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteConversationGroup(ctx, "acct-1", group.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.db.First(&pinned, "id = ?", memory.ID).Error; err != nil {
		t.Fatal(err)
	}
	if pinned.Pinned || pinned.GroupID != "" {
		t.Fatalf("memory retention not released: %#v", pinned)
	}
	stored, err = svc.GetConversation(ctx, "acct-1", thread.ID)
	if err != nil {
		t.Fatalf("thread was deleted with its group: %v", err)
	}
	if stored.GroupID != nil {
		t.Fatalf("thread group_id = %v after group delete, want nil", stored.GroupID)
	}
}

func TestListConversationGroupsCountsLiveThreadsOnly(t *testing.T) {
	svc := newConversationGroupTestService(t)
	ctx := t.Context()
	group := mustCreateGroup(t, svc, "acct-1", "Work")
	kept := createTestThread(t, svc, "acct-1", newID())
	removed := createTestThread(t, svc, "acct-1", newID())
	for _, thread := range []*database.ConversationThread{kept, removed} {
		if _, err := svc.SetConversationGroup(ctx, "acct-1", []string{thread.ID}, group.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.DeleteConversation(ctx, "acct-1", removed.ID); err != nil {
		t.Fatal(err)
	}

	groups, err := svc.ListConversationGroups(ctx, "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].ConversationCount != 1 {
		t.Fatalf("group counts = %#v, want 1 live thread", groups)
	}
}

func TestMemoryRetentionForMapsGroupMembership(t *testing.T) {
	groupID := "group-1"
	blank := "  "
	cases := []struct {
		name   string
		thread *database.ConversationThread
		pinned bool
		group  string
	}{
		{"nil thread", nil, false, ""},
		{"ungrouped", &database.ConversationThread{}, false, ""},
		{"blank group", &database.ConversationThread{GroupID: &blank}, false, ""},
		{"grouped", &database.ConversationThread{GroupID: &groupID}, true, groupID},
	}
	for _, tc := range cases {
		got := memoryRetentionFor(tc.thread)
		if got.Pinned != tc.pinned || got.GroupID != tc.group {
			t.Fatalf("%s: memoryRetentionFor = %#v, want pinned=%v group=%q", tc.name, got, tc.pinned, tc.group)
		}
	}
}

func assertSoftDeletedCount(t *testing.T, svc *ConversationService, model any, query string, args ...any) {
	t.Helper()
	var live, total int64
	if err := svc.db.Model(model).Where(query, args...).Count(&live).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.db.Unscoped().Model(model).Where(query, args...).Count(&total).Error; err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("expected no live rows, got %d", live)
	}
	if total == 0 {
		t.Fatalf("expected the soft-deleted rows to remain on disk")
	}
}
