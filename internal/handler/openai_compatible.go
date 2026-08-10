package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"

	"src.solsynth.dev/sosys/personality/internal/identity"
	"src.solsynth.dev/sosys/personality/internal/service"
)

// RegisterOpenAICompatibleRoutes exposes a stateless OpenAI Chat Completions
// subset. It deliberately lives outside the conversation API and never writes
// conversation_threads, conversation_messages, or conversation_runs.
func RegisterOpenAICompatibleRoutes(r gin.IRoutes, conversations *service.ConversationService) {
	r.POST("/v1/chat/completions", func(c *gin.Context) { openAIChatCompletion(c, conversations) })
}

type openAIChatCompletionRequest struct {
	AgentID     string          `json:"agent_id"`
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Tools       []openAITool    `json:"tools"`
	ServerTools bool            `json:"server_tools"`
	Stream      bool            `json:"stream"`
}

type openAIMessage struct {
	Role             string           `json:"role"`
	Content          json.RawMessage  `json:"content"`
	ReasoningContent string           `json:"reasoning_content"`
	ToolCallID       string           `json:"tool_call_id"`
	Name             string           `json:"name"`
	ToolCalls        []openAIToolCall `json:"tool_calls"`
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func openAIChatCompletion(c *gin.Context, conversations *service.ConversationService) {
	accountID, credentialID, ok := openAIRequestIdentity(c, conversations)
	if !ok {
		return
	}
	var request openAIChatCompletionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		openAIError(c, http.StatusBadRequest, err.Error())
		return
	}
	messages, err := parseOpenAIMessages(request.Messages)
	if err != nil {
		openAIError(c, http.StatusBadRequest, err.Error())
		return
	}
	tools, err := parseOpenAITools(request.Tools)
	if err != nil {
		openAIError(c, http.StatusBadRequest, err.Error())
		return
	}
	result, err := conversations.CompleteOpenAI(c.Request.Context(), service.OpenAICompletionInput{
		AgentID: request.AgentID, AccountID: accountID, CredentialID: credentialID, Model: request.Model, Messages: messages,
		ClientTools: tools, IncludeServerTools: request.ServerTools && credentialID == "",
	})
	if err != nil {
		openAIError(c, http.StatusBadRequest, err.Error())
		return
	}
	if credentialID != "" {
		if err := conversations.RecordOpenAICredentialUsage(c.Request.Context(), credentialID, result.Definition, result.Usage); err != nil {
			openAIError(c, http.StatusPaymentRequired, err.Error())
			return
		}
	}
	response := newOpenAIResponse(result.Model, result.Message)
	if request.Stream {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		writeOpenAIData(c, newOpenAIChunk(result.Model, result.Message))
		_, _ = fmt.Fprint(c.Writer, "data: [DONE]\n\n")
		c.Writer.Flush()
		return
	}
	c.JSON(http.StatusOK, response)
}

func openAIRequestIdentity(c *gin.Context, conversations *service.ConversationService) (string, string, bool) {
	auth := strings.TrimSpace(c.GetHeader("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		token := strings.TrimSpace(auth[len("bearer "):])
		if strings.HasPrefix(token, "sat_") {
			credential, err := conversations.AuthenticateOpenAICredential(c.Request.Context(), token)
			if err != nil {
				openAIError(c, http.StatusUnauthorized, err.Error())
				return "", "", false
			}
			return credential.AccountID, credential.CredentialID, true
		}
	}
	accountID, ok := identity.RequireAccountID(c)
	return accountID, "", ok
}

func parseOpenAIMessages(input []openAIMessage) ([]*schema.Message, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("messages is required")
	}
	messages := make([]*schema.Message, 0, len(input))
	for i, item := range input {
		content, err := openAIContentText(item.Content)
		if err != nil {
			return nil, fmt.Errorf("messages[%d].content: %w", i, err)
		}
		var role schema.RoleType
		switch strings.ToLower(strings.TrimSpace(item.Role)) {
		case "system", "developer":
			role = schema.System
		case "user":
			role = schema.User
		case "assistant":
			role = schema.Assistant
		case "tool":
			role = schema.Tool
		default:
			return nil, fmt.Errorf("messages[%d].role is unsupported", i)
		}
		msg := &schema.Message{Role: role, Content: content, ReasoningContent: item.ReasoningContent}
		if role == schema.Tool {
			msg.ToolCallID, msg.ToolName = item.ToolCallID, item.Name
			if msg.ToolCallID == "" {
				return nil, fmt.Errorf("messages[%d].tool_call_id is required for tool messages", i)
			}
		}
		for _, call := range item.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{ID: call.ID, Type: firstNonEmpty(call.Type, "function"), Function: schema.FunctionCall{Name: call.Function.Name, Arguments: call.Function.Arguments}})
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

func openAIContentText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("must be a string or text-part array")
	}
	var b strings.Builder
	for _, part := range parts {
		if part.Type == "text" || part.Type == "input_text" {
			b.WriteString(part.Text)
		}
	}
	return b.String(), nil
}

func parseOpenAITools(input []openAITool) ([]*schema.ToolInfo, error) {
	tools := make([]*schema.ToolInfo, 0, len(input))
	for i, item := range input {
		if item.Type != "" && item.Type != "function" {
			return nil, fmt.Errorf("tools[%d].type must be function", i)
		}
		name := strings.TrimSpace(item.Function.Name)
		if name == "" {
			return nil, fmt.Errorf("tools[%d].function.name is required", i)
		}
		params, err := openAIParameters(item.Function.Parameters)
		if err != nil {
			return nil, fmt.Errorf("tools[%d].function.parameters: %w", i, err)
		}
		tools = append(tools, &schema.ToolInfo{Name: name, Desc: item.Function.Description, ParamsOneOf: schema.NewParamsOneOfByParams(params)})
	}
	return tools, nil
}

func openAIParameters(raw map[string]any) (map[string]*schema.ParameterInfo, error) {
	if raw == nil {
		return map[string]*schema.ParameterInfo{}, nil
	}
	properties, _ := raw["properties"].(map[string]any)
	required := map[string]bool{}
	if values, ok := raw["required"].([]any); ok {
		for _, value := range values {
			if name, ok := value.(string); ok {
				required[name] = true
			}
		}
	}
	params := make(map[string]*schema.ParameterInfo, len(properties))
	for name, value := range properties {
		spec, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("property %q must be an object", name)
		}
		param, err := openAIParameter(spec)
		if err != nil {
			return nil, err
		}
		param.Required = required[name]
		params[name] = param
	}
	return params, nil
}

func openAIParameter(raw map[string]any) (*schema.ParameterInfo, error) {
	typeName, _ := raw["type"].(string)
	var dataType schema.DataType
	switch typeName {
	case "", "string":
		dataType = schema.String
	case "integer":
		dataType = schema.Integer
	case "number":
		dataType = schema.Number
	case "boolean":
		dataType = schema.Boolean
	case "array":
		dataType = schema.Array
	case "object":
		dataType = schema.Object
	default:
		return nil, fmt.Errorf("unsupported parameter type %q", typeName)
	}
	param := &schema.ParameterInfo{Type: dataType}
	param.Desc, _ = raw["description"].(string)
	if values, ok := raw["enum"].([]any); ok {
		for _, value := range values {
			if text, ok := value.(string); ok {
				param.Enum = append(param.Enum, text)
			}
		}
	}
	if dataType == schema.Array {
		if item, ok := raw["items"].(map[string]any); ok {
			nested, err := openAIParameter(item)
			if err != nil {
				return nil, err
			}
			param.ElemInfo = nested
		}
	}
	if dataType == schema.Object {
		nested, err := openAIParameters(raw)
		if err != nil {
			return nil, err
		}
		param.SubParams = nested
	}
	return param, nil
}

func newOpenAIResponse(model string, message *schema.Message) gin.H {
	toolCalls := make([]gin.H, 0, len(message.ToolCalls))
	for _, call := range message.ToolCalls {
		toolCalls = append(toolCalls, gin.H{"id": call.ID, "type": firstNonEmpty(call.Type, "function"), "function": gin.H{"name": call.Function.Name, "arguments": call.Function.Arguments}})
	}
	content := any(message.Content)
	if content == "" && len(toolCalls) > 0 {
		content = nil
	}
	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}
	response := gin.H{"id": "chatcmpl-" + ulid.Make().String(), "object": "chat.completion", "created": time.Now().Unix(), "model": model, "choices": []gin.H{{"index": 0, "message": gin.H{"role": "assistant", "content": content, "tool_calls": toolCalls}, "finish_reason": finishReason}}}
	if message.ReasoningContent != "" {
		response["choices"].([]gin.H)[0]["message"].(gin.H)["reasoning_content"] = message.ReasoningContent
	}
	return response
}

func newOpenAIChunk(model string, message *schema.Message) gin.H {
	delta := gin.H{"role": "assistant"}
	if message.Content != "" {
		delta["content"] = message.Content
	}
	if len(message.ToolCalls) > 0 {
		calls := make([]gin.H, 0, len(message.ToolCalls))
		for index, call := range message.ToolCalls {
			calls = append(calls, gin.H{"index": index, "id": call.ID, "type": firstNonEmpty(call.Type, "function"), "function": gin.H{"name": call.Function.Name, "arguments": call.Function.Arguments}})
		}
		delta["tool_calls"] = calls
	}
	finishReason := "stop"
	if len(message.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}
	chunk := gin.H{"id": "chatcmpl-" + ulid.Make().String(), "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model, "choices": []gin.H{{"index": 0, "delta": delta, "finish_reason": finishReason}}}
	if message.ReasoningContent != "" {
		chunk["choices"].([]gin.H)[0]["delta"].(gin.H)["reasoning_content"] = message.ReasoningContent
	}
	return chunk
}

func writeOpenAIData(c *gin.Context, value any) {
	payload, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", payload)
	c.Writer.Flush()
}

func openAIError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": gin.H{"message": message, "type": "invalid_request_error"}})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
