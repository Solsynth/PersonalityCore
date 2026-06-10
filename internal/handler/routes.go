package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/personality/internal/identity"
	"src.solsynth.dev/sosys/personality/internal/service"
)

func RegisterRoutes(r *gin.RouterGroup, conversations *service.ConversationService) {
	r.GET("/agents", func(c *gin.Context) { listAgents(c, conversations) })
	r.GET("/agents/:id", func(c *gin.Context) { getAgent(c, conversations) })

	conv := r.Group("/conversations")
	{
		conv.POST("", func(c *gin.Context) { createConversation(c, conversations) })
		conv.GET("", func(c *gin.Context) { listConversations(c, conversations) })
		conv.GET("/:id", func(c *gin.Context) { getConversation(c, conversations) })
		conv.GET("/:id/messages", func(c *gin.Context) { listMessages(c, conversations) })
		conv.POST("/:id/messages", func(c *gin.Context) { addMessage(c, conversations) })
		conv.POST("/:id/runs", func(c *gin.Context) { createRun(c, conversations) })
		conv.GET("/:id/runs", func(c *gin.Context) { listRuns(c, conversations) })
		conv.GET("/:id/runs/:runId", func(c *gin.Context) { getRun(c, conversations) })
	}
}

func listAgents(c *gin.Context, conversations *service.ConversationService) {
	c.JSON(http.StatusOK, conversations.ListAgents())
}

func getAgent(c *gin.Context, conversations *service.ConversationService) {
	agent, ok := conversations.GetAgent(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "agent not found"})
		return
	}
	c.JSON(http.StatusOK, agent)
}

func createConversation(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}

	var input service.CreateConversationInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	thread, err := conversations.CreateConversation(c.Request.Context(), accountID, input)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, thread)
}

func listConversations(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}

	input := parseListInput(c)
	items, total, err := conversations.ListConversations(c.Request.Context(), accountID, input)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Header("X-Total", strconv.FormatInt(total, 10))
	c.JSON(http.StatusOK, items)
}

func getConversation(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}

	thread, err := conversations.GetConversation(c.Request.Context(), accountID, c.Param("id"))
	if err != nil {
		renderServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, thread)
}

func listMessages(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}

	input := parseListInput(c)
	items, total, err := conversations.ListMessages(c.Request.Context(), accountID, c.Param("id"), input)
	if err != nil {
		renderServiceError(c, err)
		return
	}
	c.Header("X-Total", strconv.FormatInt(total, 10))
	c.JSON(http.StatusOK, items)
}

func addMessage(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}

	var input service.AddMessageInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	message, err := conversations.AddUserMessage(c.Request.Context(), accountID, c.Param("id"), input)
	if err != nil {
		renderServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, message)
}

func createRun(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}

	var input service.RunInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if input.Stream {
		streamRun(c, conversations, accountID, input)
		return
	}

	result, err := conversations.ExecuteRun(c.Request.Context(), accountID, c.Param("id"), input)
	if err != nil {
		renderServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func streamRun(c *gin.Context, conversations *service.ConversationService, accountID string, input service.RunInput) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Flush()

	writeSSE(c, "run.started", gin.H{"conversation_id": c.Param("id")})

	done := make(chan struct{})
	go heartbeat(c, 15*time.Second, done)
	defer close(done)

	result, err := conversations.StreamRun(c.Request.Context(), accountID, c.Param("id"), input, service.StreamCallbacks{
		OnChunk: func(chunk string) error {
			writeSSE(c, "message.delta", gin.H{"delta": chunk})
			return nil
		},
	})
	if err != nil {
		writeSSE(c, "run.failed", gin.H{"error": err.Error()})
		return
	}

	writeSSE(c, "message.completed", gin.H{"content": result.ResponseContent, "message_id": result.ResponseMessage.ID})
	writeSSE(c, "run.completed", gin.H{"run_id": result.Run.ID, "message_id": result.ResponseMessage.ID})
}

func listRuns(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}

	input := parseListInput(c)
	items, total, err := conversations.ListRuns(c.Request.Context(), accountID, c.Param("id"), input)
	if err != nil {
		renderServiceError(c, err)
		return
	}
	c.Header("X-Total", strconv.FormatInt(total, 10))
	c.JSON(http.StatusOK, items)
}

func getRun(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}

	run, err := conversations.GetRun(c.Request.Context(), accountID, c.Param("id"), c.Param("runId"))
	if err != nil {
		renderServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, run)
}

func parseListInput(c *gin.Context) service.ListInput {
	take := 20
	offset := 0
	if parsed, err := strconv.Atoi(c.DefaultQuery("take", "20")); err == nil && parsed > 0 && parsed <= 200 {
		take = parsed
	}
	if parsed, err := strconv.Atoi(c.DefaultQuery("offset", "0")); err == nil && parsed >= 0 {
		offset = parsed
	}
	return service.ListInput{Take: take, Offset: offset}
}

func renderServiceError(c *gin.Context, err error) {
	switch err {
	case service.ErrNotFound:
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case service.ErrForbidden:
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
