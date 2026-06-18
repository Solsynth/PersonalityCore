package config

import (
	"os"
	"path/filepath"
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
id = "azure"
type = "openai"
apiKey = "azure-key"
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
