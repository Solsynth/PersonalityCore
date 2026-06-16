package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	userStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	agentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
)

type Model struct {
	client         *Client
	baseURL        string
	accountID      string
	preferredAgent string
	stream         bool

	agents           []Agent
	agentIndex       int
	conversation     *Conversation
	messages         []Message
	input            textinput.Model
	viewport         viewport.Model
	width            int
	height           int
	status           string
	err              string
	streaming        bool
	pendingAssistant  string
	pendingReasoning  string
	pendingToolCalls  []string
	pendingUserInput  string
	streamEvents      chan tea.Msg
}

type loadedMsg struct {
	agents       []Agent
	conversation *Conversation
	messages     []Message
	err          error
}

type conversationReadyMsg struct {
	conversation *Conversation
	messages     []Message
	err          error
}

type streamEventMsg struct {
	event SSEEvent
	err   error
	done  bool
}

type toolCallDelta struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type runResultMsg struct {
	result *RunResult
	err    error
}

func NewModel(client *Client, baseURL, accountID, agentID string, stream bool) Model {
	input := textinput.New()
	input.Placeholder = "Type a message. Enter to send. Ctrl+N new chat. Tab switch agent."
	input.Focus()
	input.Prompt = "> "

	vp := viewport.New(0, 0)
	vp.SetContent("Loading agents...")

	return Model{
		client:         client,
		baseURL:        baseURL,
		accountID:      accountID,
		preferredAgent: strings.TrimSpace(agentID),
		stream:         stream,
		input:          input,
		viewport:       vp,
		status:         "loading",
	}
}

func (m Model) Init() tea.Cmd {
	return m.loadInitial()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = max(20, msg.Width-2)
		m.viewport.Height = max(5, msg.Height-4)
		m.refreshTranscript()
		return m, nil
	case tea.KeyMsg:
		if m.streaming {
			switch msg.String() {
			case "ctrl+c", "q":
				return m, tea.Quit
			}
			return m, nil
		}

		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "ctrl+n":
			return m, m.startConversation()
		case "tab":
			if len(m.agents) == 0 {
				return m, nil
			}
			m.agentIndex = (m.agentIndex + 1) % len(m.agents)
			m.status = fmt.Sprintf("agent selected: %s", m.currentAgent().Name)
			m.err = ""
			return m, m.startConversation()
		case "shift+tab":
			if len(m.agents) == 0 {
				return m, nil
			}
			m.agentIndex--
			if m.agentIndex < 0 {
				m.agentIndex = len(m.agents) - 1
			}
			m.status = fmt.Sprintf("agent selected: %s", m.currentAgent().Name)
			m.err = ""
			return m, m.startConversation()
		case "ctrl+s":
			m.stream = !m.stream
			mode := "streaming"
			if !m.stream {
				mode = "non-streaming"
			}
			m.status = fmt.Sprintf("run mode: %s", mode)
			return m, nil
		case "enter":
			if m.conversation == nil {
				m.err = "conversation is not ready"
				return m, nil
			}
			content := strings.TrimSpace(m.input.Value())
			if content == "" {
				return m, nil
			}
			m.pendingUserInput = content
			m.input.Reset()
			m.streaming = m.stream
			m.err = ""
			if m.stream {
				m.status = "streaming reply..."
			} else {
				m.status = "waiting for reply..."
			}
			m.messages = append(m.messages, Message{Role: "user", Content: content})
			m.pendingAssistant = ""
			m.refreshTranscript()
			if m.stream {
				m.streamEvents = make(chan tea.Msg, 32)
				return m, tea.Batch(
					startStreamRun(m.client, m.conversation.ID, content, m.streamEvents),
					waitForStreamEvent(m.streamEvents),
				)
			}
			return m, m.sendRun(content)
		}

		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	case loadedMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			m.status = "failed to load"
			m.refreshTranscript()
			return m, nil
		}
		m.agents = msg.agents
		m.conversation = msg.conversation
		m.messages = msg.messages
		m.agentIndex = m.resolveAgentIndex()
		if m.conversation != nil {
			m.status = fmt.Sprintf("conversation ready: %s", m.conversation.ID)
		} else {
			m.status = "ready"
		}
		m.refreshTranscript()
		return m, nil
	case conversationReadyMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			m.status = "failed to create conversation"
			return m, nil
		}
		m.conversation = msg.conversation
		m.messages = msg.messages
		m.pendingAssistant = ""
		if msg.conversation != nil {
			m.status = fmt.Sprintf("new conversation: %s", msg.conversation.ID)
		}
		m.err = ""
		m.refreshTranscript()
		return m, nil
	case streamEventMsg:
		if msg.err != nil {
			m.streaming = false
			m.pendingAssistant = ""
			m.err = msg.err.Error()
			m.status = "run failed"
			m.refreshTranscript()
			return m, nil
		}
		if len(msg.event.Data) > 0 {
			switch msg.event.Event {
			case "message.delta":
				var payload struct {
					Delta string `json:"delta"`
				}
				if err := jsonUnmarshal(msg.event.Data, &payload); err == nil {
					m.pendingAssistant += payload.Delta
					m.refreshTranscript()
				}
			case "reasoning.delta":
				var payload struct {
					Delta string `json:"delta"`
				}
				if err := jsonUnmarshal(msg.event.Data, &payload); err == nil {
					m.pendingReasoning += payload.Delta
					m.refreshTranscript()
				}
			case "tool_call.delta":
				var call toolCallDelta
				if err := jsonUnmarshal(msg.event.Data, &call); err == nil {
					label := call.Name
					if label == "" {
						label = "tool_call"
					}
					m.pendingToolCalls = append(m.pendingToolCalls, label)
					m.refreshTranscript()
				}
			case "message.completed":
				var payload struct {
					Content string `json:"content"`
				}
				if err := jsonUnmarshal(msg.event.Data, &payload); err == nil {
					m.pendingAssistant = ""
					m.pendingReasoning = ""
					m.pendingToolCalls = nil
					m.messages = append(m.messages, Message{Role: "assistant", Content: payload.Content})
					m.refreshTranscript()
				}
			}
		}
		if msg.done {
			m.streaming = false
			m.status = "run completed"
			m.streamEvents = nil
			return m, nil
		}
		return m, waitForStreamEvent(m.streamEvents)
	case runResultMsg:
		if msg.err != nil {
			m.streaming = false
			m.err = msg.err.Error()
			m.status = "run failed"
			m.refreshTranscript()
			return m, nil
		}
		if m.streamEvents != nil {
			return m, waitForStreamEvent(m.streamEvents)
		}
		m.streaming = false
		if !m.stream && msg.result != nil {
			m.messages = append(m.messages, Message{Role: "assistant", Content: msg.result.Content})
			m.status = "run completed"
			m.err = ""
			m.refreshTranscript()
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) View() string {
	header := fmt.Sprintf(
		"PersonalityCore TUI  agent=%s  conversation=%s  mode=%s  base=%s",
		m.currentAgentLabel(),
		m.conversationLabel(),
		map[bool]string{true: "stream", false: "json"}[m.stream],
		m.baseURL,
	)

	footer := "Enter send  Ctrl+N new  Tab next agent  Shift+Tab prev  Ctrl+S toggle stream  Q quit"
	status := statusStyle.Render(m.status)
	if m.err != "" {
		status = errorStyle.Render(m.err)
	}

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		m.viewport.View(),
		m.input.View(),
		status,
		statusStyle.Render(footer),
	)
}

func (m Model) loadInitial() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		agents, err := m.client.ListAgents(ctx)
		if err != nil {
			return loadedMsg{err: err}
		}
		if len(agents) == 0 {
			return loadedMsg{err: fmt.Errorf("no enabled agents available")}
		}

		index := 0
		if m.preferredAgent != "" {
			for i := range agents {
				if agents[i].ID == m.preferredAgent {
					index = i
					break
				}
			}
		}
		conv, err := m.client.CreateConversation(ctx, agents[index].ID, "")
		if err != nil {
			return loadedMsg{err: err}
		}
		messages, err := m.client.ListMessages(ctx, conv.ID)
		if err != nil {
			return loadedMsg{err: err}
		}
		return loadedMsg{agents: agents, conversation: conv, messages: messages}
	}
}

func (m Model) startConversation() tea.Cmd {
	agent := m.currentAgent()
	return func() tea.Msg {
		ctx := context.Background()
		conv, err := m.client.CreateConversation(ctx, agent.ID, "")
		if err != nil {
			return conversationReadyMsg{err: err}
		}
		messages, err := m.client.ListMessages(ctx, conv.ID)
		if err != nil {
			return conversationReadyMsg{err: err}
		}
		return conversationReadyMsg{conversation: conv, messages: messages}
	}
}

func (m Model) sendRun(content string) tea.Cmd {
	conversationID := m.conversation.ID
	return func() tea.Msg {
		ctx := context.Background()
		result, err := m.client.Run(ctx, conversationID, content, false, func(event SSEEvent) error {
			return nil
		})
		return runResultMsg{result: result, err: err}
	}
}

func (m *Model) currentAgent() Agent {
	if len(m.agents) == 0 {
		return Agent{}
	}
	if m.agentIndex < 0 || m.agentIndex >= len(m.agents) {
		m.agentIndex = 0
	}
	return m.agents[m.agentIndex]
}

func (m Model) currentAgentLabel() string {
	agent := m.currentAgent()
	if agent.ID == "" {
		return "-"
	}
	return agent.ID
}

func (m Model) conversationLabel() string {
	if m.conversation == nil {
		return "-"
	}
	return m.conversation.ID
}

func (m *Model) resolveAgentIndex() int {
	if len(m.agents) == 0 {
		return 0
	}
	if m.conversation != nil {
		for i := range m.agents {
			if m.agents[i].ID == m.conversation.AgentID {
				return i
			}
		}
	}
	if m.preferredAgent != "" {
		for i := range m.agents {
			if m.agents[i].ID == m.preferredAgent {
				return i
			}
		}
	}
	return 0
}

func (m *Model) refreshTranscript() {
	lines := make([]string, 0, len(m.messages)+4)
	if len(m.messages) == 0 {
		lines = append(lines, statusStyle.Render("No messages yet. Send one below."))
	}
	for _, message := range m.messages {
		switch message.Role {
		case "assistant":
			lines = append(lines, agentStyle.Render("assistant"))
		default:
			lines = append(lines, userStyle.Render("user"))
		}
		lines = append(lines, message.Content)
		lines = append(lines, "")
	}
	if m.pendingReasoning != "" || len(m.pendingToolCalls) > 0 || m.pendingAssistant != "" {
		lines = append(lines, agentStyle.Render("assistant"))
		if m.pendingReasoning != "" {
			lines = append(lines, statusStyle.Render("[thinking] "+m.pendingReasoning))
		}
		for _, tc := range m.pendingToolCalls {
			lines = append(lines, statusStyle.Render("[tool: "+tc+"]"))
		}
		if m.pendingAssistant != "" {
			lines = append(lines, m.pendingAssistant)
		}
		lines = append(lines, "")
	}
	m.viewport.SetContent(strings.Join(lines, "\n"))
	m.viewport.GotoBottom()
}

func jsonUnmarshal(data []byte, out any) error {
	return json.Unmarshal(data, out)
}

func startStreamRun(client *Client, conversationID, content string, events chan<- tea.Msg) tea.Cmd {
	return func() tea.Msg {
		go func() {
			ctx := context.Background()
			result, err := client.Run(ctx, conversationID, content, true, func(event SSEEvent) error {
				if event.Event == "heartbeat" || event.Event == "run.started" {
					return nil
				}
				events <- streamEventMsg{event: event}
				return nil
			})
			if err != nil {
				events <- streamEventMsg{err: err, done: true}
				close(events)
				return
			}
			events <- runResultMsg{result: result}
			events <- streamEventMsg{done: true}
			close(events)
		}()
		return nil
	}
}

func waitForStreamEvent(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		if events == nil {
			return nil
		}
		msg, ok := <-events
		if !ok {
			return streamEventMsg{done: true}
		}
		return msg
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
