package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	sharedauth "src.solsynth.dev/sosys/go/pkg/auth"

	"src.solsynth.dev/sosys/personality/internal/identity"
	"src.solsynth.dev/sosys/personality/internal/service"
)

func RegisterBillingAdminRoutes(r *gin.RouterGroup, conversations *service.ConversationService) {
	r.GET("/accounts/:accountId", func(c *gin.Context) { getBillingAccount(c, conversations) })
	r.PUT("/accounts/:accountId", func(c *gin.Context) { putBillingAccount(c, conversations) })
	r.POST("/accounts/:accountId/unblacklist", func(c *gin.Context) { unblacklistBillingAccount(c, conversations) })
}

func requireBillingAdmin(c *gin.Context, conversations *service.ConversationService) bool {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return false
	}
	if result, _, ok := sharedauth.GetAuth(c); ok && result != nil && result.Account != nil && result.Account.GetIsSuperuser() {
		return true
	}
	if err := conversations.RequireAccountPermission(c.Request.Context(), accountID, service.PermissionBillingManage); err != nil {
		if errors.Is(err, service.ErrPermissionDenied) {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return false
	}
	return true
}

func getBillingAccount(c *gin.Context, conversations *service.ConversationService) {
	if !requireBillingAdmin(c, conversations) {
		return
	}
	policy, err := conversations.Billing().AccountPolicy(c.Request.Context(), c.Param("accountId"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, policy)
}

func putBillingAccount(c *gin.Context, conversations *service.ConversationService) {
	if !requireBillingAdmin(c, conversations) {
		return
	}
	var req struct {
		HourlyRunLimit     *int    `json:"hourly_run_limit"`
		DailyRunLimit      *int    `json:"daily_run_limit"`
		InstantBillingWall *string `json:"instant_billing_wall"`
		Blacklisted        *bool   `json:"blacklisted"`
		BlacklistReason    *string `json:"blacklist_reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	policy, err := conversations.Billing().AccountPolicy(c.Request.Context(), c.Param("accountId"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	policy.HourlyRunLimit, policy.DailyRunLimit, policy.InstantBillingWall = req.HourlyRunLimit, req.DailyRunLimit, req.InstantBillingWall
	if req.Blacklisted != nil {
		policy.Blacklisted = *req.Blacklisted
	}
	if req.BlacklistReason != nil {
		policy.BlacklistReason = *req.BlacklistReason
	}
	policy, err = conversations.Billing().UpsertAccountPolicy(c.Request.Context(), policy)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, policy)
}

func unblacklistBillingAccount(c *gin.Context, conversations *service.ConversationService) {
	if !requireBillingAdmin(c, conversations) {
		return
	}
	policy, err := conversations.Billing().AccountPolicy(c.Request.Context(), c.Param("accountId"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	policy.Blacklisted, policy.BlacklistReason = false, ""
	policy, err = conversations.Billing().UpsertAccountPolicy(c.Request.Context(), policy)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, policy)
}
