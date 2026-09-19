package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	HTTP         HTTPConfig         `mapstructure:"http"`
	GRPC         GRPCConfig         `mapstructure:"grpc"`
	Database     DatabaseConfig     `mapstructure:"database"`
	Auth         AuthConfig         `mapstructure:"auth"`
	Billing      BillingConfig      `mapstructure:"billing"`
	Personality  PersonalityConfig  `mapstructure:"personality"`
	Sentry       SentryConfig       `mapstructure:"sentry"`
	SolarNetwork SolarNetworkConfig `mapstructure:"solarNetwork"`
	OAuth        OAuthConfig        `mapstructure:"oauth"`
	WebSearch    WebSearchConfig    `mapstructure:"webSearch"`
	Agents       AgentsConfig       `mapstructure:"agents"`
	ProvidersDir string             `mapstructure:"providersDir"`
	Providers    []ProviderConfig   `mapstructure:"providers"`
}

// OAuthConfig controls user-scoped OAuth sessions. When enabled, personality
// obtains and refreshes per-(agent, account) user tokens via Stargate's OIDC
// device flow so user-scoped tools can act as the conversation user.
type OAuthConfig struct {
	Enabled      bool     `mapstructure:"enabled"`
	ClientID     string   `mapstructure:"clientId"`
	ClientSecret string   `mapstructure:"clientSecret"` // empty => public client
	Scopes       []string `mapstructure:"scopes"`
}

// WebSearchConfig configures the built-in web search engine. Engines are the
// public search front ends the server queries directly over HTTP; no search API
// or aggregator sits in between. Engines are queried in parallel and their
// results merged, so several can be combined.
//
// Agents reach it through the "web_search" ability; the HTTP endpoint is
// available whenever the feature is enabled.
type WebSearchConfig struct {
	Enabled      bool          `mapstructure:"enabled"`
	DefaultLimit int           `mapstructure:"defaultLimit"`
	MaxLimit     int           `mapstructure:"maxLimit"`
	Timeout      time.Duration `mapstructure:"timeout"`
	CacheTTL     time.Duration `mapstructure:"cacheTTL"`
	Language     string        `mapstructure:"language"`
	UserAgent    string        `mapstructure:"userAgent"`
	// Mode decides how engines are queried: "parallel" (default) queries every
	// engine at once and merges the results, "prefer" walks them in configured
	// order and stops at the first that answers, which keeps a paid API from
	// being billed on every query.
	Mode    string                  `mapstructure:"mode"`
	Engines []WebSearchEngineConfig `mapstructure:"engines"`
	Crawl   WebSearchCrawlConfig    `mapstructure:"crawl"`
}

// WebSearchEngineConfig is one search engine. Type selects the parser and the
// request shape; scraped engines need only a type, API-backed engines need
// apiKey, and baseUrl overrides the endpoint for proxies, self-hosted mirrors,
// and tests.
type WebSearchEngineConfig struct {
	ID      string        `mapstructure:"id"`
	Type    string        `mapstructure:"type"`
	Enabled *bool         `mapstructure:"enabled"`
	APIKey  string        `mapstructure:"apiKey"`
	BaseURL string        `mapstructure:"baseUrl"`
	Region  string        `mapstructure:"region"`
	Timeout time.Duration `mapstructure:"timeout"`
	// Price is what one query through this engine costs the caller, in the
	// billing currency (normally golds). Empty or "0" means the engine is free.
	// API-backed engines are required to state it so a paid call is never
	// silently free.
	Price string `mapstructure:"price"`
}

// WebSearchCrawlConfig controls how pages discovered by the engines are read
// into the local index. Crawling is what makes results answerable later without
// a live engine: extracted page text is stored and searched locally.
type WebSearchCrawlConfig struct {
	Enabled          bool          `mapstructure:"enabled"`
	MaxPagesPerQuery int           `mapstructure:"maxPagesPerQuery"`
	PageTimeout      time.Duration `mapstructure:"pageTimeout"`
	PerHostDelay     time.Duration `mapstructure:"perHostDelay"`
	MaxPageBytes     int64         `mapstructure:"maxPageBytes"`
}

// BillingConfig controls metered Personality usage. Amounts are decimal strings
// in the configured wallet currency (normally golds) to avoid float rounding.
type BillingConfig struct {
	Enabled              bool              `mapstructure:"enabled"`
	Target               string            `mapstructure:"target"`
	UseTLS               bool              `mapstructure:"useTLS"`
	TLSSkipVerify        bool              `mapstructure:"tlsSkipVerify"`
	PayeeAccountID       string            `mapstructure:"payeeAccountId"`
	Currency             string            `mapstructure:"currency"`
	ServiceFeePercentage string            `mapstructure:"serviceFeePercentage"`
	HourlyUsageLimits    map[string]string `mapstructure:"hourlyUsageLimits"`
	DailyUsageLimits     map[string]string `mapstructure:"dailyUsageLimits"`
	InstantBillingWall   string            `mapstructure:"instantBillingWall"`
}

type HTTPConfig struct {
	Port string `mapstructure:"port"`
}

type GRPCConfig struct {
	Port     string `mapstructure:"port"`
	UseTLS   bool   `mapstructure:"useTLS"`
	CertFile string `mapstructure:"certFile"`
	KeyFile  string `mapstructure:"keyFile"`
}

type DatabaseConfig struct {
	DSN string `mapstructure:"dsn"`
}

type AuthConfig struct {
	Target           string `mapstructure:"target"`
	UseTLS           bool   `mapstructure:"useTLS"`
	TLSSkipVerify    bool   `mapstructure:"tlsSkipVerify"`
	AllowDevIDs      bool   `mapstructure:"allowDevIds"`
	Offline          bool   `mapstructure:"offline"`
	OfflineAccountID string `mapstructure:"offlineAccountId"`
	AutonomousSecret string `mapstructure:"autonomousSecret"`
}

type PersonalityConfig struct {
	MaxHistoryMessages    int                    `mapstructure:"maxHistoryMessages"`
	SSEHeartbeat          time.Duration          `mapstructure:"sseHeartbeat"`
	ChatInboundDebounce   time.Duration          `mapstructure:"chatInboundDebounce"`
	VisionModel           string                 `mapstructure:"visionModel"`
	DefaultEmbeddingModel string                 `mapstructure:"defaultEmbeddingModel"`
	OnlyAllowListedModels bool                   `mapstructure:"onlyAllowListedModels"`
	DynamicSkills         bool                   `mapstructure:"dynamicSkills"`
	Surfing               SurfingConfig          `mapstructure:"surfing"`
	PerkTiers             map[int]PerkTierConfig `mapstructure:"perkTiers"`
}

type SurfingConfig struct {
	Enabled  bool          `mapstructure:"enabled"`
	Interval time.Duration `mapstructure:"interval"`
	Prompt   string        `mapstructure:"prompt"`
}

type PerkTierConfig struct {
	MaxHistoryMessages  *int     `mapstructure:"maxHistoryMessages"`
	MaxCompletionTokens *int     `mapstructure:"maxCompletionTokens"`
	BlockedSkills       []string `mapstructure:"blockedSkills"`
	AllowVision         *bool    `mapstructure:"allowVision"`
	AllowFileSummary    *bool    `mapstructure:"allowFileSummary"`
}

type ModelPerkOverride struct {
	Blocked             *bool `json:"blocked,omitempty" mapstructure:"blocked"`
	MaxCompletionTokens *int  `json:"max_completion_tokens,omitempty" mapstructure:"maxCompletionTokens"`
}

type AgentPerkOverride struct {
	MaxCompletionTokens *int `mapstructure:"maxCompletionTokens"`
}

type SentryConfig struct {
	DSN              string  `mapstructure:"dsn"`
	TracesSampleRate float64 `mapstructure:"tracesSampleRate"`
	Environment      string  `mapstructure:"environment"`
	Release          string  `mapstructure:"release"`
}

type SolarNetworkConfig struct {
	BaseURL string `mapstructure:"baseUrl"`
}

type AgentsConfig struct {
	Dir   string        `mapstructure:"dir"`
	Items []AgentConfig `mapstructure:"items"`
}

type AgentConfig struct {
	ID                      string                       `mapstructure:"id"`
	Name                    string                       `mapstructure:"name"`
	Description             string                       `mapstructure:"description"`
	SystemPrompt            string                       `mapstructure:"systemPrompt"`
	SystemPromptFile        string                       `mapstructure:"systemPromptFile"`
	Model                   string                       `mapstructure:"model"`
	Temperature             *float32                     `mapstructure:"temperature"`
	TopP                    *float32                     `mapstructure:"topP"`
	MaxCompletionTokens     *int                         `mapstructure:"maxCompletionTokens"`
	ChatMaxCompletionTokens *int                         `mapstructure:"chatMaxCompletionTokens"`
	BillingMultiplier       *float64                     `mapstructure:"billingMultiplier"`
	DisableThinking         *bool                        `mapstructure:"disableThinking"`
	Abilities               []string                     `mapstructure:"abilities"`
	ToolScopes              []string                     `mapstructure:"toolScopes"`
	Autonomous              AgentAutonomousConfig        `mapstructure:"autonomous"`
	SolarNetworkIntegration AgentSolarNetworkIntegration `mapstructure:"solar-network-integration"`
	Enabled                 bool                         `mapstructure:"enabled"`
	PerkOverrides           map[int]AgentPerkOverride    `mapstructure:"perkOverrides"`
	sourceDir               string                       `mapstructure:"-"`
}

type AgentAutonomousConfig struct {
	WakeInterval time.Duration `mapstructure:"wakeInterval"`
	WakePrompt   string        `mapstructure:"wakePrompt"`
}

type AgentSolarNetworkIntegration struct {
	AccountName   string `mapstructure:"accountName"`
	AccessToken   string `mapstructure:"accessToken"`
	PublisherName string `mapstructure:"publisherName"`
}

type ProviderConfig struct {
	ID                  string        `mapstructure:"id"`
	Type                string        `mapstructure:"type"`
	APIKey              string        `mapstructure:"apiKey"`
	BaseURL             string        `mapstructure:"baseUrl"`
	SupportsVision      *bool         `mapstructure:"supportsVision"`
	Timeout             time.Duration `mapstructure:"timeout"`
	MaxCompletionTokens int           `mapstructure:"maxCompletionTokens"`
	Temperature         float32       `mapstructure:"temperature"`
	TopP                float32       `mapstructure:"topP"`
	Models              []ModelConfig `mapstructure:"models"`
}

type ModelConfig struct {
	Name                string                    `mapstructure:"name"`
	Type                string                    `mapstructure:"type"`
	Modalities          []string                  `mapstructure:"modalities"`
	MaxCompletionTokens int                       `mapstructure:"maxCompletionTokens"`
	Temperature         float32                   `mapstructure:"temperature"`
	TopP                float32                   `mapstructure:"topP"`
	Pricing             *ModelPricingConfig       `mapstructure:"pricing"`
	PerkOverrides       map[int]ModelPerkOverride `mapstructure:"perkOverrides"`
}

// ModelPricingConfig prices one million input/output tokens in one Wallet
// currency. An omitted currency inherits billing.currency.
// A model without a pricing section is intentionally free.
type ModelPricingConfig struct {
	Currency string  `json:"currency,omitempty" mapstructure:"currency"`
	Input    *string `json:"input,omitempty" mapstructure:"input"`
	Output   *string `json:"output,omitempty" mapstructure:"output"`
}

func (m ModelConfig) SupportsModality(modality string) bool {
	for _, v := range m.Modalities {
		if strings.EqualFold(strings.TrimSpace(v), modality) {
			return true
		}
	}
	return false
}

func (m ModelConfig) IsEmbedding() bool {
	return strings.EqualFold(strings.TrimSpace(m.Type), "embedding")
}

func (p ProviderConfig) ResolveModel(modelName string) *ModelConfig {
	modelName = strings.TrimSpace(modelName)
	for i := range p.Models {
		if strings.EqualFold(strings.TrimSpace(p.Models[i].Name), modelName) {
			return &p.Models[i]
		}
	}
	return nil
}

type agentFile struct {
	Agents AgentsConfig `mapstructure:"agents"`
}

type providerFile struct {
	Providers []ProviderConfig `mapstructure:"providers"`
}

func Load(configPath string) (*Config, error) {
	v := viper.New()
	v.SetConfigType("toml")
	setDefaults(v)
	applyEnvOverrides(v)

	if configPath != "" {
		v.SetConfigFile(configPath)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	if err := loadAgentFiles(&cfg); err != nil {
		return nil, err
	}
	if err := loadProviderFiles(&cfg); err != nil {
		return nil, err
	}
	if err := resolveAgentPromptFiles(&cfg, configPath); err != nil {
		return nil, err
	}
	if err := validateSolarNetworkConfig(&cfg); err != nil {
		return nil, err
	}
	if err := validateOAuthConfig(&cfg); err != nil {
		return nil, err
	}
	normalizeWebSearchConfig(&cfg)
	if err := validateWebSearchConfig(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("http.port", "8090")
	v.SetDefault("grpc.port", "9095")
	v.SetDefault("grpc.useTLS", false)
	v.SetDefault("grpc.certFile", "")
	v.SetDefault("grpc.keyFile", "")
	v.SetDefault("database.dsn", "")
	v.SetDefault("auth.target", "stargate:9090")
	v.SetDefault("auth.useTLS", true)
	v.SetDefault("auth.tlsSkipVerify", true)
	v.SetDefault("auth.allowDevIds", true)
	v.SetDefault("auth.offline", false)
	v.SetDefault("auth.offlineAccountId", "local-dev")
	v.SetDefault("auth.autonomousSecret", "")
	v.SetDefault("billing.enabled", false)
	v.SetDefault("billing.target", "")
	v.SetDefault("billing.useTLS", false)
	v.SetDefault("billing.tlsSkipVerify", false)
	v.SetDefault("billing.payeeAccountId", "")
	v.SetDefault("billing.currency", "golds")
	v.SetDefault("billing.serviceFeePercentage", "0")
	v.SetDefault("billing.hourlyUsageLimits", map[string]string{})
	v.SetDefault("billing.dailyUsageLimits", map[string]string{})
	v.SetDefault("billing.instantBillingWall", "0")
	v.SetDefault("personality.maxHistoryMessages", 24)
	v.SetDefault("personality.sseHeartbeat", 15*time.Second)
	v.SetDefault("personality.chatInboundDebounce", 2*time.Second)
	v.SetDefault("personality.defaultEmbeddingModel", "")
	v.SetDefault("personality.dynamicSkills", true)
	v.SetDefault("personality.surfing.enabled", false)
	v.SetDefault("personality.surfing.interval", 1*time.Hour)
	v.SetDefault("sentry.dsn", "")
	v.SetDefault("sentry.tracesSampleRate", 0.01)
	v.SetDefault("sentry.environment", "")
	v.SetDefault("sentry.release", "")
	v.SetDefault("solarNetwork.baseUrl", "")
	v.SetDefault("oauth.enabled", false)
	v.SetDefault("oauth.clientId", "")
	v.SetDefault("oauth.clientSecret", "")
	v.SetDefault("oauth.scopes", []string{"*"})
	v.SetDefault("webSearch.enabled", false)
	v.SetDefault("webSearch.defaultLimit", 5)
	v.SetDefault("webSearch.maxLimit", 10)
	v.SetDefault("webSearch.timeout", 15*time.Second)
	v.SetDefault("webSearch.cacheTTL", 5*time.Minute)
	v.SetDefault("webSearch.language", "")
	v.SetDefault("webSearch.userAgent", "")
	v.SetDefault("webSearch.mode", "parallel")
	v.SetDefault("webSearch.engines", []WebSearchEngineConfig{})
	v.SetDefault("webSearch.crawl.enabled", true)
	v.SetDefault("webSearch.crawl.maxPagesPerQuery", 3)
	v.SetDefault("webSearch.crawl.pageTimeout", 8*time.Second)
	v.SetDefault("webSearch.crawl.perHostDelay", 1*time.Second)
	v.SetDefault("webSearch.crawl.maxPageBytes", int64(2<<20))
	v.SetDefault("agents.dir", "")
	v.SetDefault("agents.items", []AgentConfig{})
	v.SetDefault("providersDir", "")
	v.SetDefault("providers", []ProviderConfig{})
}

func applyEnvOverrides(v *viper.Viper) {
	setEnvIfPresent(v, "database.dsn", "DATABASE_DSN")
	setEnvIfPresent(v, "auth.target", "AUTH_TARGET")
	setEnvIfPresent(v, "auth.autonomousSecret", "AUTONOMOUS_SECRET")
	setEnvIfPresent(v, "billing.target", "BILLING_TARGET")
}

func setEnvIfPresent(v *viper.Viper, key, env string) {
	if value := strings.TrimSpace(os.Getenv(env)); value != "" {
		v.Set(key, value)
	}
}

func loadAgentFiles(cfg *Config) error {
	dir := strings.TrimSpace(cfg.Agents.Dir)
	if dir == "" {
		return nil
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return fmt.Errorf("glob agent configs: %w", err)
	}
	sort.Strings(matches)

	for _, path := range matches {
		v := viper.New()
		v.SetConfigFile(path)
		v.SetConfigType("toml")
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("read agent config %s: %w", path, err)
		}

		var extra agentFile
		if err := v.Unmarshal(&extra); err != nil {
			return fmt.Errorf("unmarshal agent config %s: %w", path, err)
		}
		for i := range extra.Agents.Items {
			extra.Agents.Items[i].sourceDir = filepath.Dir(path)
		}
		cfg.Agents.Items = append(cfg.Agents.Items, extra.Agents.Items...)
	}

	return nil
}

func resolveAgentPromptFiles(cfg *Config, configPath string) error {
	baseDir := "."
	if strings.TrimSpace(configPath) != "" {
		baseDir = filepath.Dir(configPath)
	}

	for i := range cfg.Agents.Items {
		cfg.Agents.Items[i].Abilities = mergeAgentAbilities(cfg.Agents.Items[i].Abilities, cfg.Agents.Items[i].ToolScopes)
		if strings.TrimSpace(cfg.Agents.Items[i].sourceDir) == "" {
			cfg.Agents.Items[i].sourceDir = baseDir
		}
		promptFile := strings.TrimSpace(cfg.Agents.Items[i].SystemPromptFile)
		if promptFile == "" {
			continue
		}

		resolved, err := resolvePromptPath(baseDir, cfg.Agents.Items[i].sourceDir, promptFile)
		if err != nil {
			return fmt.Errorf("resolve system prompt file for agent %q: %w", cfg.Agents.Items[i].ID, err)
		}

		content, err := os.ReadFile(resolved)
		if err != nil {
			return fmt.Errorf("read system prompt file for agent %q: %w", cfg.Agents.Items[i].ID, err)
		}
		cfg.Agents.Items[i].SystemPrompt = string(content)
	}

	return nil
}

func mergeAgentAbilities(primary, legacy []string) []string {
	seen := make(map[string]struct{}, len(primary)+len(legacy))
	result := make([]string, 0, len(primary)+len(legacy))

	appendItems := func(items []string) {
		for _, item := range items {
			value := strings.TrimSpace(item)
			if value == "" {
				continue
			}
			key := strings.ToLower(value)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, value)
		}
	}

	appendItems(primary)
	appendItems(legacy)
	return result
}

func validateSolarNetworkConfig(cfg *Config) error {
	baseURL := strings.TrimSpace(cfg.SolarNetwork.BaseURL)
	requiresSolarBaseURL := false

	for _, agent := range cfg.Agents.Items {
		if !agent.Enabled || !hasAbility(agent.Abilities, "chat") {
			continue
		}

		requiresSolarBaseURL = true
		if strings.TrimSpace(agent.SolarNetworkIntegration.AccountName) == "" {
			return fmt.Errorf("agent %q chat ability requires solar-network-integration.accountName", strings.TrimSpace(agent.ID))
		}
		if strings.TrimSpace(agent.SolarNetworkIntegration.AccessToken) == "" {
			return fmt.Errorf("agent %q chat ability requires solar-network-integration.accessToken", strings.TrimSpace(agent.ID))
		}
	}

	if requiresSolarBaseURL && baseURL == "" {
		return fmt.Errorf("solarNetwork.baseUrl is required when an enabled agent has chat ability")
	}

	return nil
}

func validateOAuthConfig(cfg *Config) error {
	if !cfg.OAuth.Enabled {
		return nil
	}
	if strings.TrimSpace(cfg.OAuth.ClientID) == "" {
		return fmt.Errorf("oauth.clientId is required when oauth is enabled")
	}
	if strings.TrimSpace(cfg.SolarNetwork.BaseURL) == "" {
		return fmt.Errorf("solarNetwork.baseUrl is required when oauth is enabled")
	}
	return nil
}

// validAmount reports whether v is a non-negative decimal amount, the shape the
// billing ledger stores for a charge.
func validAmount(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	digits, dot := false, false
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			digits = true
		case r == '.' && !dot:
			dot = true
		default:
			return false
		}
	}
	return digits
}

func hasAbility(abilities []string, want string) bool {
	normalizedWant := strings.TrimSpace(strings.ToLower(want))
	for _, ability := range abilities {
		if strings.TrimSpace(strings.ToLower(ability)) == normalizedWant {
			return true
		}
	}
	return false
}

// defaultWebSearchEngines are used when the feature is enabled without an
// explicit engine list. DuckDuckGo's no-JavaScript endpoint is the default: it
// answers server-side requests with a parseable result page. Bing is supported
// but opt-in, because it replies to longer queries with HTTP 200 pages about
// unrelated topics.
var defaultWebSearchEngines = []string{"duckduckgo"}

// normalizeWebSearchConfig fills in defaults, resolves the default engine set,
// and clamps inconsistent values so runtime search behavior never depends on a
// zero-valued config.
func normalizeWebSearchConfig(cfg *Config) {
	if cfg.WebSearch.DefaultLimit < 1 {
		cfg.WebSearch.DefaultLimit = 5
	}
	if cfg.WebSearch.MaxLimit < cfg.WebSearch.DefaultLimit {
		cfg.WebSearch.MaxLimit = cfg.WebSearch.DefaultLimit
	}
	if cfg.WebSearch.Timeout <= 0 {
		cfg.WebSearch.Timeout = 15 * time.Second
	}
	if cfg.WebSearch.CacheTTL < 0 {
		cfg.WebSearch.CacheTTL = 0
	}
	cfg.WebSearch.Mode = strings.ToLower(strings.TrimSpace(cfg.WebSearch.Mode))
	if cfg.WebSearch.Mode == "" {
		cfg.WebSearch.Mode = "parallel"
	}

	if cfg.WebSearch.Enabled {
		kept := make([]WebSearchEngineConfig, 0, len(cfg.WebSearch.Engines))
		for _, engine := range cfg.WebSearch.Engines {
			if engine.Enabled != nil && !*engine.Enabled {
				continue
			}
			if strings.TrimSpace(engine.ID) == "" {
				engine.ID = strings.TrimSpace(engine.Type)
			}
			kept = append(kept, engine)
		}
		if len(kept) == 0 && len(cfg.WebSearch.Engines) == 0 {
			for _, engineType := range defaultWebSearchEngines {
				kept = append(kept, WebSearchEngineConfig{ID: engineType, Type: engineType})
			}
		}
		cfg.WebSearch.Engines = kept
	}

	if cfg.WebSearch.Crawl.MaxPagesPerQuery <= 0 {
		cfg.WebSearch.Crawl.MaxPagesPerQuery = 3
	}
	if cfg.WebSearch.Crawl.MaxPagesPerQuery > 10 {
		cfg.WebSearch.Crawl.MaxPagesPerQuery = 10
	}
	if cfg.WebSearch.Crawl.PageTimeout <= 0 {
		cfg.WebSearch.Crawl.PageTimeout = 8 * time.Second
	}
	if cfg.WebSearch.Crawl.PerHostDelay < 0 {
		cfg.WebSearch.Crawl.PerHostDelay = 0
	}
	if cfg.WebSearch.Crawl.MaxPageBytes <= 0 {
		cfg.WebSearch.Crawl.MaxPageBytes = 2 << 20
	}
}

func validateWebSearchConfig(cfg *Config) error {
	requiresSearch := false
	for _, agent := range cfg.Agents.Items {
		if agent.Enabled && hasAbility(agent.Abilities, "web_search") {
			requiresSearch = true
			break
		}
	}

	if !cfg.WebSearch.Enabled {
		if requiresSearch {
			return fmt.Errorf("webSearch.enabled must be true when an enabled agent has web_search ability")
		}
		return nil
	}
	if len(cfg.WebSearch.Engines) == 0 {
		return fmt.Errorf("webSearch.engines requires at least one engine when webSearch.enabled")
	}

	seen := make(map[string]bool, len(cfg.WebSearch.Engines))
	for index, engine := range cfg.WebSearch.Engines {
		engineType := strings.ToLower(strings.TrimSpace(engine.Type))
		id := strings.TrimSpace(engine.ID)
		if id == "" {
			id = engineType
		}
		if id == "" {
			return fmt.Errorf("webSearch.engines[%d] requires id or type", index)
		}
		if seen[id] {
			return fmt.Errorf("duplicate web search engine id %q", id)
		}
		seen[id] = true

		switch engineType {
		case "duckduckgo", "bing", "google":
		case "exa", "tavily":
			// API-backed engines answer from any egress, which is what makes web
			// search usable from a datacenter IP that scraped engines block.
			if strings.TrimSpace(engine.APIKey) == "" {
				return fmt.Errorf("web search engine %q requires apiKey", id)
			}
			if !validAmount(engine.Price) {
				return fmt.Errorf("web search engine %q requires price, the amount one query costs in the billing currency (use \"0\" for a free engine)", id)
			}
		default:
			return fmt.Errorf("web search engine %q uses unsupported type %q", id, engine.Type)
		}
		if strings.TrimSpace(engine.Price) != "" && !validAmount(engine.Price) {
			return fmt.Errorf("web search engine %q has an invalid price %q", id, engine.Price)
		}
	}

	switch cfg.WebSearch.Mode {
	case "parallel", "prefer":
	default:
		return fmt.Errorf("webSearch.mode must be %q or %q, got %q", "parallel", "prefer", cfg.WebSearch.Mode)
	}

	return nil
}

func resolvePromptPath(baseDir, sourceDir, promptFile string) (string, error) {
	if filepath.IsAbs(promptFile) {
		return promptFile, nil
	}

	candidates := []string{
		filepath.Join(sourceDir, promptFile),
		filepath.Join(baseDir, promptFile),
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("file %q not found relative to %q or %q", promptFile, sourceDir, baseDir)
}

func loadProviderFiles(cfg *Config) error {
	dir := strings.TrimSpace(cfg.ProvidersDir)
	if dir == "" {
		return nil
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return fmt.Errorf("glob provider configs: %w", err)
	}
	sort.Strings(matches)

	for _, path := range matches {
		v := viper.New()
		v.SetConfigFile(path)
		v.SetConfigType("toml")
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("read provider config %s: %w", path, err)
		}

		var extra providerFile
		if err := v.Unmarshal(&extra); err != nil {
			return fmt.Errorf("unmarshal provider config %s: %w", path, err)
		}
		cfg.Providers = append(cfg.Providers, extra.Providers...)
	}

	return nil
}
