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
	App          AppConfig         `mapstructure:"app"`
	HTTP         HTTPConfig        `mapstructure:"http"`
	GRPC         GRPCConfig        `mapstructure:"grpc"`
	Database     DatabaseConfig    `mapstructure:"database"`
	Auth         AuthConfig        `mapstructure:"auth"`
	Personality  PersonalityConfig `mapstructure:"personality"`
	Sentry       SentryConfig      `mapstructure:"sentry"`
	Agents       AgentsConfig      `mapstructure:"agents"`
	ProvidersDir string            `mapstructure:"providersDir"`
	Providers    []ProviderConfig  `mapstructure:"providers"`
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
}

type PersonalityConfig struct {
	MaxHistoryMessages int           `mapstructure:"maxHistoryMessages"`
	SSEHeartbeat       time.Duration `mapstructure:"sseHeartbeat"`
}

type SentryConfig struct {
	DSN              string  `mapstructure:"dsn"`
	TracesSampleRate float64 `mapstructure:"tracesSampleRate"`
	Environment      string  `mapstructure:"environment"`
	Release          string  `mapstructure:"release"`
}

type AgentsConfig struct {
	Dir   string        `mapstructure:"dir"`
	Items []AgentConfig `mapstructure:"items"`
}

type AgentConfig struct {
	ID                  string   `mapstructure:"id"`
	Name                string   `mapstructure:"name"`
	Description         string   `mapstructure:"description"`
	SystemPrompt        string   `mapstructure:"systemPrompt"`
	Model               string   `mapstructure:"model"`
	Temperature         *float32 `mapstructure:"temperature"`
	TopP                *float32 `mapstructure:"topP"`
	MaxCompletionTokens *int     `mapstructure:"maxCompletionTokens"`
	ToolScopes          []string `mapstructure:"toolScopes"`
	Enabled             bool     `mapstructure:"enabled"`
}

type ProviderConfig struct {
	ID                  string        `mapstructure:"id"`
	Type                string        `mapstructure:"type"`
	APIKey              string        `mapstructure:"apiKey"`
	BaseURL             string        `mapstructure:"baseUrl"`
	ByAzure             bool          `mapstructure:"byAzure"`
	APIVersion          string        `mapstructure:"apiVersion"`
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
	v.SetDefault("personality.maxHistoryMessages", 24)
	v.SetDefault("personality.sseHeartbeat", 15*time.Second)
	v.SetDefault("sentry.dsn", "")
	v.SetDefault("sentry.tracesSampleRate", 0.01)
	v.SetDefault("sentry.environment", "")
	v.SetDefault("sentry.release", "")
	v.SetDefault("agents.dir", "")
	v.SetDefault("agents.items", []AgentConfig{})
	v.SetDefault("providersDir", "")
	v.SetDefault("providers", []ProviderConfig{})
}

func applyEnvOverrides(v *viper.Viper) {
	setEnvIfPresent(v, "database.dsn", "DATABASE_DSN")
	setEnvIfPresent(v, "auth.target", "AUTH_TARGET")
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
		cfg.Agents.Items = append(cfg.Agents.Items, extra.Agents.Items...)
	}

	return nil
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
