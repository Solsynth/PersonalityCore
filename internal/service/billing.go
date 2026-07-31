package service

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
)

const PermissionBillingManage = "personality.billing.manage"

var ErrBillingBlacklisted = errors.New("account is blocked from Personality services")
var ErrBillingQuotaExceeded = errors.New("Personality usage threshold exceeded")

// PaymentClient deliberately mirrors only the Wallet RPC needed by billing.
// It makes accounting testable without a live Wallet service.
type PaymentClient interface {
	CreateTransactionWithAccount(context.Context, string, string, string, string, string) (string, error)
}

type BillingService struct {
	db      *database.DB
	cfg     *config.BillingConfig
	rootCfg *config.Config
	payment PaymentClient
}

type BillingRunUsage struct {
	Used int64 `json:"used"`
	Max  *int  `json:"max"`
}

type BillingUsageSummary struct {
	HourlyRuns BillingRunUsage `json:"hourly_runs"`
	DailyRuns  BillingRunUsage `json:"daily_runs"`
}

type BillingSettlementResult struct {
	AccountID string `json:"account_id"`
	Settled   bool   `json:"settled"`
}

func NewBillingService(db *database.DB, cfg *config.Config) *BillingService {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return &BillingService{db: db, cfg: &cfg.Billing, rootCfg: cfg}
}

func (s *BillingService) SetPaymentClient(client PaymentClient) { s.payment = client }

func (s *BillingService) enabled() bool {
	return s != nil && s.db != nil && s.cfg != nil && s.cfg.Enabled
}

// AuthorizeRun enforces the universal blacklist and configured UTC usage
// thresholds. It reserves a ledger row before generation so simultaneous calls
// are counted even when the selected model is free.
func (s *BillingService) AuthorizeRun(ctx context.Context, accountID string, def agent.Definition) (string, error) {
	if s == nil || s.db == nil {
		return "", nil
	}
	policy, err := s.AccountPolicy(ctx, accountID)
	if err != nil {
		return "", err
	}
	if policy.Blacklisted {
		return "", ErrBillingBlacklisted
	}
	if !s.enabled() {
		return "", nil
	}
	now := time.Now().UTC()
	hourly, daily := s.limits(policy)
	var hourlyCount, dailyCount int64
	if hourly > 0 {
		if err := s.db.WithContext(ctx).Model(&database.BillingUsage{}).Where("account_id = ? AND created_at >= ?", accountID, now.Truncate(time.Hour)).Count(&hourlyCount).Error; err != nil {
			return "", err
		}
		if hourlyCount >= int64(hourly) {
			return "", fmt.Errorf("%w: hourly limit is %d", ErrBillingQuotaExceeded, hourly)
		}
	}
	if daily > 0 {
		if err := s.db.WithContext(ctx).Model(&database.BillingUsage{}).Where("account_id = ? AND created_at >= ?", accountID, utcDay(now)).Count(&dailyCount).Error; err != nil {
			return "", err
		}
		if dailyCount >= int64(daily) {
			return "", fmt.Errorf("%w: daily limit is %d", ErrBillingQuotaExceeded, daily)
		}
	}
	usage := &database.BillingUsage{ID: newID(), AccountID: accountID, Model: def.Model, Amount: "0", CreatedAt: now}
	if err := s.db.WithContext(ctx).Create(usage).Error; err != nil {
		return "", err
	}
	return usage.ID, nil
}

func (s *BillingService) CancelAuthorization(ctx context.Context, usageID string) {
	if s != nil && s.db != nil && usageID != "" {
		_ = s.db.WithContext(ctx).Delete(&database.BillingUsage{}, "id = ?", usageID).Error
	}
}

func (s *BillingService) RecordUsage(ctx context.Context, usageID, runID string, def agent.Definition, usage *schema.TokenUsage) error {
	if !s.enabled() || usageID == "" {
		return nil
	}
	price, err := s.price(def, usage)
	if err != nil {
		return err
	}
	updates := map[string]any{"run_id": runID, "model": def.Model, "amount": price.amount, "currency": price.currency}
	if usage != nil {
		updates["input_tokens"] = usage.PromptTokens
		updates["output_tokens"] = usage.CompletionTokens
	}
	if err := s.db.WithContext(ctx).Model(&database.BillingUsage{}).Where("id = ?", usageID).Updates(updates).Error; err != nil {
		return err
	}
	var record database.BillingUsage
	if err := s.db.WithContext(ctx).First(&record, "id = ?", usageID).Error; err != nil {
		return err
	}
	policy, err := s.AccountPolicy(ctx, record.AccountID)
	if err != nil {
		return err
	}
	// The legacy/global wall applies to the configured default currency only.
	// Other currencies settle at UTC midnight unless a future policy supplies a
	// currency-specific wall.
	if record.Currency != s.defaultCurrency() {
		return nil
	}
	wall := normalizedDecimal(s.cfg.InstantBillingWall)
	if policy.InstantBillingWall != nil {
		wall = normalizedDecimal(*policy.InstantBillingWall)
	}
	if wall == "0" {
		return nil
	}
	total, err := s.unpaidAmount(ctx, record.AccountID, record.Currency, time.Time{})
	if err != nil {
		return err
	}
	if decimalCmp(total, wall) < 0 {
		return nil
	}
	if err := s.chargeAccount(ctx, record.AccountID, record.Currency, "instant billing wall", time.Time{}, time.Time{}); err != nil {
		return err
	}
	return nil
}

func (s *BillingService) limits(policy *database.BillingAccountPolicy) (int, int) {
	hourly, daily := s.cfg.DefaultHourlyRuns, s.cfg.DefaultDailyRuns
	if policy.HourlyRunLimit != nil {
		hourly = *policy.HourlyRunLimit
	}
	if policy.DailyRunLimit != nil {
		daily = *policy.DailyRunLimit
	}
	return hourly, daily
}

func (s *BillingService) AccountPolicy(ctx context.Context, accountID string) (*database.BillingAccountPolicy, error) {
	policy := &database.BillingAccountPolicy{AccountID: strings.TrimSpace(accountID)}
	err := s.db.WithContext(ctx).First(policy, "account_id = ?", policy.AccountID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return policy, nil
	}
	return policy, err
}

// UsageSummary returns UTC-based current use and the resolved account limits.
// A nil Max means no limit is configured for that interval.
func (s *BillingService) UsageSummary(ctx context.Context, accountID string) (*BillingUsageSummary, error) {
	policy, err := s.AccountPolicy(ctx, accountID)
	if err != nil {
		return nil, err
	}
	hourlyLimit, dailyLimit := s.limits(policy)
	now := time.Now().UTC()
	var hourlyUsed, dailyUsed int64
	if err := s.db.WithContext(ctx).Model(&database.BillingUsage{}).Where("account_id = ? AND created_at >= ?", accountID, now.Truncate(time.Hour)).Count(&hourlyUsed).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Model(&database.BillingUsage{}).Where("account_id = ? AND created_at >= ?", accountID, utcDay(now)).Count(&dailyUsed).Error; err != nil {
		return nil, err
	}
	return &BillingUsageSummary{HourlyRuns: BillingRunUsage{Used: hourlyUsed, Max: optionalLimit(hourlyLimit)}, DailyRuns: BillingRunUsage{Used: dailyUsed, Max: optionalLimit(dailyLimit)}}, nil
}

func optionalLimit(limit int) *int {
	if limit <= 0 {
		return nil
	}
	return &limit
}

func (s *BillingService) CheckAccess(ctx context.Context, accountID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	policy, err := s.AccountPolicy(ctx, accountID)
	if err != nil {
		return err
	}
	if policy.Blacklisted {
		return ErrBillingBlacklisted
	}
	return nil
}

func (s *BillingService) UpsertAccountPolicy(ctx context.Context, policy *database.BillingAccountPolicy) (*database.BillingAccountPolicy, error) {
	if policy == nil || strings.TrimSpace(policy.AccountID) == "" {
		return nil, fmt.Errorf("account_id is required")
	}
	policy.AccountID = strings.TrimSpace(policy.AccountID)
	if err := validatePolicy(policy); err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Save(policy).Error; err != nil {
		return nil, err
	}
	return policy, nil
}

func (s *BillingService) Blacklist(ctx context.Context, accountID, reason string) error {
	p, err := s.AccountPolicy(ctx, accountID)
	if err != nil {
		return err
	}
	p.Blacklisted, p.BlacklistReason = true, strings.TrimSpace(reason)
	_, err = s.UpsertAccountPolicy(ctx, p)
	return err
}

func (s *BillingService) clearBlacklist(ctx context.Context, accountID string) error {
	p, err := s.AccountPolicy(ctx, accountID)
	if err != nil {
		return err
	}
	if !p.Blacklisted && p.BlacklistReason == "" {
		return nil
	}
	p.Blacklisted, p.BlacklistReason = false, ""
	_, err = s.UpsertAccountPolicy(ctx, p)
	return err
}

// SettleAccount immediately charges every outstanding balance for an account.
// It intentionally remains callable by a blacklisted account so a failed daily
// charge can be retried. A successful full settlement restores account access.
func (s *BillingService) SettleAccount(ctx context.Context, accountID string) (*BillingSettlementResult, error) {
	if !s.enabled() {
		return nil, fmt.Errorf("billing is not enabled")
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, fmt.Errorf("account_id is required")
	}
	type settlementKey struct{ Currency string }
	var currencies []settlementKey
	if err := s.db.WithContext(ctx).Model(&database.BillingUsage{}).
		Select("currency").Distinct().
		Where("account_id = ? AND payment_id IS NULL", accountID).
		Scan(&currencies).Error; err != nil {
		return nil, err
	}
	for _, item := range currencies {
		if err := s.chargeAccount(ctx, accountID, item.Currency, "manual Personality billing settlement", time.Time{}, time.Time{}); err != nil {
			return nil, err
		}
	}
	if err := s.clearBlacklist(ctx, accountID); err != nil {
		return nil, err
	}
	return &BillingSettlementResult{AccountID: accountID, Settled: true}, nil
}

type pricedUsage struct{ amount, currency string }

func (s *BillingService) price(def agent.Definition, usage *schema.TokenUsage) (pricedUsage, error) {
	if usage == nil {
		return pricedUsage{amount: "0", currency: s.defaultCurrency()}, nil
	}
	parts := strings.SplitN(def.Model, "/", 2)
	if len(parts) != 2 {
		return pricedUsage{amount: "0", currency: s.defaultCurrency()}, nil
	}
	for _, p := range s.rootCfg.Providers {
		if p.ID == parts[0] {
			if m := p.ResolveModel(parts[1]); m != nil {
				if m.Pricing == nil || (m.Pricing.Input == nil && m.Pricing.Output == nil) {
					return pricedUsage{amount: "0", currency: s.defaultCurrency()}, nil
				}
				in, out := new(big.Rat), new(big.Rat)
				if m.Pricing.Input != nil {
					var err error
					in, err = decimal(*m.Pricing.Input)
					if err != nil {
						return pricedUsage{}, err
					}
				}
				if m.Pricing.Output != nil {
					var err error
					out, err = decimal(*m.Pricing.Output)
					if err != nil {
						return pricedUsage{}, err
					}
				}
				total := new(big.Rat).Add(new(big.Rat).Mul(in, big.NewRat(int64(usage.PromptTokens), 1_000_000)), new(big.Rat).Mul(out, big.NewRat(int64(usage.CompletionTokens), 1_000_000)))
				if def.BillingMultiplier != nil {
					mult := new(big.Rat).SetFloat64(*def.BillingMultiplier)
					if mult == nil || *def.BillingMultiplier < 0 {
						return pricedUsage{}, fmt.Errorf("invalid billing multiplier")
					}
					total.Mul(total, mult)
				}
				feePercent, err := decimal(s.cfg.ServiceFeePercentage)
				if err != nil {
					return pricedUsage{}, fmt.Errorf("invalid billing service fee percentage: %w", err)
				}
				if feePercent.Sign() > 0 {
					// A percentage of 5 means the user is charged 105% of the
					// model/agent price; 0 leaves the price unchanged.
					factor := new(big.Rat).Add(big.NewRat(1, 1), new(big.Rat).Quo(feePercent, big.NewRat(100, 1)))
					total.Mul(total, factor)
				}
				currency := strings.TrimSpace(m.Pricing.Currency)
				if currency == "" {
					currency = s.defaultCurrency()
				}
				return pricedUsage{amount: total.FloatString(8), currency: currency}, nil
			}
		}
	}
	return pricedUsage{amount: "0", currency: s.defaultCurrency()}, nil
}

func validatePolicy(p *database.BillingAccountPolicy) error {
	if p.HourlyRunLimit != nil && *p.HourlyRunLimit < 0 {
		return fmt.Errorf("hourly_run_limit must be non-negative")
	}
	if p.DailyRunLimit != nil && *p.DailyRunLimit < 0 {
		return fmt.Errorf("daily_run_limit must be non-negative")
	}
	if p.InstantBillingWall != nil {
		_, err := decimal(*p.InstantBillingWall)
		if err != nil {
			return fmt.Errorf("invalid instant_billing_wall: %w", err)
		}
	}
	return nil
}
func decimal(v string) (*big.Rat, error) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(v))
	if !ok || r.Sign() < 0 {
		return nil, fmt.Errorf("must be a non-negative decimal")
	}
	return r, nil
}
func normalizedDecimal(v string) string {
	if r, err := decimal(v); err == nil {
		return r.FloatString(8)
	}
	return "0"
}
func decimalCmp(a, b string) int {
	ar, ea := decimal(a)
	br, eb := decimal(b)
	if ea != nil || eb != nil {
		return -1
	}
	return ar.Cmp(br)
}
func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func (s *BillingService) defaultCurrency() string {
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.Currency) == "" {
		return "golds"
	}
	return strings.TrimSpace(s.cfg.Currency)
}

func (s *BillingService) unpaidAmount(ctx context.Context, accountID, currency string, before time.Time) (string, error) {
	q := s.db.WithContext(ctx).Model(&database.BillingUsage{}).Where("payment_id IS NULL")
	if accountID != "" {
		q = q.Where("account_id = ?", accountID)
	}
	if currency != "" {
		q = q.Where("currency = ?", currency)
	}
	if !before.IsZero() {
		q = q.Where("created_at < ?", before)
	}
	var usages []database.BillingUsage
	if err := q.Order("created_at ASC, id ASC").Find(&usages).Error; err != nil {
		return "", err
	}
	total := new(big.Rat)
	for _, usage := range usages {
		amount, err := decimal(usage.Amount)
		if err != nil {
			return "", err
		}
		total.Add(total, amount)
	}
	return total.FloatString(8), nil
}

// chargeAccount settles all eligible unpaid rows atomically after Wallet has
// accepted the transfer. A failed transfer blacklists that one account.
func (s *BillingService) chargeAccount(ctx context.Context, accountID, currency, remarks string, from, before time.Time) error {
	if s.payment == nil {
		return fmt.Errorf("billing payment client is unavailable")
	}
	q := s.db.WithContext(ctx).Where("account_id = ? AND currency = ? AND payment_id IS NULL", accountID, currency)
	if !from.IsZero() {
		q = q.Where("created_at >= ?", from)
	}
	if !before.IsZero() {
		q = q.Where("created_at < ?", before)
	}
	var usages []database.BillingUsage
	if err := q.Find(&usages).Error; err != nil {
		return err
	}
	total := new(big.Rat)
	for _, usage := range usages {
		amount, err := decimal(usage.Amount)
		if err != nil {
			return err
		}
		total.Add(total, amount)
	}
	if total.Sign() == 0 {
		return nil
	}
	billable := floorCurrencyAmount(total)
	if billable.Sign() == 0 {
		// Wallet only supports two decimal places. Leave the outstanding amount
		// in the ledger so it is combined with future usage instead of lost.
		return nil
	}
	amount := billable.FloatString(2)
	txID, err := s.payment.CreateTransactionWithAccount(ctx, accountID, s.cfg.PayeeAccountID, currency, amount, remarks)
	if err != nil {
		_ = s.Blacklist(ctx, accountID, "wallet payment failed: "+err.Error())
		return fmt.Errorf("charge account: %w", err)
	}
	payment := &database.BillingPayment{ID: newID(), AccountID: accountID, Amount: amount, Currency: currency, WalletTxID: txID, PeriodStart: from, PeriodEnd: before}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(payment).Error; err != nil {
			return err
		}
		return applyPaymentAllocation(tx, usages, billable, payment.ID)
	}); err != nil {
		return err
	}
	return nil
}

// floorCurrencyAmount truncates, never rounds, to Wallet's two decimal places.
func floorCurrencyAmount(amount *big.Rat) *big.Rat {
	if amount == nil || amount.Sign() <= 0 {
		return new(big.Rat)
	}
	scaled := new(big.Int).Mul(amount.Num(), big.NewInt(100))
	scaled.Quo(scaled, amount.Denom())
	return new(big.Rat).SetFrac(scaled, big.NewInt(100))
}

// applyPaymentAllocation consumes the oldest outstanding usage first. A
// partially consumed row keeps its fractional remainder unpaid, allowing it to
// roll into the next settlement cycle.
func applyPaymentAllocation(tx *gorm.DB, usages []database.BillingUsage, paid *big.Rat, paymentID string) error {
	remaining := new(big.Rat).Set(paid)
	for _, usage := range usages {
		if remaining.Sign() == 0 {
			break
		}
		outstanding, err := decimal(usage.Amount)
		if err != nil {
			return err
		}
		cmp := outstanding.Cmp(remaining)
		if cmp <= 0 {
			if err := tx.Model(&database.BillingUsage{}).Where("id = ?", usage.ID).Update("payment_id", paymentID).Error; err != nil {
				return err
			}
			remaining.Sub(remaining, outstanding)
			continue
		}
		// Keep only the unbillable remainder on this usage row. It is included
		// in the next charge and therefore never disappears due to rounding.
		outstanding.Sub(outstanding, remaining)
		if err := tx.Model(&database.BillingUsage{}).Where("id = ?", usage.ID).Update("amount", outstanding.FloatString(8)).Error; err != nil {
			return err
		}
		remaining.SetInt64(0)
	}
	return nil
}

func usageIDs(items []database.BillingUsage) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

// SettleCompletedDays is safe to call at startup and at UTC midnight; paid rows
// are linked to a BillingPayment so re-runs only process genuinely unpaid usage.
func (s *BillingService) SettleCompletedDays(ctx context.Context) error {
	if !s.enabled() {
		return nil
	}
	cutoff := utcDay(time.Now())
	type settlementKey struct{ AccountID, Currency string }
	var accounts []settlementKey
	if err := s.db.WithContext(ctx).Model(&database.BillingUsage{}).Select("account_id, currency").Distinct().Where("payment_id IS NULL AND created_at < ?", cutoff).Scan(&accounts).Error; err != nil {
		return err
	}
	for _, account := range accounts {
		policy, err := s.AccountPolicy(ctx, account.AccountID)
		if err != nil {
			return err
		}
		if policy.Blacklisted {
			// A blocked account must explicitly retry via SettleAccount. This
			// avoids a cron retry loop against a known-failing payer.
			continue
		}
		if err := s.chargeAccount(ctx, account.AccountID, account.Currency, "daily Personality usage", time.Time{}, cutoff); err != nil {
			return err
		}
	}
	return nil
}
