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
	App          AppConfig          `mapstructure:"app"`
	HTTP         HTTPConfig         `mapstructure:"http"`
	GRPC         GRPCConfig         `mapstructure:"grpc"`
	Database     DatabaseConfig     `mapstructure:"database"`
	Auth         AuthConfig         `mapstructure:"auth"`
	Personality  PersonalityConfig  `mapstructure:"personality"`
	Sentry       SentryConfig       `mapstructure:"sentry"`
	SolarNetwork SolarNetworkConfig `mapstructure:"solarNetwork"`
	Agents       AgentsConfig       `mapstructure:"agents"`
	ProvidersDir string             `mapstructure:"providersDir"`
	Providers    []ProviderConfig   `mapstructure:"providers"`
}

type AppConfig struct {
	Name string `mapstructure:"name"`
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
	MaxHistoryMessages int           `mapstructure:"maxHistoryMessages"`
	SSEHeartbeat       time.Duration `mapstructure:"sseHeartbeat"`
	SolarInboundDebounce time.Duration `mapstructure:"solarInboundDebounce"`
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
	DisableThinking         *bool                        `mapstructure:"disableThinking"`
	Abilities               []string                     `mapstructure:"abilities"`
	ToolScopes              []string                     `mapstructure:"toolScopes"`
	Autonomous              AgentAutonomousConfig        `mapstructure:"autonomous"`
	SolarNetworkIntegration AgentSolarNetworkIntegration `mapstructure:"solar-network-integration"`
	Enabled                 bool                         `mapstructure:"enabled"`
	sourceDir               string                       `mapstructure:"-"`
}

type AgentAutonomousConfig struct {
	WakeInterval time.Duration `mapstructure:"wakeInterval"`
	WakePrompt   string        `mapstructure:"wakePrompt"`
}

type AgentSolarNetworkIntegration struct {
	AccountName string `mapstructure:"accountName"`
	AccessToken string `mapstructure:"accessToken"`
}

type ProviderConfig struct {
	ID                  string        `mapstructure:"id"`
	Type                string        `mapstructure:"type"`
	APIKey              string        `mapstructure:"apiKey"`
	BaseURL             string        `mapstructure:"baseUrl"`
	ByAzure             bool          `mapstructure:"byAzure"`
	APIVersion          string        `mapstructure:"apiVersion"`
	SupportsVision      *bool         `mapstructure:"supportsVision"`
	Timeout             time.Duration `mapstructure:"timeout"`
	MaxCompletionTokens int           `mapstructure:"maxCompletionTokens"`
	Temperature         float32       `mapstructure:"temperature"`
	TopP                float32       `mapstructure:"topP"`
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

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("app.name", "PersonalityCore")
	v.SetDefault("http.port", "8090")
	v.SetDefault("grpc.port", "9095")
	v.SetDefault("grpc.useTLS", false)
	v.SetDefault("grpc.certFile", "")
	v.SetDefault("grpc.keyFile", "")
	v.SetDefault("database.dsn", "")
	v.SetDefault("auth.target", "")
	v.SetDefault("auth.useTLS", false)
	v.SetDefault("auth.tlsSkipVerify", false)
	v.SetDefault("auth.allowDevIds", true)
	v.SetDefault("auth.offline", false)
	v.SetDefault("auth.offlineAccountId", "local-dev")
	v.SetDefault("auth.autonomousSecret", "")
	v.SetDefault("personality.maxHistoryMessages", 24)
	v.SetDefault("personality.sseHeartbeat", 15*time.Second)
	v.SetDefault("personality.solarInboundDebounce", 2*time.Second)
	v.SetDefault("sentry.dsn", "")
	v.SetDefault("sentry.tracesSampleRate", 0.01)
	v.SetDefault("sentry.environment", "")
	v.SetDefault("sentry.release", "")
	v.SetDefault("solarNetwork.baseUrl", "")
	v.SetDefault("agents.dir", "")
	v.SetDefault("agents.items", []AgentConfig{})
	v.SetDefault("providersDir", "")
	v.SetDefault("providers", []ProviderConfig{})
}

func applyEnvOverrides(v *viper.Viper) {
	setEnvIfPresent(v, "database.dsn", "DATABASE_DSN")
	setEnvIfPresent(v, "auth.target", "AUTH_TARGET")
	setEnvIfPresent(v, "auth.autonomousSecret", "AUTONOMOUS_SECRET")
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

func hasAbility(abilities []string, want string) bool {
	normalizedWant := strings.TrimSpace(strings.ToLower(want))
	for _, ability := range abilities {
		if strings.TrimSpace(strings.ToLower(ability)) == normalizedWant {
			return true
		}
	}
	return false
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
