package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/persona/internal/identity"
	"src.solsynth.dev/sosys/persona/internal/service"
)

// RegisterBillingRoutes exposes the account owner's billing controls. The
// spending quota is the maximum unpaid gold balance allowed before Personality
// submits an immediate Wallet transaction; zero restores daily-only settlement.
func RegisterBillingRoutes(r *gin.RouterGroup, conversations *service.ConversationService) {
	r.GET("/me", func(c *gin.Context) { getMyBilling(c, conversations) })
	r.PUT("/me/spending-quota", func(c *gin.Context) { setMySpendingQuota(c, conversations) })
	r.POST("/me/settle", func(c *gin.Context) { settleMyBilling(c, conversations) })
	r.GET("/me/ledger", func(c *gin.Context) { listMyBillingLedger(c, conversations) })
	r.GET("/me/ledger/summary", func(c *gin.Context) { getMyBillingLedgerSummary(c, conversations) })
}

// parseLedgerQuery reads the shared audit filters from the request query
// string. accountID pins the query to one account: the self surface passes the
// caller's own id, the admin surface the account in the path.
func parseLedgerQuery(c *gin.Context, accountID string) (service.BillingLedgerQuery, error) {
	query := service.BillingLedgerQuery{
		AccountID:    accountID,
		Action:       c.Query("action"),
		Model:        c.Query("model"),
		Surface:      c.Query("surface"),
		Currency:     c.Query("currency"),
		CredentialID: c.Query("credential_id"),
		ClientIP:     c.Query("client_ip"),
		DeviceID:     c.Query("device_id"),
		RunID:        c.Query("run_id"),
		UnpaidOnly:   parseBoolQuery(c, "unpaid"),
	}
	list := parseListInput(c)
	query.Take, query.Offset = list.Take, list.Offset
	from, err := parseTimeQuery(c, "from")
	if err != nil {
		return query, err
	}
	to, err := parseTimeQuery(c, "to")
	if err != nil {
		return query, err
	}
	query.From, query.To = from, to
	return query, nil
}

func parseTimeQuery(c *gin.Context, name string) (*time.Time, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be an RFC3339 timestamp", name)
	}
	return &parsed, nil
}

func parseBoolQuery(c *gin.Context, name string) bool {
	switch strings.ToLower(strings.TrimSpace(c.Query(name))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func listMyBillingLedger(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	query, err := parseLedgerQuery(c, accountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	entries, total, err := conversations.Billing().Ledger(c.Request.Context(), query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Header("X-Total", strconv.FormatInt(total, 10))
	c.JSON(http.StatusOK, entries)
}

func getMyBillingLedgerSummary(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	query, err := parseLedgerQuery(c, accountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	summary, err := conversations.Billing().LedgerSummary(c.Request.Context(), query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, summary)
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
