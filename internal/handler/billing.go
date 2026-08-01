package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/personality/internal/identity"
	"src.solsynth.dev/sosys/personality/internal/service"
)

// RegisterBillingRoutes exposes the account owner's billing controls. The
// spending quota is the maximum unpaid gold balance allowed before Personality
// submits an immediate Wallet transaction; zero restores daily-only settlement.
func RegisterBillingRoutes(r *gin.RouterGroup, conversations *service.ConversationService) {
	r.GET("/me", func(c *gin.Context) { getMyBilling(c, conversations) })
	r.PUT("/me/spending-quota", func(c *gin.Context) { setMySpendingQuota(c, conversations) })
	r.POST("/me/settle", func(c *gin.Context) { settleMyBilling(c, conversations) })
}

func settleMyBilling(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	result, err := conversations.Billing().SettleAccount(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(http.StatusPaymentRequired, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

func getMyBilling(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	policy, err := conversations.Billing().AccountPolicy(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	usage, err := conversations.Billing().UsageSummary(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"hourly_run_limit":   policy.HourlyRunLimit,
		"daily_run_limit":    policy.DailyRunLimit,
		"hourly_usage_limits": policy.HourlyUsageLimits,
		"daily_usage_limits":  policy.DailyUsageLimits,
		"spending_quota":      policy.InstantBillingWall,
		"blacklisted":         policy.Blacklisted,
		"usage":               usage,
	})
}

func setMySpendingQuota(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	var req struct {
		SpendingQuota *string `json:"spending_quota"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.SpendingQuota == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "spending_quota is required"})
		return
	}
	policy, err := conversations.Billing().AccountPolicy(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	policy.InstantBillingWall = req.SpendingQuota
	if _, err = conversations.Billing().UpsertAccountPolicy(c.Request.Context(), policy); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"spending_quota": policy.InstantBillingWall})
}
