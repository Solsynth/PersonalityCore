package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"src.solsynth.dev/sosys/persona/internal/config"
	"src.solsynth.dev/sosys/persona/internal/logging"
)

// ContextWindow reports the model's total input-token ceiling: how many tokens
// of prompt the model can hold. It exists only to report how full a run's
// context was, so it is deliberately advisory — an unknown window (0) omits the
// ratio rather than guessing one.
//
// Resolution order, first non-zero wins:
//  1. the model's own `contextWindow`
//  2. the provider's `contextWindow` default
//  3. the built-in preset for well-known cloud models
//  4. the provider's own /models reply, when it advertises a window
//
// Only the last step touches the network, and only once per provider/model.
func (e *Executor) ContextWindow(ctx context.Context, modelRef string) int {
	if e == nil {
		return 0
	}
	provider, modelName, mc, err := e.resolveModelForPurpose(modelRef, false)
	if err != nil {
		return 0
	}
	if mc != nil && mc.ContextWindow > 0 {
		return mc.ContextWindow
	}
	if provider.ContextWindow > 0 {
		return provider.ContextWindow
	}
	if window := presetContextWindow(modelName); window > 0 {
		return window
	}
	return e.discoverContextWindow(ctx, provider, modelName)
}

// contextWindowPresets are the windows that are cheap to know and stable
// enough to hard-code. Matching is anchored (prefix) so a self-hosted model
// whose id happens to contain a vendor name is not misread.
var contextWindowPresets = []struct {
	prefix string
	window int
}{
	{"gpt-4.1", 1_047_576},
	{"gpt-4o", 128_000},
	{"gpt-4-turbo", 128_000},
	{"o1", 200_000},
	{"o3", 200_000},
	{"o4", 200_000},
	{"claude", 200_000},
	{"gemini", 1_048_576},
	{"deepseek-chat", 128_000},
	{"deepseek-reasoner", 128_000},
}

func presetContextWindow(modelName string) int {
	name := strings.ToLower(strings.TrimSpace(modelName))
	// Longest prefix first so "gpt-4-turbo" is not shadowed by "gpt-4.1".
	presets := append([]struct {
		prefix string
		window int
	}(nil), contextWindowPresets...)
	sort.SliceStable(presets, func(i, j int) bool { return len(presets[i].prefix) > len(presets[j].prefix) })
	for _, preset := range presets {
		if strings.HasPrefix(name, preset.prefix) {
			return preset.window
		}
	}
	return 0
}

// discoverContextWindow asks the provider's own /models endpoint for a window.
// OpenAI-compatible servers answer the list; only some (vLLM, LM Studio, and
// similar) include a context length, so a miss is normal and cached.
func (e *Executor) discoverContextWindow(ctx context.Context, provider config.ProviderConfig, modelName string) int {
	if e == nil || !provider.DiscoverContextWindow {
		return 0
	}
	if strings.TrimSpace(provider.BaseURL) == "" || strings.TrimSpace(provider.APIKey) == "" {
		return 0
	}
	cacheKey := provider.ID + "\x00" + strings.ToLower(modelName)
	if cached, ok := e.contextWindows.Load(cacheKey); ok {
		window, _ := cached.(int)
		return window
	}

	window := e.fetchContextWindow(ctx, provider, modelName)
	e.contextWindows.Store(cacheKey, window)
	if window == 0 {
		logging.Log.Debug().
			Str("provider_id", provider.ID).
			Str("model", modelName).
			Msg("provider did not advertise a context window; context usage ratio stays unset")
	}
	return window
}

func (e *Executor) fetchContextWindow(ctx context.Context, provider config.ProviderConfig, modelName string) int {
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	url := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/") + "/models"
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := e.discoveryHTTPClient(provider).Do(req)
	if err != nil {
		logging.Log.Debug().Err(err).Str("provider_id", provider.ID).Str("model", modelName).Msg("context window discovery failed")
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}

	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0
	}
	for _, entry := range payload.Data {
		id, _ := entry["id"].(string)
		if !strings.EqualFold(strings.TrimSpace(id), strings.TrimSpace(modelName)) {
			continue
		}
		return contextWindowFromEntry(entry)
	}
	return 0
}

func (e *Executor) discoveryHTTPClient(provider config.ProviderConfig) *http.Client {
	timeout := provider.Timeout
	if timeout <= 0 || timeout > 15*time.Second {
		timeout = 10 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

// contextWindowFromEntry reads whichever spelling a server used for a model's
// input ceiling. Every field is optional; an entry that states none yields 0.
func contextWindowFromEntry(entry map[string]any) int {
	keys := []string{
		"context_window", "context_length", "max_context_tokens", "max_context_length",
		"max_input_tokens", "max_model_len", "n_ctx", "n_ctx_train",
	}
	for _, key := range keys {
		if window := intFromJSON(entry[key]); window > 0 {
			return window
		}
	}
	if meta, ok := entry["meta"].(map[string]any); ok {
		for _, key := range []string{"n_ctx_train", "n_ctx", "context_length"} {
			if window := intFromJSON(meta[key]); window > 0 {
				return window
			}
		}
	}
	return 0
}

func intFromJSON(value any) int {
	switch typed := value.(type) {
	case float64:
		if typed <= 0 {
			return 0
		}
		return int(typed)
	case int:
		return typed
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil || parsed <= 0 {
			return 0
		}
		return int(parsed)
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0
		}
		var parsed int
		if _, err := fmt.Sscanf(trimmed, "%d", &parsed); err != nil || parsed <= 0 {
			return 0
		}
		return parsed
	default:
		return 0
	}
}
