package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	tea "github.com/charmbracelet/bubbletea"

	"src.solsynth.dev/sosys/personality/internal/tui"
)

func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:8090", "PersonalityCore HTTP base URL")
	accountID := flag.String("account-id", "", "optional X-Account-Id header for dev mode")
	agentID := flag.String("agent-id", "", "preferred agent ID")
	stream := flag.Bool("stream", true, "use SSE streaming runs")
	autonomousSecret := flag.String("autonomous-secret", "", "secret for internal autonomous conversation endpoint")
	startUserID := flag.String("start-user-id", "", "target Solar account ID for autonomous conversation start")
	startUser := flag.String("start-user", "", "start an autonomous conversation with this Solar account name and exit")
	startPrompt := flag.String("start-prompt", "", "prompt for the autonomous conversation start request")
	flag.Parse()

	client, err := tui.NewClient(*baseURL, *accountID, *autonomousSecret)
	if err != nil {
		log.Fatalf("create client: %v", err)
	}
	if *startUserID != "" || *startUser != "" {
		if *agentID == "" {
			log.Fatal("-agent-id is required when using -start-user-id or -start-user")
		}
		if *startUserID == "" && *startUser == "" {
			log.Fatal("-start-user-id or -start-user is required")
		}
		result, err := client.StartConversationWithUser(context.Background(), *agentID, tui.StartConversationInput{
			TargetAccountID:   *startUserID,
			TargetAccountName: *startUser,
			Prompt:            *startPrompt,
		})
		if err != nil {
			log.Fatalf("start conversation: %v", err)
		}
		fmt.Printf("conversation_id=%s\nrun_id=%s\nmessage_id=%s\ncontent=%s\n", result.Thread.ID, result.Run.ID, result.ResponseMessage.ID, result.Content)
		return
	}

	model := tui.NewModel(client, *baseURL, *accountID, *agentID, *stream)
	program := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		log.Fatalf("run tui: %v", err)
	}
}
