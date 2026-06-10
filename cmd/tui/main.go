package main

import (
	"flag"
	"log"

	tea "github.com/charmbracelet/bubbletea"

	"src.solsynth.dev/sosys/personality/internal/tui"
)

func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:8090", "PersonalityCore HTTP base URL")
	accountID := flag.String("account-id", "", "optional X-Account-Id header for dev mode")
	agentID := flag.String("agent-id", "", "preferred agent ID")
	stream := flag.Bool("stream", true, "use SSE streaming runs")
	flag.Parse()

	client, err := tui.NewClient(*baseURL, *accountID)
	if err != nil {
		log.Fatalf("create client: %v", err)
	}

	model := tui.NewModel(client, *baseURL, *accountID, *agentID, *stream)
	program := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		log.Fatalf("run tui: %v", err)
	}
}
