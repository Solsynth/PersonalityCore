package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/personality/internal/identity"
	"src.solsynth.dev/sosys/personality/internal/service"
)

func RegisterMemoryRoutes(r *gin.RouterGroup, conversations *service.ConversationService) {
	memories := r.Group("/memories")
	memories.GET("", func(c *gin.Context) { listMemories(c, conversations) })
	memories.POST("", func(c *gin.Context) { saveMemory(c, conversations) })
	memories.DELETE("/:id", func(c *gin.Context) { deleteMemory(c, conversations) })
}

type memoryRequest struct {
	AgentID    string  `json:"agent_id"`
	Scope      string  `json:"scope"`
	Category   string  `json:"category"`
	Key        string  `json:"key"`
	Content    string  `json:"content"`
	Confidence float32 `json:"confidence"`
	Confirmed  bool    `json:"confirmed"`
}

func listMemories(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(c.Query("agent_id"))
	if agentID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent_id is required"})
		return
	}
	limit := 20
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive integer"})
			return
		}
		limit = parsed
	}
	items, err := conversations.ListMemories(c.Request.Context(), accountID, agentID, c.Query("q"), limit)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

func saveMemory(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	var request memoryRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	record, err := conversations.SaveMemory(c.Request.Context(), accountID, request.AgentID, service.MemoryInput{
		Scope:      request.Scope,
		Category:   request.Category,
		Key:        request.Key,
		Content:    request.Content,
		Confidence: request.Confidence,
		Confirmed:  request.Confirmed,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, record)
}

func deleteMemory(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(c.Query("agent_id"))
	if agentID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent_id is required"})
		return
	}
	if err := conversations.ForgetMemory(c.Request.Context(), accountID, agentID, c.Param("id")); err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
