package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/personality/internal/service"
)

func RegisterImageSummaryRoutes(r *gin.RouterGroup, conversations *service.ConversationService) {
	r.GET("/files/:id/summary", func(c *gin.Context) { getFileSummary(c, conversations) })
	r.POST("/files/summary", func(c *gin.Context) { generateFileSummary(c, conversations) })
}

func getFileSummary(c *gin.Context, conversations *service.ConversationService) {
	attachmentID := strings.TrimSpace(c.Param("id"))
	if attachmentID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}

	summary, err := conversations.GetImageSummary(c.Request.Context(), attachmentID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if summary == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "summary not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"attachment_id": attachmentID, "summary": summary})
}

func generateFileSummary(c *gin.Context, conversations *service.ConversationService) {
	var req struct {
		AttachmentID string `json:"attachment_id"`
		ImageURL     string `json:"image_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	attachmentID := strings.TrimSpace(req.AttachmentID)
	imageURL := strings.TrimSpace(req.ImageURL)
	if attachmentID == "" && imageURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "attachment_id or image_url is required"})
		return
	}

	// resolve imageURL from attachmentID if not provided
	if imageURL == "" {
		baseURL := strings.TrimSpace(conversations.SolarBaseURL())
		if baseURL == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "image_url is required when solar baseUrl is not configured"})
			return
		}
		imageURL = strings.TrimRight(baseURL, "/") + "/drive/files/" + attachmentID
	}

	summary, err := conversations.SummarizeAndCacheImage(c.Request.Context(), imageURL, attachmentID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"attachment_id": attachmentID, "summary": summary})
}
