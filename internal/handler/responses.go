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
}

type responseRequest struct {
	AgentID            string          `json:"agent_id"`
	ConversationID     string          `json:"conversation_id"`
	PreviousResponseID string          `json:"previous_response_id"`
	Instructions       string          `json:"instructions"`
	Input              json.RawMessage `json:"input"`
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
	message, err := parseResponseMessage(request.Instructions, request.Input)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, err := conversations.ExecuteResponse(c.Request.Context(), accountID, service.ResponseInput{
		AgentID:            request.AgentID,
		ConversationID:     request.ConversationID,
		PreviousResponseID: request.PreviousResponseID,
		Message:            message,
	})
	if err != nil {
		status := http.StatusBadRequest
		if err == service.ErrNotFound || err == service.ErrForbidden {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	if result == nil || result.Run == nil || result.Thread == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "generation returned no response"})
		return
	}

	content := result.ResponseContent
	c.JSON(http.StatusOK, gin.H{
		"id":              result.Run.ID,
		"object":          "personality.response",
		"agent_id":        result.Thread.AgentID,
		"conversation_id": result.Thread.ID,
		"model":           result.Run.Model,
		"output_text":     content,
		"output": []gin.H{{
			"type": "message",
			"role": "assistant",
			"content": []gin.H{{
				"type": "output_text",
				"text": content,
			}},
		}},
	})
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
