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
	App         AppConfig         `mapstructure:"app"`
	HTTP        HTTPConfig        `mapstructure:"http"`
	GRPC        GRPCConfig        `mapstructure:"grpc"`
	Database    DatabaseConfig    `mapstructure:"database"`
	Auth        AuthConfig        `mapstructure:"auth"`
	LLM         LLMConfig         `mapstructure:"llm"`
	Personality PersonalityConfig `mapstructure:"personality"`
	Sentry      SentryConfig      `mapstructure:"sentry"`
	Agents      AgentsConfig      `mapstructure:"agents"`
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
	Target        string `mapstructure:"target"`
	UseTLS        bool   `mapstructure:"useTLS"`
	TLSSkipVerify bool   `mapstructure:"tlsSkipVerify"`
	AllowDevIDs   bool   `mapstructure:"allowDevIds"`
}

type LLMConfig struct {
	Provider            string        `mapstructure:"provider"`
	APIKey              string        `mapstructure:"apiKey"`
	BaseURL             string        `mapstructure:"baseUrl"`
	Model               string        `mapstructure:"model"`
	ByAzure             bool          `mapstructure:"byAzure"`
	APIVersion          string        `mapstructure:"apiVersion"`
	Timeout             time.Duration `mapstructure:"timeout"`
	MaxCompletionTokens int           `mapstructure:"maxCompletionTokens"`
	Temperature         float32       `mapstructure:"temperature"`
	TopP                float32       `mapstructure:"topP"`
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

type agentFile struct {
	Agents AgentsConfig `mapstructure:"agents"`
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
	v.SetDefault("llm.provider", "openai")
	v.SetDefault("llm.apiKey", "")
	v.SetDefault("llm.baseUrl", "")
	v.SetDefault("llm.model", "gpt-4.1-mini")
	v.SetDefault("llm.byAzure", false)
	v.SetDefault("llm.apiVersion", "")
	v.SetDefault("llm.timeout", 90*time.Second)
	v.SetDefault("llm.maxCompletionTokens", 2048)
	v.SetDefault("llm.temperature", 0.7)
	v.SetDefault("llm.topP", 1.0)
	v.SetDefault("personality.maxHistoryMessages", 24)
	v.SetDefault("personality.sseHeartbeat", 15*time.Second)
	v.SetDefault("sentry.dsn", "")
	v.SetDefault("sentry.tracesSampleRate", 0.01)
	v.SetDefault("sentry.environment", "")
	v.SetDefault("sentry.release", "")
	v.SetDefault("agents.dir", "")
	v.SetDefault("agents.items", []AgentConfig{})
}

func applyEnvOverrides(v *viper.Viper) {
	setEnvIfPresent(v, "database.dsn", "DATABASE_DSN")
	setEnvIfPresent(v, "llm.apiKey", "OPENAI_API_KEY")
	setEnvIfPresent(v, "llm.baseUrl", "OPENAI_BASE_URL")
	setEnvIfPresent(v, "llm.model", "OPENAI_MODEL")
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
