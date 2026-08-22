package identity

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	sharedauth "src.solsynth.dev/sosys/go/pkg/auth"
)

const accountIDKey = "account_id"
const perkLevelKey = "perk_level"
const accountNameKey = "account_name"
const accountNickKey = "account_nick"

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

// SetAccountProfile records the authenticated account's display identity so
// handlers can pass it into prompt assembly without an extra lookup.
func SetAccountProfile(c *gin.Context, name, nick string) {
	c.Set(accountNameKey, strings.TrimSpace(name))
	c.Set(accountNickKey, strings.TrimSpace(nick))
}

// GetAccountProfile returns the authenticated account's name and nick, or
// empty strings when auth did not provide them.
func GetAccountProfile(c *gin.Context) (string, string) {
	name, _ := c.Get(accountNameKey)
	nick, _ := c.Get(accountNickKey)
	nameStr, _ := name.(string)
	nickStr, _ := nick.(string)
	return strings.TrimSpace(nameStr), strings.TrimSpace(nickStr)
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
