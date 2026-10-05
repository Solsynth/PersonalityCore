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
	r.GET("/web/search/engines", func(c *gin.Context) { listWebSearchEngines(c, conversations) })
	r.GET("/web/search/preference", func(c *gin.Context) { getWebSearchPreference(c, conversations) })
	r.PUT("/web/search/preference", func(c *gin.Context) { putWebSearchPreference(c, conversations) })
}

// listWebSearchEngines is the picker's data: the engines a caller may keep its
// searches on, each with what one query costs, in the server's billing
// currency. Engines that cost nothing come back marked free.
func listWebSearchEngines(c *gin.Context, conversations *service.ConversationService) {
	if _, ok := identity.RequireAccountID(c); !ok {
		return
	}
	engines, err := conversations.WebSearchEngines()
	if err != nil {
		if errors.Is(err, websearch.ErrNotConfigured) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, engines)
}

func getWebSearchPreference(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	preference, err := conversations.WebSearchPreference(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, preference)
}

// putWebSearchPreference moves an account's searches onto one engine. An empty
// engine hands the choice back to the server; anything else must name a
// configured engine, so the account cannot be left paying for an engine it did
// not choose.
func putWebSearchPreference(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	var request struct {
		Engine *string `json:"engine"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if request.Engine == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "engine is required"})
		return
	}
	preference, err := conversations.SetWebSearchPreference(c.Request.Context(), accountID, *request.Engine)
	if err != nil {
		switch {
		case errors.Is(err, websearch.ErrNotConfigured):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		case errors.Is(err, websearch.ErrInvalidQuery):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, preference)
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
