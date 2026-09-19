package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoad_MergesAgentDirectory(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	agentDir := filepath.Join(dir, "agents")
	providerDir := filepath.Join(dir, "models.d")
	promptDir := filepath.Join(dir, "prompts")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(providerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "inline-system.md"), []byte("inline prompt from file"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(mainFile, []byte(`
providersDir = "`+providerDir+`"

[database]
dsn = "postgres://example"

[solarNetwork]
baseUrl = "https://solar.example"

[agents]
dir = "`+agentDir+`"

[[agents.items]]
id = "inline"
name = "Inline"
systemPromptFile = "./prompts/inline-system.md"
enabled = true

[[providers]]
id = "openai"
type = "openai"
apiKey = "inline-key"
timeout = "30s"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(agentDir, "extra.toml"), []byte(`
[agents]
[[agents.items]]
id = "extra"
name = "Extra"
systemPrompt = "extra"
enabled = true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(providerDir, "extra.toml"), []byte(`
[[providers]]
id = "extra"
type = "openai"
apiKey = "extra-key"
baseUrl = "https://example.invalid"
timeout = "45s"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(mainFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Agents.Items) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(cfg.Agents.Items))
	}
	if cfg.Agents.Items[0].SystemPrompt != "inline prompt from file" {
		t.Fatalf("expected prompt file to be loaded, got %q", cfg.Agents.Items[0].SystemPrompt)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(cfg.Providers))
	}
}

func TestLoad_AgentPromptFileSupportsRootRelativePath(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	agentDir := filepath.Join(dir, "agents.d")
	providerDir := filepath.Join(dir, "models.d")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(providerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(agentDir, "michan.md"), []byte("prompt from repo-root-relative path"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainFile, []byte(`
providersDir = "`+providerDir+`"

[solarNetwork]
baseUrl = "https://solar.example"

[agents]
dir = "`+agentDir+`"

[[providers]]
id = "deepseek"
type = "openai"
apiKey = "test-key"
timeout = "30s"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "michan.toml"), []byte(`
[agents]
[[agents.items]]
id = "michan"
name = "Michan"
systemPromptFile = "agents.d/michan.md"
enabled = true
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(mainFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Agents.Items) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(cfg.Agents.Items))
	}
	if got := cfg.Agents.Items[0].SystemPrompt; got != "prompt from repo-root-relative path" {
		t.Fatalf("prompt = %q", got)
	}
}

func TestLoad_ChatAbilityRequiresSolarIntegration(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(mainFile, []byte(`
[database]
dsn = "postgres://example"

[solarNetwork]
baseUrl = "https://solar.example"

[[providers]]
id = "openai"
type = "openai"
apiKey = "test-key"
timeout = "30s"

[[agents.items]]
id = "chatty"
name = "Chatty"
model = "openai/gpt-4.1-mini"
abilities = ["chat"]
enabled = true
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(mainFile)
	if err == nil {
		t.Fatal("expected chat integration validation error")
	}
}

func TestLoad_ChatAbilityLoadsSolarIntegration(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(mainFile, []byte(`
[database]
dsn = "postgres://example"

[solarNetwork]
baseUrl = "https://solar.example"

[[providers]]
id = "openai"
type = "openai"
apiKey = "test-key"
timeout = "30s"

[[agents.items]]
id = "chatty"
name = "Chatty"
model = "openai/gpt-4.1-mini"
abilities = ["chat"]
enabled = true

[agents.items.solar-network-integration]
accountName = "bot-account"
accessToken = "secret-token"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(mainFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got := cfg.Agents.Items[0].SolarNetworkIntegration.AccountName; got != "bot-account" {
		t.Fatalf("expected accountName to load, got %q", got)
	}
}

func TestLoad_AutonomousConfigLoadsWakeSettings(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(mainFile, []byte(`
[database]
dsn = "postgres://example"

[[providers]]
id = "openai"
type = "openai"
apiKey = "test-key"
timeout = "30s"

[[agents.items]]
id = "autonomous-bot"
name = "Autonomous Bot"
model = "openai/gpt-4.1-mini"
abilities = ["autonomous"]
enabled = true

[agents.items.autonomous]
wakeInterval = "10m"
wakePrompt = "Check for anything worth proactively following up on."
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(mainFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.Agents.Items[0].Autonomous.WakeInterval; got != 10*time.Minute {
		t.Fatalf("expected wake interval 10m, got %v", got)
	}
	if got := cfg.Agents.Items[0].Autonomous.WakePrompt; got != "Check for anything worth proactively following up on." {
		t.Fatalf("unexpected wake prompt %q", got)
	}
}

func TestLoad_ChatMaxCompletionTokensLoads(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(mainFile, []byte(`
[database]
dsn = "postgres://example"

[solarNetwork]
baseUrl = "https://solar.example"

[[providers]]
id = "openai"
type = "openai"
apiKey = "test-key"
timeout = "30s"

[[agents.items]]
id = "chatty"
name = "Chatty"
model = "openai/gpt-4.1-mini"
maxCompletionTokens = 1024
chatMaxCompletionTokens = 160
abilities = ["chat"]
enabled = true

[agents.items.solar-network-integration]
accountName = "bot-account"
accessToken = "secret-token"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(mainFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Agents.Items[0].ChatMaxCompletionTokens == nil || *cfg.Agents.Items[0].ChatMaxCompletionTokens != 160 {
		t.Fatalf("expected chat max completion tokens 160, got %#v", cfg.Agents.Items[0].ChatMaxCompletionTokens)
	}
	if cfg.Agents.Items[0].MaxCompletionTokens == nil || *cfg.Agents.Items[0].MaxCompletionTokens != 1024 {
		t.Fatalf("expected normal max completion tokens 1024, got %#v", cfg.Agents.Items[0].MaxCompletionTokens)
	}
}

func TestLoad_PersonalityChatInboundDebounceLoads(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(mainFile, []byte(`
[database]
dsn = "postgres://example"

[personality]
chatInboundDebounce = "5s"

[[providers]]
id = "openai"
type = "openai"
apiKey = "test-key"
timeout = "30s"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(mainFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.Personality.ChatInboundDebounce; got != 5*time.Second {
		t.Fatalf("expected solar inbound debounce 5s, got %v", got)
	}
}

func TestLoad_DefaultEmbeddingModelAndModelTypeLoad(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(mainFile, []byte(`
[database]
dsn = "postgres://example"

[personality]
defaultEmbeddingModel = "openai/text-embedding-3-small"

[[providers]]
id = "openai"
type = "openai"
apiKey = "test-key"
timeout = "30s"

[[providers.models]]
name = "gpt-4.1-mini"

[[providers.models]]
name = "text-embedding-3-small"
type = "embedding"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(mainFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.Personality.DefaultEmbeddingModel; got != "openai/text-embedding-3-small" {
		t.Fatalf("expected default embedding model to load, got %q", got)
	}
	if len(cfg.Providers) != 1 || len(cfg.Providers[0].Models) != 2 {
		t.Fatalf("unexpected providers payload: %#v", cfg.Providers)
	}
	if cfg.Providers[0].Models[0].IsEmbedding() {
		t.Fatal("expected first model to default to completion")
	}
	if !cfg.Providers[0].Models[1].IsEmbedding() {
		t.Fatal("expected second model to be marked as embedding")
	}
}

func TestLoad_OAuthDefaultsScopes(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(mainFile, []byte(`
[database]
dsn = "postgres://example"

[solarNetwork]
baseUrl = "https://solar.example"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(mainFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.OAuth.Enabled {
		t.Fatal("expected oauth disabled by default")
	}
	want := []string{"*"}
	if len(cfg.OAuth.Scopes) != len(want) {
		t.Fatalf("expected default scopes %v, got %v", want, cfg.OAuth.Scopes)
	}
	for i := range want {
		if cfg.OAuth.Scopes[i] != want[i] {
			t.Fatalf("expected default scopes %v, got %v", want, cfg.OAuth.Scopes)
		}
	}
}

func TestLoad_OAuthBlockParses(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(mainFile, []byte(`
[database]
dsn = "postgres://example"

[solarNetwork]
baseUrl = "https://solar.example"

[oauth]
enabled = true
clientId = "personality"
clientSecret = "secret"
scopes = ["openid", "profile", "files"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(mainFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.OAuth.Enabled {
		t.Fatal("expected oauth enabled")
	}
	if cfg.OAuth.ClientID != "personality" {
		t.Fatalf("expected clientId to load, got %q", cfg.OAuth.ClientID)
	}
	if cfg.OAuth.ClientSecret != "secret" {
		t.Fatalf("expected clientSecret to load, got %q", cfg.OAuth.ClientSecret)
	}
	if len(cfg.OAuth.Scopes) != 3 || cfg.OAuth.Scopes[2] != "files" {
		t.Fatalf("unexpected scopes %v", cfg.OAuth.Scopes)
	}
}

func TestLoad_OAuthEnabledRequiresClientID(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(mainFile, []byte(`
[database]
dsn = "postgres://example"

[solarNetwork]
baseUrl = "https://solar.example"

[oauth]
enabled = true
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(mainFile)
	if err == nil {
		t.Fatal("expected oauth validation error when enabled without clientId")
	}
}

func TestLoad_OAuthEnabledRequiresBaseURL(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(mainFile, []byte(`
[database]
dsn = "postgres://example"

[oauth]
enabled = true
clientId = "personality"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(mainFile)
	if err == nil {
		t.Fatal("expected oauth validation error when enabled without solarNetwork.baseUrl")
	}
}

func TestLoad_WebSearchParsesEnginesAndDefaults(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(mainFile, []byte(`
[database]
dsn = "postgres://example"

[webSearch]
enabled = true
language = "en"

[[webSearch.engines]]
type = "bing"
region = "en-GB"
timeout = "5s"

[[webSearch.engines]]
id = "ddg"
type = "duckduckgo"
enabled = false
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(mainFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.WebSearch.Enabled || cfg.WebSearch.Language != "en" {
		t.Fatalf("unexpected webSearch config: %#v", cfg.WebSearch)
	}
	if cfg.WebSearch.DefaultLimit != 5 || cfg.WebSearch.MaxLimit != 10 {
		t.Fatalf("unexpected limits: %#v", cfg.WebSearch)
	}
	if cfg.WebSearch.Timeout != 15*time.Second || cfg.WebSearch.CacheTTL != 5*time.Minute {
		t.Fatalf("unexpected durations: %#v", cfg.WebSearch)
	}
	// The explicitly disabled engine is dropped; the remaining one gets an id.
	if len(cfg.WebSearch.Engines) != 1 {
		t.Fatalf("got %d engines, want 1: %#v", len(cfg.WebSearch.Engines), cfg.WebSearch.Engines)
	}
	engine := cfg.WebSearch.Engines[0]
	if engine.ID != "bing" || engine.Type != "bing" || engine.Region != "en-GB" || engine.Timeout != 5*time.Second {
		t.Fatalf("unexpected engine: %#v", engine)
	}
}

func TestLoad_WebSearchDefaultsToDirectEngines(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(mainFile, []byte(`
[database]
dsn = "postgres://example"

[webSearch]
enabled = true
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(mainFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	types := make([]string, 0, len(cfg.WebSearch.Engines))
	for _, engine := range cfg.WebSearch.Engines {
		types = append(types, engine.Type)
	}
	if len(types) != 1 || types[0] != "duckduckgo" {
		t.Fatalf("default engines = %#v, want duckduckgo only", types)
	}
	if !cfg.WebSearch.Crawl.Enabled {
		t.Fatal("crawling must be on by default so discovered pages can be indexed")
	}
	if cfg.WebSearch.Crawl.MaxPagesPerQuery != 3 || cfg.WebSearch.Crawl.PageTimeout != 8*time.Second {
		t.Fatalf("unexpected crawl defaults: %#v", cfg.WebSearch.Crawl)
	}
}

func TestLoad_WebSearchAbilityRequiresEnabledConfig(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(mainFile, []byte(`
[database]
dsn = "postgres://example"

[[agents.items]]
id = "researcher"
name = "Researcher"
model = "openai/gpt-4.1-mini"
abilities = ["web_search"]
enabled = true
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(mainFile)
	if err == nil {
		t.Fatal("expected a validation error when an agent declares web_search without webSearch.enabled")
	}
}

func TestLoad_WebSearchValidationRules(t *testing.T) {
	cases := map[string]string{
		"unknown engine type": `
[webSearch]
enabled = true

[[webSearch.engines]]
type = "altavista"
`,
		"duplicate engine id": `
[webSearch]
enabled = true

[[webSearch.engines]]
id = "google-mirror"
type = "google"

[[webSearch.engines]]
id = "google-mirror"
type = "bing"
`,
		"every engine disabled": `
[webSearch]
enabled = true

[[webSearch.engines]]
type = "bing"
enabled = false
`,
	}

	for name, block := range cases {
		dir := t.TempDir()
		mainFile := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(mainFile, []byte("[database]\ndsn = \"postgres://example\"\n"+block), 0o644); err != nil {
			t.Fatal(err)
		}

		if _, err := Load(mainFile); err == nil {
			t.Fatalf("%s: expected a validation error", name)
		}
	}
}

func TestLoad_WebSearchNormalizesInconsistentValues(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(mainFile, []byte(`
[database]
dsn = "postgres://example"

[webSearch]
enabled = true
defaultLimit = 25
maxLimit = 3

[webSearch.crawl]
maxPagesPerQuery = 500

[[webSearch.engines]]
type = "searxng"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// An unsupported engine type must fail even though the limits are clamped.
	if _, err := Load(mainFile); err == nil {
		t.Fatal("expected an unsupported engine type error")
	}

	block := strings.ReplaceAll(string(mustRead(t, mainFile)), `type = "searxng"`, `type = "bing"`)
	if err := os.WriteFile(mainFile, []byte(block), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(mainFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.WebSearch.DefaultLimit != 25 || cfg.WebSearch.MaxLimit != 25 {
		t.Fatalf("maxLimit must not fall below defaultLimit: %#v", cfg.WebSearch)
	}
	if cfg.WebSearch.Crawl.MaxPagesPerQuery != 10 {
		t.Fatalf("maxPagesPerQuery = %d, want the 10 page cap", cfg.WebSearch.Crawl.MaxPagesPerQuery)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}
