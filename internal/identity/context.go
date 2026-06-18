package identity

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	sharedauth "src.solsynth.dev/sosys/go/pkg/auth"
)

const accountIDKey = "account_id"
const perkLevelKey = "perk_level"

func RequireAccountID(c *gin.Context) (string, bool) {
	value, ok := c.Get(accountIDKey)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return "", false
	}
	accountID, _ := value.(string)
	if strings.TrimSpace(accountID) == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return "", false
	}
	return accountID, true
}

func SetAccountID(c *gin.Context, accountID string) {
	c.Set(accountIDKey, strings.TrimSpace(accountID))
}

func SetPerkLevel(c *gin.Context, level int32) {
	c.Set(perkLevelKey, level)
}

func GetPerkLevel(c *gin.Context) int32 {
	v, ok := c.Get(perkLevelKey)
	if !ok {
		return 0
	}
	level, _ := v.(int32)
	return level
}

func ExtractAccountIDFromAuth(c *gin.Context) (string, bool) {
	result, _, ok := sharedauth.GetAuth(c)
	if !ok || result == nil || result.Account == nil {
		return "", false
	}
	accountID := strings.TrimSpace(result.Account.GetId())
	return accountID, accountID != ""
}

func ExtractPerkLevelFromAuth(c *gin.Context) int32 {
	result, _, ok := sharedauth.GetAuth(c)
	if !ok || result == nil || result.Account == nil {
		return 0
	}
	return result.Account.GetPerkLevel()
}
