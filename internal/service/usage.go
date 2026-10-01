package service

import (
	"context"
	"encoding/json"
	"math"
	"strings"

	"github.com/cloudwego/eino/schema"
	"gorm.io/datatypes"

	"src.solsynth.dev/sosys/persona/internal/database"
)

// runUsage accumulates provider-reported token usage across every model call a
// single run makes. A tool-calling run calls the model once per round, and each
// call reports only its own tokens, so totals must be summed rather than
// overwritten. PeakInputTokens is the largest single prompt, which is what the
// context window was actually filled with.
type runUsage struct {
	InputTokens     int
	OutputTokens    int
	TotalTokens     int
	PeakInputTokens int
	Rounds          int
}

func (u *runUsage) add(usage *schema.TokenUsage) {
	if u == nil || usage == nil {
		return
	}
	total := usage.TotalTokens
	if total == 0 {
		total = usage.PromptTokens + usage.CompletionTokens
	}
	u.InputTokens += usage.PromptTokens
	u.OutputTokens += usage.CompletionTokens
	u.TotalTokens += total
	if usage.PromptTokens > u.PeakInputTokens {
		u.PeakInputTokens = usage.PromptTokens
	}
	u.Rounds++
}

// addMeta takes a whole response metadata block, tolerating the nil meta a
// provider may return on an otherwise successful call.
func (u *runUsage) addMeta(meta *schema.ResponseMeta) {
	if meta != nil {
		u.add(meta.Usage)
	}
}

// tokenUsage collapses the accumulator into what the billing ledger takes. A
// run that reported nothing yields nil so billing keeps its zero-amount path.
func (u *runUsage) tokenUsage() *schema.TokenUsage {
	if u == nil || u.Rounds == 0 {
		return nil
	}
	return &schema.TokenUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
	}
}

// runContextUsage is how full the model's context was. used_tokens is the peak
// prompt across the run's rounds; the window and ratio are omitted when the
// model's ceiling is unknown, because a ratio against a guessed window would be
// worse than no ratio.
type runContextUsage struct {
	UsedTokens   int      `json:"used_tokens"`
	WindowTokens int      `json:"window_tokens,omitempty"`
	UsedRatio    *float64 `json:"used_ratio,omitempty"`
}

// runUsagePayload is the shape stored in conversation_runs.usage and returned
// by every run endpoint.
type runUsagePayload struct {
	InputTokens  int              `json:"input_tokens"`
	OutputTokens int              `json:"output_tokens"`
	TotalTokens  int              `json:"total_tokens"`
	Rounds       int              `json:"rounds"`
	Context      *runContextUsage `json:"context,omitempty"`
}

// ConversationUsage is the token total across every run in one conversation.
type ConversationUsage struct {
	Runs                  int `json:"runs"`
	InputTokens           int `json:"input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	TotalTokens           int `json:"total_tokens"`
	PeakContextUsedTokens int `json:"peak_context_used_tokens,omitempty"`
	ContextWindowTokens   int `json:"context_window_tokens,omitempty"`
}

// stampRunUsage writes the run's accumulated usage onto the row before it is
// saved. It resolves the context window here so the stored payload is complete
// at read time rather than recomputed per request.
func (s *ConversationService) stampRunUsage(ctx context.Context, run *database.ConversationRun, usage *runUsage, modelRef string) {
	if run == nil {
		return
	}
	if usage == nil || usage.Rounds == 0 {
		run.Usage = datatypes.JSON([]byte("{}"))
		return
	}

	payload := runUsagePayload{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		TotalTokens:  usage.TotalTokens,
		Rounds:       usage.Rounds,
	}
	context := &runContextUsage{UsedTokens: usage.PeakInputTokens}
	if window := s.resolveContextWindow(ctx, modelRef); window > 0 {
		ratio := math.Round(float64(usage.PeakInputTokens)/float64(window)*1_000_000) / 1_000_000
		context.WindowTokens = window
		context.UsedRatio = &ratio
	}
	payload.Context = context

	encoded, err := json.Marshal(payload)
	if err != nil {
		// A marshal failure must never sink a completed run; the usage column
		// simply stays empty and the response still carries the answer.
		run.Usage = datatypes.JSON([]byte("{}"))
		return
	}
	run.Usage = datatypes.JSON(encoded)
}

func (s *ConversationService) resolveContextWindow(ctx context.Context, modelRef string) int {
	if s == nil || s.executor == nil || strings.TrimSpace(modelRef) == "" {
		return 0
	}
	return s.executor.ContextWindow(ctx, modelRef)
}

// ConversationUsage totals the token usage of every run in one conversation.
// Runs are read scoped to the account exactly like the run endpoints, and the
// aggregate is computed from the stored payloads so it never re-prices a run
// against today's configuration.
func (s *ConversationService) ConversationUsage(ctx context.Context, accountID, threadID string) (*ConversationUsage, error) {
	if _, err := s.GetConversation(ctx, accountID, threadID); err != nil {
		return nil, err
	}

	var runs []database.ConversationRun
	if err := s.db.WithContext(ctx).
		Select("usage").
		Where("thread_id = ? AND account_id = ?", threadID, accountID).
		Find(&runs).Error; err != nil {
		return nil, err
	}

	total := &ConversationUsage{}
	for i := range runs {
		payload, ok := decodeRunUsage(runs[i].Usage)
		if !ok {
			continue
		}
		total.Runs++
		total.InputTokens += payload.InputTokens
		total.OutputTokens += payload.OutputTokens
		total.TotalTokens += payload.TotalTokens
		if payload.Context != nil {
			if payload.Context.UsedTokens > total.PeakContextUsedTokens {
				total.PeakContextUsedTokens = payload.Context.UsedTokens
			}
			if payload.Context.WindowTokens > total.ContextWindowTokens {
				total.ContextWindowTokens = payload.Context.WindowTokens
			}
		}
	}
	return total, nil
}

// decodeRunUsage reads a stored usage payload. An empty or malformed document
// (including the "{}" a run is created with) is reported as not-ok so it adds
// nothing to an aggregate.
func decodeRunUsage(raw datatypes.JSON) (runUsagePayload, bool) {
	var payload runUsagePayload
	if len(raw) == 0 {
		return payload, false
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, false
	}
	if payload == (runUsagePayload{}) {
		return payload, false
	}
	return payload, true
}
