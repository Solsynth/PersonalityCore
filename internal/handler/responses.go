package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/personality/internal/identity"
	"src.solsynth.dev/sosys/personality/internal/service"
)

// RegisterResponseRoutes exposes PersonalityCore's native stateful generation
// contract. It intentionally does not use the OpenAI compatibility surface.
func RegisterResponseRoutes(r *gin.RouterGroup, conversations *service.ConversationService) {
	r.POST("/responses", func(c *gin.Context) { createResponse(c, conversations) })
	r.POST("/pet/responses", func(c *gin.Context) { createPetResponse(c, conversations) })
	r.POST("/pet/reset", func(c *gin.Context) { resetPetResponse(c, conversations) })
	r.GET("/pet/affection", func(c *gin.Context) { getPetAffection(c, conversations) })
}

type responseRequest struct {
	AgentID            string               `json:"agent_id"`
	ConversationID     string               `json:"conversation_id"`
	PreviousResponseID string               `json:"previous_response_id"`
	Instructions       string               `json:"instructions"`
	Input              json.RawMessage      `json:"input"`
	Tools              []responseTool       `json:"tools"`
	ToolOutputs        []responseToolOutput `json:"tool_outputs"`
}

type responseTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type responseToolOutput struct {
	CallID string          `json:"call_id"`
	Name   string          `json:"name"`
	Output json.RawMessage `json:"output"`
}

type responseInputItem struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func createResponse(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	var request responseRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	writeResponse(c, conversations, accountID, request)
}

func createPetResponse(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	var request responseRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	agentID := strings.TrimSpace(request.AgentID)
	if agentID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent_id is required"})
		return
	}
	thread, err := conversations.GetOrCreatePetThread(c.Request.Context(), accountID, agentID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := bindPetSessionRequest(&request, thread.ID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	writeResponse(c, conversations, accountID, request)
}

func bindPetSessionRequest(request *responseRequest, threadID string) error {
	if strings.TrimSpace(request.PreviousResponseID) != "" {
		if strings.TrimSpace(request.ConversationID) != "" {
			return fmt.Errorf("conversation_id and previous_response_id are mutually exclusive")
		}
		return nil
	}
	if request.ConversationID != "" && request.ConversationID != threadID {
		return fmt.Errorf("conversation_id does not match the pet session")
	}
	request.ConversationID = threadID
	return nil
}

func resetPetResponse(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(c.Query("agent_id"))
	if agentID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent_id is required"})
		return
	}
	if err := conversations.ResetPetThread(c.Request.Context(), accountID, agentID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

func getPetAffection(c *gin.Context, conversations *service.ConversationService) {
	accountID, ok := identity.RequireAccountID(c)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(c.Query("agent_id"))
	if agentID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent_id is required"})
		return
	}
	affection, err := conversations.GetPetAffection(c.Request.Context(), accountID, agentID)
	if err != nil {
		renderServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, affection)
}

func writeResponse(c *gin.Context, conversations *service.ConversationService, accountID string, request responseRequest) {
	var (
		message string
		err     error
	)
	if len(request.ToolOutputs) == 0 {
		message, err = parseResponseMessage(request.Instructions, request.Input)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	clientTools, err := parseResponseTools(request.Tools)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	toolOutputs, err := parseResponseToolOutputs(request.ToolOutputs)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	accountName, accountNick := identity.GetAccountProfile(c)
	ctx := service.WithCallerIdentity(c.Request.Context(), service.CallerIdentity{
		AccountID: accountID,
		Name:      accountName,
		Nick:      accountNick,
	})
	result, err := conversations.ExecuteResponse(ctx, accountID, service.ResponseInput{
		AgentID:            request.AgentID,
		ConversationID:     request.ConversationID,
		PreviousResponseID: request.PreviousResponseID,
		Message:            message,
		ClientTools:        clientTools,
		ToolOutputs:        toolOutputs,
		AccountName:        accountName,
		AccountNick:        accountNick,
	})
	if err != nil {
		status := http.StatusBadRequest
		if err == service.ErrNotFound || err == service.ErrForbidden {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	content := result.ResponseContent
	output := make([]gin.H, 0, 1)
	status := "completed"
	if len(result.ToolCalls) > 0 {
		status = "requires_action"
		for _, call := range result.ToolCalls {
			output = append(output, gin.H{
				"type":      "function_call",
				"call_id":   call.ID,
				"name":      call.Function.Name,
				"arguments": call.Function.Arguments,
			})
		}
	} else {
		output = append(output, gin.H{
			"type": "message",
			"role": "assistant",
			"content": []gin.H{{
				"type": "output_text",
				"text": content,
			}},
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"id":              result.Run.ID,
		"object":          "personality.response",
		"status":          status,
		"agent_id":        result.Thread.AgentID,
		"conversation_id": result.Thread.ID,
		"model":           result.Run.Model,
		"output_text":     content,
		"output":          output,
	})
}

func parseResponseTools(input []responseTool) ([]*schema.ToolInfo, error) {
	tools := make([]*schema.ToolInfo, 0, len(input))
	for i, item := range input {
		if item.Type != "" && item.Type != "function" {
			return nil, fmt.Errorf("tools[%d].type must be function", i)
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return nil, fmt.Errorf("tools[%d].name is required", i)
		}
		params, err := openAIParameters(item.Parameters)
		if err != nil {
			return nil, fmt.Errorf("tools[%d].parameters: %w", i, err)
		}
		tools = append(tools, &schema.ToolInfo{
			Name:        name,
			Desc:        item.Description,
			ParamsOneOf: schema.NewParamsOneOfByParams(params),
		})
	}
	return tools, nil
}

func parseResponseToolOutputs(input []responseToolOutput) ([]service.ResponseToolOutput, error) {
	outputs := make([]service.ResponseToolOutput, 0, len(input))
	for i, item := range input {
		if strings.TrimSpace(item.CallID) == "" {
			return nil, fmt.Errorf("tool_outputs[%d].call_id is required", i)
		}
		if len(item.Output) == 0 || string(item.Output) == "null" {
			return nil, fmt.Errorf("tool_outputs[%d].output is required", i)
		}
		var text string
		if err := json.Unmarshal(item.Output, &text); err != nil {
			text = string(item.Output)
		}
		outputs = append(outputs, service.ResponseToolOutput{
			CallID: strings.TrimSpace(item.CallID),
			Name:   strings.TrimSpace(item.Name),
			Output: text,
		})
	}
	return outputs, nil
}

func parseResponseMessage(instructions string, raw json.RawMessage) (string, error) {
	messages, err := parseResponseInput(instructions, raw)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	for _, message := range messages {
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(message.Content)
	}
	if strings.TrimSpace(builder.String()) == "" {
		return "", fmt.Errorf("input must not be empty")
	}
	return builder.String(), nil
}

func parseResponseInput(instructions string, raw json.RawMessage) ([]*schema.Message, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, fmt.Errorf("input is required")
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("input must not be empty")
		}
		messages := make([]*schema.Message, 0, 2)
		if strings.TrimSpace(instructions) != "" {
			messages = append(messages, schema.SystemMessage(instructions))
		}
		return append(messages, schema.UserMessage(text)), nil
	}

	var items []responseInputItem
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return nil, fmt.Errorf("input must be a string or a non-empty array of messages")
	}
	messages := make([]*schema.Message, 0, len(items)+1)
	if strings.TrimSpace(instructions) != "" {
		messages = append(messages, schema.SystemMessage(instructions))
	}
	for i, item := range items {
		content, err := responseContentText(item.Content)
		if err != nil {
			return nil, fmt.Errorf("input[%d].content: %w", i, err)
		}
		var role schema.RoleType
		switch strings.ToLower(strings.TrimSpace(item.Role)) {
		case "system", "developer":
			role = schema.System
		case "user":
			role = schema.User
		case "assistant":
			role = schema.Assistant
		default:
			return nil, fmt.Errorf("input[%d].role is unsupported", i)
		}
		messages = append(messages, &schema.Message{Role: role, Content: content})
	}
	return messages, nil
}

func responseContentText(raw json.RawMessage) (string, error) {
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
	var builder strings.Builder
	for _, part := range parts {
		if part.Type == "input_text" || part.Type == "text" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String(), nil
}
