package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/personality/internal/identity"
	"src.solsynth.dev/sosys/personality/internal/service"
)

func RegisterOpenAICredentialRoutes(r *gin.RouterGroup, conversations *service.ConversationService) {
	r.GET("", func(c *gin.Context) { listOpenAICredentials(c, conversations) })
	r.POST("", func(c *gin.Context) { createOpenAICredential(c, conversations) })
	r.DELETE("/:id", func(c *gin.Context) { revokeOpenAICredential(c, conversations) })
}

type openAICredentialRequest struct {
	Name          string   `json:"name"`
	AgentIDs      []string `json:"agent_ids"`
	Providers     []string `json:"providers"`
	Models        []string `json:"models"`
	UsageLimit    string   `json:"usage_limit"`
	UsageCurrency string   `json:"usage_currency"`
}

func listOpenAICredentials(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	items, err := conversations.ListOpenAICredentials(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func createOpenAICredential(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	var req openAICredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	created, err := conversations.CreateOpenAICredential(c.Request.Context(), accountID, service.CreateOpenAICredentialInput{Name: req.Name, AgentIDs: req.AgentIDs, Providers: req.Providers, Models: req.Models, UsageLimit: req.UsageLimit, UsageCurrency: req.UsageCurrency})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, created)
}

func revokeOpenAICredential(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	if err := conversations.RevokeOpenAICredential(c.Request.Context(), accountID, strings.TrimSpace(c.Param("id"))); err != nil {
		if errors.Is(err, service.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
