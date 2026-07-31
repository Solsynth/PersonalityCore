package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"src.solsynth.dev/sosys/personality/internal/agent"
)

// OpenAICompletionInput is intentionally stateless: no conversation, message,
// or run records are created while processing it. Server tools may still carry
// out their own domain action (for example, creating a scheduled task).
type OpenAICompletionInput struct {
	AgentID            string
	AccountID          string
	Model              string
	Messages           []*schema.Message
	ClientTools        []*schema.ToolInfo
	IncludeServerTools bool
}

type OpenAICompletionResult struct {
	Message *schema.Message
	Model   string
}

func (s *ConversationService) CompleteOpenAI(ctx context.Context, input OpenAICompletionInput) (*OpenAICompletionResult, error) {
	if s.billing != nil {
		if err := s.billing.CheckAccess(ctx, input.AccountID); err != nil {
			return nil, err
		}
	}
	agentID, modelOverride, err := resolveOpenAIAgentModel(input.AgentID, input.Model)
	if err != nil {
		return nil, err
	}
	var def agent.Definition
	if strings.EqualFold(agentID, "raw") {
		if modelOverride == "" {
			return nil, fmt.Errorf("raw requires model raw/provider/model")
		}
		// raw is intentionally not a registry agent: it is a transparent model
		// proxy with neither a system prompt nor server-owned tools.
		def = agent.Definition{ID: "raw", Name: "raw", Model: modelOverride, Enabled: true}
		input.IncludeServerTools = false
	} else {
		var ok bool
		def, ok = s.registry.Get(agentID)
		if !ok {
			return nil, fmt.Errorf("agent %q is unavailable", agentID)
		}
		if modelOverride != "" {
			def.Model = modelOverride
		}
	}
	if len(input.Messages) == 0 {
		return nil, fmt.Errorf("messages is required")
	}

	messages := append([]*schema.Message(nil), input.Messages...)
	if strings.TrimSpace(def.SystemPrompt) != "" {
		messages = append([]*schema.Message{schema.SystemMessage(def.SystemPrompt)}, messages...)
	}
	activeSkills := map[string]bool{}
	serverTools := []*schema.ToolInfo(nil)
	if input.IncludeServerTools {
		serverTools = s.buildToolInfos(def, activeSkills, 0)
	}
	if err := rejectToolNameCollisions(serverTools, input.ClientTools); err != nil {
		return nil, err
	}
	tools := append(append([]*schema.ToolInfo(nil), serverTools...), input.ClientTools...)
	if len(tools) == 0 {
		response, err := s.executor.Generate(ctx, agent.RunRequest{Agent: def, Messages: messages})
		if err != nil {
			return nil, fmt.Errorf("generation failed: %w", err)
		}
		return &OpenAICompletionResult{Message: response, Model: def.Model}, nil
	}

	toolModel, err := s.executor.NewToolCallingModel(ctx, def, tools)
	if err != nil {
		return nil, err
	}
	serverNames := toolNames(serverTools)
	for step := 0; step < 6; step++ {
		response, err := toolModel.Generate(ctx, messages, model.WithToolChoice(schema.ToolChoiceAllowed))
		if err != nil {
			return nil, fmt.Errorf("generation failed: %w", err)
		}
		if len(response.ToolCalls) == 0 {
			return &OpenAICompletionResult{Message: response, Model: def.Model}, nil
		}

		clientCalls := make([]schema.ToolCall, 0, len(response.ToolCalls))
		serverCalls := make([]schema.ToolCall, 0, len(response.ToolCalls))
		for _, call := range response.ToolCalls {
			if serverNames[call.Function.Name] {
				serverCalls = append(serverCalls, call)
			} else {
				clientCalls = append(clientCalls, call)
			}
		}
		// A client tool call is a protocol handoff. Do not run it on the server;
		// the client returns its tool result in a later stateless request.
		if len(clientCalls) > 0 {
			// Providers may return a mixed batch. Execute the server-owned calls
			// before handing the client-owned subset back. Their results cannot be
			// retained in this deliberately stateless API, so callers that need a
			// model-visible server result should issue server and client calls in
			// separate turns.
			for _, call := range serverCalls {
				if _, err := s.executeOpenAIServerTool(ctx, def, input.AccountID, call, activeSkills); err != nil {
					return nil, err
				}
			}
			response.ToolCalls = clientCalls
			return &OpenAICompletionResult{Message: response, Model: def.Model}, nil
		}
		messages = append(messages, response)
		for _, call := range serverCalls {
			result, err := s.executeOpenAIServerTool(ctx, def, input.AccountID, call, activeSkills)
			if err != nil {
				return nil, err
			}
			messages = append(messages, schema.ToolMessage(result.Content, call.ID, schema.WithToolName(call.Function.Name)))
			if call.Function.Name == "activate_skill" {
				serverTools = s.buildToolInfos(def, activeSkills, 0)
				if err := rejectToolNameCollisions(serverTools, input.ClientTools); err != nil {
					return nil, err
				}
				tools = append(append([]*schema.ToolInfo(nil), serverTools...), input.ClientTools...)
				toolModel, err = s.executor.NewToolCallingModel(ctx, def, tools)
				if err != nil {
					return nil, err
				}
				serverNames = toolNames(serverTools)
			}
		}
	}
	return nil, fmt.Errorf("server tool loop exceeded maximum iterations")
}

// resolveOpenAIAgentModel lets standard OpenAI clients select an agent through
// model while still allowing an agent to expose multiple provider models.
// Accepted forms are "agent" and "agent/provider/model". agent_id is retained
// as a compatibility extension, in which case model is "provider/model".
func resolveOpenAIAgentModel(rawAgentID, rawModel string) (agentID, modelOverride string, err error) {
	agentID = strings.TrimSpace(rawAgentID)
	model := strings.TrimSpace(rawModel)
	if agentID != "" {
		if model == "" {
			return agentID, "", nil
		}
		parts := strings.SplitN(model, "/", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return "", "", fmt.Errorf("model must use provider/model when agent_id is provided")
		}
		return agentID, model, nil
	}
	if model == "" {
		return "", "", fmt.Errorf("model is required and must name an agent")
	}
	parts := strings.SplitN(model, "/", 3)
	if len(parts) == 1 {
		return parts[0], "", nil
	}
	if len(parts) != 3 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" || strings.TrimSpace(parts[2]) == "" {
		return "", "", fmt.Errorf("invalid model %q, expected agent or agent/provider/model", model)
	}
	return parts[0], parts[1] + "/" + parts[2], nil
}

func rejectToolNameCollisions(server, client []*schema.ToolInfo) error {
	names := toolNames(server)
	for _, tool := range client {
		if tool != nil && names[tool.Name] {
			return fmt.Errorf("client tool %q conflicts with a server tool", tool.Name)
		}
	}
	return nil
}

func toolNames(tools []*schema.ToolInfo) map[string]bool {
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if tool != nil {
			names[tool.Name] = true
		}
	}
	return names
}

func (s *ConversationService) executeOpenAIServerTool(ctx context.Context, def agent.Definition, accountID string, call schema.ToolCall, activeSkills map[string]bool) (*executedChatToolResult, error) {
	switch call.Function.Name {
	case "list_skills":
		return s.executeListSkillsToolCall(def, activeSkills, 0), nil
	case "activate_skill":
		return s.executeActivateSkillToolCall(call, activeSkills), nil
	}
	if isTaskToolName(call.Function.Name) {
		return s.executeTaskToolCall(ctx, def.ID, accountID, call)
	}
	if isSurfingToolName(call.Function.Name) {
		return s.executeSurfingToolCall(ctx, def.ID, accountID, call)
	}
	return s.executeChatToolCall(ctx, def.ID, call)
}
