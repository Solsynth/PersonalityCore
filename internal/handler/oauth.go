package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/personality/internal/identity"
	"src.solsynth.dev/sosys/personality/internal/service"
)

// registerOAuthRoutes mounts the user OAuth device-flow routes under the
// authenticated API group. These are thin passthroughs to OAuthService.
func registerOAuthRoutes(r *gin.RouterGroup, conversations *service.ConversationService) {
	r.POST("/agents/:id/oauth/device", func(c *gin.Context) {
		startOAuthDeviceFlow(c, conversations)
	})
	r.GET("/agents/:id/oauth/status", func(c *gin.Context) {
		getOAuthStatus(c, conversations)
	})
	r.DELETE("/agents/:id/oauth", func(c *gin.Context) {
		revokeOAuth(c, conversations)
	})
}

func startOAuthDeviceFlow(c *gin.Context, conversations *service.ConversationService) {
	agentID := c.Param("id")
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	if strings.TrimSpace(agentID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent id is required"})
		return
	}
	info, err := conversations.StartOAuthDeviceFlow(c.Request.Context(), agentID, accountID)
	if err != nil {
		renderServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, info)
}

func getOAuthStatus(c *gin.Context, conversations *service.ConversationService) {
	agentID := c.Param("id")
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	status, scopes, expiresAt, err := conversations.OAuthStatus(c.Request.Context(), agentID, accountID)
	if err != nil {
		renderServiceError(c, err)
		return
	}
	resp := gin.H{
		"status":    status,
		"account_id": accountID,
	}
	if scopes != "" {
		resp["scopes"] = scopes
	}
	if expiresAt != nil {
		resp["expires_at"] = expiresAt
	}
	c.JSON(http.StatusOK, resp)
}

func revokeOAuth(c *gin.Context, conversations *service.ConversationService) {
	agentID := c.Param("id")
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	if err := conversations.RevokeOAuth(c.Request.Context(), agentID, accountID); err != nil {
		renderServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
