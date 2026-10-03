package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/persona/internal/service"
)

func TestConversationDeleteEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAgentTestService(t)
	ctx := t.Context()
	mine, err := svc.CreateConversation(ctx, "acct-1", service.CreateConversationInput{AgentID: "mochi", Title: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := svc.CreateConversation(ctx, "acct-2", service.CreateConversationInput{AgentID: "mochi", Title: "Theirs"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddUserMessage(ctx, "acct-1", mine.ID, service.AddMessageInput{Content: "hi"}); err != nil {
		t.Fatal(err)
	}

	r := newAgentTestRouter(svc, "acct-1")

	// Batch delete skips the other account's thread and reports one removal.
	body := `{"ids":["` + mine.ID + `","` + theirs.ID + `","missing"]}`
	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/conversations/batch-delete", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("batch-delete status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var batch struct {
		Deleted int `json:"deleted"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	if batch.Deleted != 1 {
		t.Fatalf("deleted = %d, want 1", batch.Deleted)
	}
	if _, err := svc.GetConversation(ctx, "acct-2", theirs.ID); err != nil {
		t.Fatalf("another account's thread was deleted: %v", err)
	}

	// Single delete is 404 for an id that is already gone.
	response = httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/conversations/"+mine.ID, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("delete status = %d, want 404; body = %s", response.Code, response.Body.String())
	}

	// Empty ids are a 400, not a silent no-op.
	response = httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/conversations/batch-delete", strings.NewReader(`{"ids":["  "]}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("empty batch status = %d, want 400", response.Code)
	}
}

func TestConversationGroupEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAgentTestService(t)
	r := newAgentTestRouter(svc, "acct-1")

	create := httptest.NewRecorder()
	r.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/conversation-groups", strings.NewReader(`{"name":"Work","description":"Things"}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body = %s", create.Code, create.Body.String())
	}
	var group struct {
		ID                string `json:"id"`
		Name              string `json:"name"`
		ConversationCount int    `json:"conversation_count"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &group); err != nil {
		t.Fatal(err)
	}
	if group.ID == "" || group.Name != "Work" || group.ConversationCount != 0 {
		t.Fatalf("unexpected created group: %#v", group)
	}

	// An empty name is a 400.
	bad := httptest.NewRecorder()
	r.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/api/conversation-groups", strings.NewReader(`{"name":""}`)))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("empty-name status = %d, want 400", bad.Code)
	}

	list := httptest.NewRecorder()
	r.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/conversation-groups", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), group.ID) {
		t.Fatalf("list status = %d body = %s", list.Code, list.Body.String())
	}

	patch := httptest.NewRecorder()
	r.ServeHTTP(patch, httptest.NewRequest(http.MethodPatch, "/api/conversation-groups/"+group.ID, strings.NewReader(`{"name":"Travel"}`)))
	if patch.Code != http.StatusOK || !strings.Contains(patch.Body.String(), "Travel") {
		t.Fatalf("patch status = %d body = %s", patch.Code, patch.Body.String())
	}

	// Another account cannot touch it; an unknown id is a 404.
	other := newAgentTestRouter(svc, "acct-2")
	forbidden := httptest.NewRecorder()
	other.ServeHTTP(forbidden, httptest.NewRequest(http.MethodPatch, "/api/conversation-groups/"+group.ID, strings.NewReader(`{"name":"Hijack"}`)))
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("foreign patch status = %d, want 403", forbidden.Code)
	}
	missing := httptest.NewRecorder()
	r.ServeHTTP(missing, httptest.NewRequest(http.MethodDelete, "/api/conversation-groups/missing", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown delete status = %d, want 404", missing.Code)
	}

	// Assign a thread through the endpoint, then delete the group.
	thread, err := svc.CreateConversation(t.Context(), "acct-1", service.CreateConversationInput{AgentID: "mochi", Title: "Chat"})
	if err != nil {
		t.Fatal(err)
	}
	assign := httptest.NewRecorder()
	r.ServeHTTP(assign, httptest.NewRequest(http.MethodPost, "/api/conversations/group", strings.NewReader(`{"ids":["`+thread.ID+`"],"group_id":"`+group.ID+`"}`)))
	if assign.Code != http.StatusOK || !strings.Contains(assign.Body.String(), `"updated":1`) {
		t.Fatalf("assign status = %d body = %s", assign.Code, assign.Body.String())
	}

	del := httptest.NewRecorder()
	r.ServeHTTP(del, httptest.NewRequest(http.MethodDelete, "/api/conversation-groups/"+group.ID, nil))
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete group status = %d, want 204; body = %s", del.Code, del.Body.String())
	}
}
