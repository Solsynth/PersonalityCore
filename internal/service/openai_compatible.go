package service

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"

	"src.solsynth.dev/sosys/persona/internal/agent"
)

// OpenAICompletionInput is intentionally stateless: no conversation, message,
// or run records are created while processing it. Server tools may still carry
// out their own domain action (for example, creating a scheduled task).
type OpenAICompletionInput struct {
	AgentID            string
	AccountID          string
	CredentialID       string
	Model              string
	Messages           []*schema.Message
	ClientTools        []*schema.ToolInfo
	IncludeServerTools bool
	// Overrides names the server-owned tools the caller runs itself. They
	// leave the server's list before the collision check, which is what lets
	// a caller name its replacement exactly as the server named the original:
	// one name, one owner, and the owner is the caller.
	Overrides []string
	// AccountName and AccountNick carry the authenticated caller's identity
	// from the request context so the model can address the user.
	AccountName string
	AccountNick string
	// BillingUsageID and BillingRunID are set by callers that already reserved
	// a billing ledger row, such as the Responses API.
	BillingUsageID string
	BillingRunID   string
}

type OpenAICompletionResult struct {
	Message    *schema.Message
	Model      string
	Definition agent.Definition
	Usage      *schema.TokenUsage
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
	if err := s.AuthorizeOpenAICredential(ctx, input.CredentialID, agentID, def.Model, def); err != nil {
		return nil, err
	}

	billingUsageID := strings.TrimSpace(input.BillingUsageID)
	ownsBilling := billingUsageID == ""
	billingRunID := strings.TrimSpace(input.BillingRunID)
	if billingRunID == "" {
		billingRunID = newID()
	}
	if ownsBilling && s.billing != nil {
		billingUsageID, err = s.billing.AuthorizeRun(ctx, input.AccountID, def)
		if err != nil {
			return nil, err
		}
	}
	generationCompleted := false
	defer func() {
		if ownsBilling && !generationCompleted && s.billing != nil {
			s.billing.CancelAuthorization(ctx, billingUsageID)
		}
	}()
	finish := func(response *schema.Message) (*OpenAICompletionResult, error) {
		generationCompleted = true
		if ownsBilling && s.billing != nil {
			if err := s.billing.RecordUsage(ctx, billingUsageID, billingRunID, def, response.ResponseMeta.Usage); err != nil {
				return nil, err
			}
		}
		return &OpenAICompletionResult{Message: response, Model: def.Model, Definition: def, Usage: response.ResponseMeta.Usage}, nil
	}

	messages := append([]*schema.Message(nil), input.Messages...)
	if strings.TrimSpace(def.SystemPrompt) != "" {
		messages = append([]*schema.Message{schema.SystemMessage(def.SystemPrompt)}, messages...)
	}
	if overlay := renderUserIdentityOverlay(input.AccountName, input.AccountNick); overlay != "" {
		messages = append(messages, schema.SystemMessage(overlay))
	}
	activeSkills := map[string]bool{}
	serverTools := []*schema.ToolInfo(nil)
	if input.IncludeServerTools {
		serverTools = s.callerServerTools(def, activeSkills, callerOverrides(input.Overrides))
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
		return finish(response)
	}

	toolModel, err := s.executor.NewToolCallingModel(ctx, def, tools)
	if err != nil {
		return nil, err
	}
	serverNames := toolNames(serverTools)
	for {
		response, err := toolModel.Generate(ctx, messages, model.WithToolChoice(schema.ToolChoiceAllowed))
		if err != nil {
			return nil, fmt.Errorf("generation failed: %w", err)
		}
		if len(response.ToolCalls) == 0 {
			return finish(response)
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
			return finish(response)
		}
		messages = append(messages, response)
		for _, call := range serverCalls {
			result, err := s.executeOpenAIServerTool(ctx, def, input.AccountID, call, activeSkills)
			if err != nil {
				return nil, err
			}
			messages = append(messages, schema.ToolMessage(result.Content, call.ID, schema.WithToolName(call.Function.Name)))
			if call.Function.Name == "activate_skill" {
				serverTools = s.callerServerTools(def, activeSkills, callerOverrides(input.Overrides))
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

// callerServerTools is the server's own tool list with the tools this caller
// has taken over removed.
//
// Every place on this path builds the server's tools and then checks them
// against the caller's, so the filter belongs here rather than at each call
// site: a collision check that ran against an unfiltered list would reject the
// caller's replacement for a tool it had already been promised.
func (s *ConversationService) callerServerTools(def agent.Definition, activeSkills map[string]bool, overrides map[string]bool) []*schema.ToolInfo {
	if len(overrides) == 0 {
		return s.buildToolInfos(def, activeSkills, 0)
	}
	return s.applyCallerOverrides(s.buildToolInfos(def, activeSkills, 0), overrides)
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
		// No caller catalogue on the stateless path: there is no conversation
		// to remember an activation in, and no channel to ask the caller to
		// load one, so only the server's own skills are advertised.
		return s.executeListSkillsToolCall(def, activeSkills, 0, nil, nil), nil
	case "activate_skill":
		result, _ := s.executeActivateSkillToolCall(call, activeSkills, def, nil)
		return result, nil
	case memorySearchToolName, memorySaveToolName, memoryForgetToolName:
		return s.executeMemoryToolCall(ctx, def, accountID, call)
	}
	if call.Function.Name == getCurrentUserProfileToolName {
		return s.executeGetCurrentUserProfileToolCall(ctx, def.ID, nil, call)
	}
	if isTaskToolName(call.Function.Name) {
		return s.executeTaskToolCall(ctx, def.ID, accountID, call)
	}
	if isPetToolName(call.Function.Name) {
		return s.executePetToolCall(ctx, accountID, def.ID, call)
	}
	if isWebSearchToolName(call.Function.Name) {
		return s.executeWebSearchToolCall(ctx, accountID, call)
	}
	if isUserScopedToolName(call.Function.Name) {
		return s.executeUserScopedToolCall(ctx, call)
	}
	return s.executeChatToolCall(ctx, def.ID, call)
}


// OpenAIStreamCallbacks forwards the streamed parts of one completion.
type OpenAIStreamCallbacks struct {
	OnContent   func(string) error
	OnReasoning func(string) error
}

// StreamOpenAICompletion is the streaming variant of CompleteOpenAI. Content
// and reasoning deltas are forwarded as they arrive, so a plain turn streams
// live; server-owned tool calls are executed in-process exactly like the
// non-streaming path; and a round that calls a client-owned tool returns those
// calls to the caller, who executes them and continues with a new request.
func (s *ConversationService) StreamOpenAICompletion(ctx context.Context, input OpenAICompletionInput, callbacks OpenAIStreamCallbacks) (*OpenAICompletionResult, error) {
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
	if err := s.AuthorizeOpenAICredential(ctx, input.CredentialID, agentID, def.Model, def); err != nil {
		return nil, err
	}

	billingUsageID := strings.TrimSpace(input.BillingUsageID)
	ownsBilling := billingUsageID == ""
	billingRunID := strings.TrimSpace(input.BillingRunID)
	if billingRunID == "" {
		billingRunID = newID()
	}
	if ownsBilling && s.billing != nil {
		billingUsageID, err = s.billing.AuthorizeRun(ctx, input.AccountID, def)
		if err != nil {
			return nil, err
		}
	}
	generationCompleted := false
	defer func() {
		if ownsBilling && !generationCompleted && s.billing != nil {
			s.billing.CancelAuthorization(ctx, billingUsageID)
		}
	}()
	finish := func(response *schema.Message) (*OpenAICompletionResult, error) {
		generationCompleted = true
		if ownsBilling && s.billing != nil {
			if err := s.billing.RecordUsage(ctx, billingUsageID, billingRunID, def, response.ResponseMeta.Usage); err != nil {
				return nil, err
			}
		}
		return &OpenAICompletionResult{Message: response, Model: def.Model, Definition: def, Usage: response.ResponseMeta.Usage}, nil
	}

	messages := append([]*schema.Message(nil), input.Messages...)
	if strings.TrimSpace(def.SystemPrompt) != "" {
		messages = append([]*schema.Message{schema.SystemMessage(def.SystemPrompt)}, messages...)
	}
	if overlay := renderUserIdentityOverlay(input.AccountName, input.AccountNick); overlay != "" {
		messages = append(messages, schema.SystemMessage(overlay))
	}
	activeSkills := map[string]bool{}
	serverTools := []*schema.ToolInfo(nil)
	if input.IncludeServerTools {
		serverTools = s.callerServerTools(def, activeSkills, callerOverrides(input.Overrides))
	}
	if err := rejectToolNameCollisions(serverTools, input.ClientTools); err != nil {
		return nil, err
	}
	tools := append(append([]*schema.ToolInfo(nil), serverTools...), input.ClientTools...)
	if len(tools) == 0 {
		stream, err := s.executor.Stream(ctx, agent.RunRequest{Agent: def, Messages: messages})
		if err != nil {
			return nil, fmt.Errorf("generation failed: %w", err)
		}
		defer stream.Close()
		var content, reasoning strings.Builder
		var usage *schema.TokenUsage
		for {
			chunk, recvErr := stream.Recv()
			if recvErr != nil {
				if recvErr == io.EOF {
					break
				}
				return nil, fmt.Errorf("generation failed: %w", recvErr)
			}
			if chunk == nil {
				continue
			}
			if chunk.ResponseMeta != nil && chunk.ResponseMeta.Usage != nil {
				usage = chunk.ResponseMeta.Usage
			}
			if chunk.Content != "" {
				content.WriteString(chunk.Content)
				if callbacks.OnContent != nil {
					if err := callbacks.OnContent(chunk.Content); err != nil {
						return nil, err
					}
				}
			}
			if chunk.ReasoningContent != "" {
				reasoning.WriteString(chunk.ReasoningContent)
				if callbacks.OnReasoning != nil {
					if err := callbacks.OnReasoning(chunk.ReasoningContent); err != nil {
						return nil, err
					}
				}
			}
		}
		return finish(&schema.Message{
			Role:             schema.Assistant,
			Content:          strings.TrimSpace(content.String()),
			ReasoningContent: strings.TrimSpace(reasoning.String()),
			ResponseMeta:     &schema.ResponseMeta{Usage: usage},
		})
	}

	toolModel, err := s.executor.NewToolCallingModel(ctx, def, tools)
	if err != nil {
		return nil, err
	}
	serverNames := toolNames(serverTools)
	for {
		var round *streamedToolRound
		var roundErr error
		for attempt := 0; attempt < 2; attempt++ {
			genOpts := []model.Option{model.WithToolChoice(schema.ToolChoiceAllowed)}
			if attempt > 0 {
				genOpts = append(genOpts, einoopenai.WithExtraFields(map[string]any{"thinking": map[string]any{"type": "disabled"}}))
			}
			round, roundErr = s.streamToolRound(ctx, toolModel, messages, genOpts, StreamCallbacks{
				OnChunk:      callbacks.OnContent,
				OnReasoning:  callbacks.OnReasoning,
			})
			if roundErr == nil {
				break
			}
			if attempt == 0 && (strings.Contains(roundErr.Error(), "thinking mode") || strings.Contains(roundErr.Error(), "tool_choice")) {
				continue
			}
			break
		}
		if roundErr != nil {
			return nil, fmt.Errorf("generation failed: %w", roundErr)
		}
		if len(round.calls) == 0 {
			return finish(&schema.Message{
				Role:             schema.Assistant,
				Content:          round.content,
				ReasoningContent: round.reasoning,
				ResponseMeta:     &schema.ResponseMeta{Usage: round.usage},
			})
		}

		clientCalls := make([]schema.ToolCall, 0, len(round.calls))
		serverCalls := make([]schema.ToolCall, 0, len(round.calls))
		for _, call := range round.calls {
			if serverNames[call.Function.Name] {
				serverCalls = append(serverCalls, call)
			} else {
				clientCalls = append(clientCalls, call)
			}
		}
		if len(clientCalls) > 0 {
			for _, call := range serverCalls {
				if _, err := s.executeOpenAIServerTool(ctx, def, input.AccountID, call, activeSkills); err != nil {
					return nil, err
				}
			}
			return finish(&schema.Message{
				Role:             schema.Assistant,
				Content:          round.content,
				ReasoningContent: round.reasoning,
				ToolCalls:        clientCalls,
				ResponseMeta:     &schema.ResponseMeta{Usage: round.usage},
			})
		}
		messages = append(messages, &schema.Message{Role: schema.Assistant, Content: round.content, ToolCalls: round.calls, ReasoningContent: round.reasoning})
		for _, call := range serverCalls {
			result, err := s.executeOpenAIServerTool(ctx, def, input.AccountID, call, activeSkills)
			if err != nil {
				return nil, err
			}
			messages = append(messages, schema.ToolMessage(result.Content, call.ID, schema.WithToolName(call.Function.Name)))
			if call.Function.Name == "activate_skill" {
				serverTools = s.callerServerTools(def, activeSkills, callerOverrides(input.Overrides))
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
}
