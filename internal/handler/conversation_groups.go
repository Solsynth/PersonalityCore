package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/persona/internal/identity"
	"src.solsynth.dev/sosys/persona/internal/service"
)

func deleteConversation(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	if err := conversations.DeleteConversation(c.Request.Context(), accountID, c.Param("id")); err != nil {
		renderServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func batchDeleteConversations(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	var input struct {
		IDs []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	deleted, err := conversations.DeleteConversations(c.Request.Context(), accountID, input.IDs)
	if err != nil {
		renderServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
}

func setConversationsGroup(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	var input struct {
		IDs     []string `json:"ids"`
		GroupID string   `json:"group_id"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated, err := conversations.SetConversationGroup(c.Request.Context(), accountID, input.IDs, input.GroupID)
	if err != nil {
		renderServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": updated})
}

func listConversationGroups(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	groups, err := conversations.ListConversationGroups(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, groups)
}

func createConversationGroup(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	var input service.ConversationGroupInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	group, err := conversations.CreateConversationGroup(c.Request.Context(), accountID, input)
	if err != nil {
		renderServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, group)
}

func updateConversationGroup(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	var input service.ConversationGroupUpdateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	group, err := conversations.UpdateConversationGroup(c.Request.Context(), accountID, c.Param("id"), input)
	if err != nil {
		renderServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, group)
}

func deleteConversationGroup(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	if err := conversations.DeleteConversationGroup(c.Request.Context(), accountID, c.Param("id")); err != nil {
		renderServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
