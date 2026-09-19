package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/persona/internal/identity"
	"src.solsynth.dev/sosys/persona/internal/service"
	"src.solsynth.dev/sosys/persona/internal/websearch"
)

func RegisterWebSearchRoutes(r *gin.RouterGroup, conversations *service.ConversationService) {
	r.POST("/web/search", func(c *gin.Context) { webSearch(c, conversations) })
}

// webSearch exposes the configured search providers directly, without a model
// in the loop. It is available whenever webSearch is enabled server-side; the
// per-agent `web_search` ability only gates the tool.
func webSearch(c *gin.Context, conversations *service.ConversationService) {
	var input service.WebSearchInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(input.Query) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query is required"})
		return
	}
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}

	response, err := conversations.SearchWeb(c.Request.Context(), accountID, input)
	if err != nil {
		switch {
		case errors.Is(err, websearch.ErrNotConfigured):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		case errors.Is(err, websearch.ErrInvalidQuery):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, service.ErrBillingBlacklisted),
			errors.Is(err, service.ErrBillingQuotaExceeded),
			errors.Is(err, service.ErrPaymentWalletRequired):
			// API-backed search costs golds, so the account must be able to pay.
			c.JSON(http.StatusPaymentRequired, gin.H{"error": err.Error()})
		default:
			// Every engine failed; the upstream is the faulting party.
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, response)
}
