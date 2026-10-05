package service

import (
	"context"
	"math/big"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

// BillingLedgerQuery filters the audit ledger. Every field is optional: an
// empty AccountID spans all accounts and is intended for administrators, while
// the self-service surface always pins it to the caller.
type BillingLedgerQuery struct {
	AccountID    string
	Action       string
	Model        string
	Surface      string
	Currency     string
	CredentialID string
	ClientIP     string
	DeviceID     string
	RunID        string
	// From is inclusive, To exclusive, both UTC.
	From *time.Time
	To   *time.Time
	// UnpaidOnly keeps the rows that no payment has settled yet.
	UnpaidOnly bool
	Take       int
	Offset     int
}

// BillingLedgerEntry is one audit row: what was consumed, by which call, and
// how much. ThreadID and AgentID come from the run the row is linked to and
// stay empty for non-generation charges, which have no run.
type BillingLedgerEntry struct {
	ID             string    `json:"id"`
	Action         string    `json:"action"`
	Surface        string    `json:"surface,omitempty"`
	Model          string    `json:"model"`
	RunID          *string   `json:"run_id,omitempty"`
	ThreadID       string    `json:"thread_id,omitempty"`
	AgentID        string    `json:"agent_id,omitempty"`
	AccountID      string    `json:"account_id"`
	Currency       string    `json:"currency"`
	InputTokens    int       `json:"input_tokens"`
	OutputTokens   int       `json:"output_tokens"`
	Amount         string    `json:"amount"`
	OriginalAmount string    `json:"original_amount"`
	PaymentID      *string   `json:"payment_id,omitempty"`
	CredentialID   *string   `json:"credential_id,omitempty"`
	ClientIP       string    `json:"client_ip,omitempty"`
	DeviceID       string    `json:"device_id,omitempty"`
	UserAgent      string    `json:"user_agent,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// BillingLedgerBucket is one aggregate: how much a single key (an action, a
// model, an address, …) consumed. Currency is kept beside the amount because
// summing different currencies is meaningless; every bucket is single-currency
// and a key that spans currencies yields one bucket per currency.
type BillingLedgerBucket struct {
	Key          string `json:"key"`
	Currency     string `json:"currency"`
	Entries      int64  `json:"entries"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	Amount       string `json:"amount"`
}

// BillingLedgerSummary answers "who spent what" over the same filter as the
// ledger list. Amounts are always the incurred price (original_amount), never
// the remaining unpaid balance, so a partially settled row still reports what
// it cost.
type BillingLedgerSummary struct {
	Entries      int64                 `json:"entries"`
	ByAction     []BillingLedgerBucket `json:"by_action"`
	BySurface    []BillingLedgerBucket `json:"by_surface"`
	ByModel      []BillingLedgerBucket `json:"by_model"`
	ByCurrency   []BillingLedgerBucket `json:"by_currency"`
	ByClientIP   []BillingLedgerBucket `json:"by_client_ip"`
	ByDeviceID   []BillingLedgerBucket `json:"by_device_id"`
	ByCredential []BillingLedgerBucket `json:"by_credential"`
	ByDay        []BillingLedgerBucket `json:"by_day"`
}

const (
	ledgerDefaultTake = 20
	ledgerMaxTake     = 200
)

// apply pushes a query's filters onto a ledger statement. Every statement over
// the ledger aliases the table as `bu`, including the join targets, so the
// filters can be shared between the list, the count and the summary.
func (q BillingLedgerQuery) apply(db *gorm.DB) *gorm.DB {
	if v := strings.TrimSpace(q.AccountID); v != "" {
		db = db.Where("bu.account_id = ?", v)
	}
	if v := strings.TrimSpace(q.Action); v != "" {
		db = db.Where("bu.action = ?", v)
	}
	if v := strings.TrimSpace(q.Model); v != "" {
		db = db.Where("bu.model = ?", v)
	}
	if v := strings.TrimSpace(q.Surface); v != "" {
		db = db.Where("bu.surface = ?", v)
	}
	if v := strings.TrimSpace(q.Currency); v != "" {
		db = db.Where("bu.currency = ?", v)
	}
	if v := strings.TrimSpace(q.CredentialID); v != "" {
		db = db.Where("bu.credential_id = ?", v)
	}
	if v := strings.TrimSpace(q.ClientIP); v != "" {
		db = db.Where("bu.client_ip = ?", v)
	}
	if v := strings.TrimSpace(q.DeviceID); v != "" {
		db = db.Where("bu.device_id = ?", v)
	}
	if v := strings.TrimSpace(q.RunID); v != "" {
		db = db.Where("bu.run_id = ?", v)
	}
	if q.UnpaidOnly {
		db = db.Where("bu.payment_id IS NULL")
	}
	if q.From != nil {
		db = db.Where("bu.created_at >= ?", q.From.UTC())
	}
	if q.To != nil {
		db = db.Where("bu.created_at < ?", q.To.UTC())
	}
	return db
}

func (s *BillingService) ledgerBase(ctx context.Context) *gorm.DB {
	return s.db.WithContext(ctx).Table("billing_usages AS bu")
}

// Ledger returns the audit rows matching q, newest first, with the total
// matching count so a caller can page through them.
func (s *BillingService) Ledger(ctx context.Context, q BillingLedgerQuery) ([]BillingLedgerEntry, int64, error) {
	if s == nil || s.db == nil {
		return []BillingLedgerEntry{}, 0, nil
	}
	var total int64
	if err := q.apply(s.ledgerBase(ctx)).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	take, offset := ledgerWindow(q)
	entries := make([]BillingLedgerEntry, 0, take)
	err := q.apply(s.ledgerBase(ctx)).
		Select(`bu.id, bu.action, bu.surface, bu.model, bu.run_id, bu.account_id, bu.currency,
			bu.input_tokens, bu.output_tokens, bu.amount, bu.original_amount, bu.payment_id,
			bu.credential_id, bu.client_ip, bu.device_id, bu.user_agent, bu.created_at,
			COALESCE(r.thread_id, '') AS thread_id, COALESCE(r.agent_id, '') AS agent_id`).
		Joins("LEFT JOIN conversation_runs AS r ON r.id = bu.run_id").
		Order("bu.created_at DESC, bu.id DESC").
		Offset(offset).
		Limit(take).
		Scan(&entries).Error
	if err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// LedgerSummary aggregates the matching rows along every audit dimension at
// once, so one request answers "which action, which endpoint, which model,
// which credential, which address, which device, which day". Sums are computed
// in exact decimal arithmetic rather than by the database, because amounts are
// stored as decimal strings that SQL would have to cast through a lossy float.
func (s *BillingService) LedgerSummary(ctx context.Context, q BillingLedgerQuery) (*BillingLedgerSummary, error) {
	summary := &BillingLedgerSummary{
		ByAction:     []BillingLedgerBucket{},
		BySurface:    []BillingLedgerBucket{},
		ByModel:      []BillingLedgerBucket{},
		ByCurrency:   []BillingLedgerBucket{},
		ByClientIP:   []BillingLedgerBucket{},
		ByDeviceID:   []BillingLedgerBucket{},
		ByCredential: []BillingLedgerBucket{},
		ByDay:        []BillingLedgerBucket{},
	}
	if s == nil || s.db == nil {
		return summary, nil
	}
	var rows []ledgerAggregateRow
	err := q.apply(s.ledgerBase(ctx)).
		Select("bu.action, bu.surface, bu.model, bu.currency, bu.credential_id, bu.client_ip, bu.device_id, bu.input_tokens, bu.output_tokens, bu.original_amount, bu.created_at").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	byAction, bySurface, byModel, byCurrency := newLedgerBuckets(), newLedgerBuckets(), newLedgerBuckets(), newLedgerBuckets()
	byClientIP, byDeviceID, byCredential, byDay := newLedgerBuckets(), newLedgerBuckets(), newLedgerBuckets(), newLedgerBuckets()
	for i := range rows {
		row := &rows[i]
		credential := ""
		if row.CredentialID != nil {
			credential = *row.CredentialID
		}
		byAction.add(row.Action, row)
		bySurface.add(row.Surface, row)
		byModel.add(row.Model, row)
		byCurrency.add(row.Currency, row)
		byClientIP.add(row.ClientIP, row)
		byDeviceID.add(row.DeviceID, row)
		byCredential.add(credential, row)
		byDay.add(row.CreatedAt.UTC().Format(time.DateOnly), row)
		summary.Entries++
	}
	summary.ByAction = byAction.list()
	summary.BySurface = bySurface.list()
	summary.ByModel = byModel.list()
	summary.ByCurrency = byCurrency.list()
	summary.ByClientIP = byClientIP.list()
	summary.ByDeviceID = byDeviceID.list()
	summary.ByCredential = byCredential.list()
	summary.ByDay = byDay.list()
	return summary, nil
}

func ledgerWindow(q BillingLedgerQuery) (take, offset int) {
	take = q.Take
	if take <= 0 {
		take = ledgerDefaultTake
	}
	if take > ledgerMaxTake {
		take = ledgerMaxTake
	}
	offset = q.Offset
	if offset < 0 {
		offset = 0
	}
	return take, offset
}

// ledgerAggregateRow is the narrow projection the summary aggregates in Go.
type ledgerAggregateRow struct {
	Action         string
	Surface        string
	Model          string
	Currency       string
	CredentialID   *string
	ClientIP       string
	DeviceID       string
	InputTokens    int
	OutputTokens   int
	OriginalAmount string
	CreatedAt      time.Time
}

type ledgerBucketKey struct {
	key      string
	currency string
}

type ledgerAccumulator struct {
	entries      int64
	inputTokens  int64
	outputTokens int64
	amount       *big.Rat
}

type ledgerBuckets struct {
	acc map[ledgerBucketKey]*ledgerAccumulator
}

func newLedgerBuckets() *ledgerBuckets {
	return &ledgerBuckets{acc: map[ledgerBucketKey]*ledgerAccumulator{}}
}

func (b *ledgerBuckets) add(key string, row *ledgerAggregateRow) {
	key = strings.TrimSpace(key)
	k := ledgerBucketKey{key: key, currency: row.Currency}
	acc := b.acc[k]
	if acc == nil {
		acc = &ledgerAccumulator{amount: new(big.Rat)}
		b.acc[k] = acc
	}
	acc.entries++
	acc.inputTokens += int64(row.InputTokens)
	acc.outputTokens += int64(row.OutputTokens)
	amount := row.OriginalAmount
	if strings.TrimSpace(amount) == "" {
		amount = "0"
	}
	if value, err := decimal(amount); err == nil {
		acc.amount.Add(acc.amount, value)
	}
}

// list sorts buckets by amount descending, then by key, so the biggest
// consumers surface first.
func (b *ledgerBuckets) list() []BillingLedgerBucket {
	out := make([]BillingLedgerBucket, 0, len(b.acc))
	for k, acc := range b.acc {
		out = append(out, BillingLedgerBucket{
			Key:          k.key,
			Currency:     k.currency,
			Entries:      acc.entries,
			InputTokens:  acc.inputTokens,
			OutputTokens: acc.outputTokens,
			Amount:       acc.amount.FloatString(8),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		cmp := decimalCmp(out[i].Amount, out[j].Amount)
		if cmp != 0 {
			return cmp > 0
		}
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Currency < out[j].Currency
	})
	return out
}
