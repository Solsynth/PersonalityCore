package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_MergesAgentDirectory(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "config.toml")
	agentDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(mainFile, []byte(`
[database]
dsn = "postgres://example"

[agents]
dir = "`+agentDir+`"

[[agents.items]]
id = "inline"
name = "Inline"
systemPrompt = "inline"
enabled = true
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

	cfg, err := Load(mainFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Agents.Items) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(cfg.Agents.Items))
	}
}
