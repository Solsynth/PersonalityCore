package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/database"
)

var ErrOpenAICredentialUnauthorized = errors.New("invalid AI credential")
var ErrOpenAICredentialLimitExceeded = errors.New("AI credential usage limit exceeded")

// OpenAICredential is the safe public representation of an AI-only credential.
type OpenAICredential struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	TokenPrefix   string     `json:"token_prefix"`
	AgentIDs      []string   `json:"agent_ids,omitempty"`
	Providers     []string   `json:"providers,omitempty"`
	Models        []string   `json:"models,omitempty"`
	UsageLimit    string     `json:"usage_limit"`
	UsageUsed     string     `json:"usage_used"`
	UsageCurrency string     `json:"usage_currency"`
	Enabled       bool       `json:"enabled"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type OpenAICredentialAuth struct {
	CredentialID string
	AccountID    string
}

type CreateOpenAICredentialInput struct {
	Name          string
	AgentIDs      []string
	Providers     []string
	Models        []string
	UsageLimit    string
	UsageCurrency string
}

type CreatedOpenAICredential struct {
	Credential OpenAICredential `json:"credential"`
	Token      string           `json:"token"`
}

func (s *ConversationService) CreateOpenAICredential(ctx context.Context, accountID string, input CreateOpenAICredentialInput) (*CreatedOpenAICredential, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, fmt.Errorf("account_id is required")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len(name) > 128 {
		return nil, fmt.Errorf("name is required and must be at most 128 characters")
	}
	limit, err := decimal(strings.TrimSpace(input.UsageLimit))
	if err != nil {
		return nil, fmt.Errorf("usage_limit: %w", err)
	}
	if limit.Sign() < 0 {
		return nil, fmt.Errorf("usage_limit must be non-negative")
	}
	currency := strings.TrimSpace(input.UsageCurrency)
	if currency == "" {
		currency = s.billing.defaultCurrency()
	}
	if currency == "" {
		return nil, fmt.Errorf("usage_currency is required")
	}
	if err := s.validateCredentialScopes(input.AgentIDs, input.Providers, input.Models); err != nil {
		return nil, err
	}
	token, err := newOpenAICredentialToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	row := &database.OpenAIAccessCredential{
		ID: newID(), AccountID: accountID, Name: name, TokenHash: hashOpenAICredentialToken(token),
		TokenPrefix: token[:minInt(16, len(token))], AgentIDs: jsonList(input.AgentIDs), Providers: jsonList(input.Providers), Models: jsonList(input.Models),
		UsageLimit: limit.FloatString(8), UsageUsed: "0", UsageCurrency: currency, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	return &CreatedOpenAICredential{Credential: publicOpenAICredential(row), Token: token}, nil
}

func (s *ConversationService) ListOpenAICredentials(ctx context.Context, accountID string) ([]OpenAICredential, error) {
	var rows []database.OpenAIAccessCredential
	if err := s.db.WithContext(ctx).Where("account_id = ?", strings.TrimSpace(accountID)).Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]OpenAICredential, 0, len(rows))
	for i := range rows {
		out = append(out, publicOpenAICredential(&rows[i]))
	}
	return out, nil
}

func (s *ConversationService) RevokeOpenAICredential(ctx context.Context, accountID, id string) error {
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&database.OpenAIAccessCredential{}).Where("id = ? AND account_id = ? AND revoked_at IS NULL", strings.TrimSpace(id), strings.TrimSpace(accountID)).Updates(map[string]any{"enabled": false, "revoked_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *ConversationService) AuthenticateOpenAICredential(ctx context.Context, token string) (*OpenAICredentialAuth, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrOpenAICredentialUnauthorized
	}
	var row database.OpenAIAccessCredential
	if err := s.db.WithContext(ctx).Where("token_hash = ? AND enabled = true AND revoked_at IS NULL", hashOpenAICredentialToken(token)).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrOpenAICredentialUnauthorized
		}
		return nil, err
	}
	return &OpenAICredentialAuth{CredentialID: row.ID, AccountID: row.AccountID}, nil
}

func (s *ConversationService) AuthorizeOpenAICredential(ctx context.Context, credentialID, agentID, model string, def agent.Definition) error {
	if strings.TrimSpace(credentialID) == "" {
		return nil
	}
	var row database.OpenAIAccessCredential
	if err := s.db.WithContext(ctx).First(&row, "id = ? AND enabled = true AND revoked_at IS NULL", strings.TrimSpace(credentialID)).Error; err != nil {
		return ErrOpenAICredentialUnauthorized
	}
	used, err := decimal(row.UsageUsed)
	if err != nil {
		return err
	}
	limit, err := decimal(row.UsageLimit)
	if err != nil {
		return err
	}
	if limit.Sign() > 0 && used.Cmp(limit) >= 0 {
		return ErrOpenAICredentialLimitExceeded
	}
	if err := requireScope(row.AgentIDs, agentID, "agent"); err != nil {
		return err
	}
	parts := strings.SplitN(strings.TrimSpace(model), "/", 2)
	provider := ""
	if len(parts) == 2 {
		provider = parts[0]
	}
	if err := requireScope(row.Providers, provider, "provider"); err != nil {
		return err
	}
	if err := requireScope(row.Models, model, "model"); err != nil {
		return err
	}
	return nil
}

func (s *ConversationService) RecordOpenAICredentialUsage(ctx context.Context, credentialID string, def agent.Definition, usage *schema.TokenUsage) error {
	if strings.TrimSpace(credentialID) == "" {
		return nil
	}
	price, err := s.billing.price(def, usage)
	if err != nil {
		return err
	}
	var exceeded bool
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row database.OpenAIAccessCredential
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = ? AND enabled = true AND revoked_at IS NULL", credentialID).Error; err != nil {
			return ErrOpenAICredentialUnauthorized
		}
		if row.UsageCurrency != price.currency {
			return fmt.Errorf("credential currency %q does not match model currency %q", row.UsageCurrency, price.currency)
		}
		used, err := decimal(row.UsageUsed)
		if err != nil {
			return err
		}
		limit, err := decimal(row.UsageLimit)
		if err != nil {
			return err
		}
		total := new(big.Rat).Add(used, mustDecimal(price.amount))
		exceeded = limit.Sign() > 0 && total.Cmp(limit) > 0
		row.UsageUsed = total.FloatString(8)
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		item := &database.OpenAICredentialUsage{ID: newID(), CredentialID: row.ID, AccountID: row.AccountID, Model: def.Model, Currency: price.currency, Amount: price.amount, CreatedAt: time.Now().UTC()}
		if usage != nil {
			item.InputTokens, item.OutputTokens = usage.PromptTokens, usage.CompletionTokens
		}
		return tx.Create(item).Error
	})
	if err != nil {
		return err
	}
	if exceeded {
		return ErrOpenAICredentialLimitExceeded
	}
	return nil
}

func (s *ConversationService) validateCredentialScopes(agents, providers, models []string) error {
	for _, id := range agents {
		if _, ok := s.registry.Get(strings.TrimSpace(id)); !ok {
			return fmt.Errorf("unknown agent %q", id)
		}
	}
	for _, providerID := range providers {
		found := false
		for _, provider := range s.cfg.Providers {
			if strings.EqualFold(strings.TrimSpace(provider.ID), strings.TrimSpace(providerID)) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown provider %q", providerID)
		}
	}
	for _, ref := range models {
		parts := strings.SplitN(strings.TrimSpace(ref), "/", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return fmt.Errorf("model %q must use provider/model format", ref)
		}
		found := false
		for _, provider := range s.cfg.Providers {
			if strings.EqualFold(strings.TrimSpace(provider.ID), strings.TrimSpace(parts[0])) && provider.ResolveModel(parts[1]) != nil {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown model %q", ref)
		}
	}
	return nil
}

func requireScope(raw datatypes.JSON, value, kind string) error {
	var allowed []string
	if len(raw) == 0 || json.Unmarshal(raw, &allowed) != nil || len(allowed) == 0 {
		return nil
	}
	for _, item := range allowed {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(value)) {
			return nil
		}
	}
	return fmt.Errorf("credential does not allow %s %q", kind, value)
}

func publicOpenAICredential(row *database.OpenAIAccessCredential) OpenAICredential {
	return OpenAICredential{ID: row.ID, Name: row.Name, TokenPrefix: row.TokenPrefix, AgentIDs: decodeJSONList(row.AgentIDs), Providers: decodeJSONList(row.Providers), Models: decodeJSONList(row.Models), UsageLimit: row.UsageLimit, UsageUsed: row.UsageUsed, UsageCurrency: row.UsageCurrency, Enabled: row.Enabled, RevokedAt: row.RevokedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
func jsonList(values []string) datatypes.JSON { b, _ := json.Marshal(values); return datatypes.JSON(b) }
func decodeJSONList(raw datatypes.JSON) []string {
	var values []string
	_ = json.Unmarshal(raw, &values)
	return values
}
func newOpenAICredentialToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "sat_" + base64.RawURLEncoding.EncodeToString(raw), nil
}
func hashOpenAICredentialToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func mustDecimal(value string) *big.Rat { parsed, _ := decimal(value); return parsed }
