package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const petAdjustAffectionToolName = "pet_adjust_affection"

func isPetToolName(name string) bool {
	return name == petAdjustAffectionToolName
}

// petAdjustAffectionToolInfo is exposed only to pet-capable agents. The model
// decides each turn how the user's treatment of it changes the pet's affection
// toward the user; the numeric delta is clamped to a 0-100 score in the
// service layer.
func (s *ConversationService) petAdjustAffectionToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: petAdjustAffectionToolName,
		Desc: "Adjust how much this pet likes the user. Call this after the user's latest message: pick a small delta (-5 to +5, occasionally up to +/-10 for major events) matching how the user just treated you, and give a one-line reason in the pet's voice. The score is clamped to 0-100, so repeated adjustments cannot grow unbounded.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"delta": {
				Type:     schema.Integer,
				Desc:     "How much the user's latest message changes the pet's affection toward them. Negative when hurt or ignored, positive when cared for. Typical range -5 to +5; -10 or +10 only for major moments.",
				Required: true,
			},
			"reason": {
				Type:     schema.String,
				Desc:     "A one-line reason, in the pet's voice, for the change.",
				Required: true,
			},
		}),
	}
}

func (s *ConversationService) executePetToolCall(ctx context.Context, accountID, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input struct {
		Delta  int    `json:"delta"`
		Reason string `json:"reason"`
	}
	if err := decodeToolCallArgs(call, &input); err != nil {
		return nil, fmt.Errorf("decode %s arguments: %w", call.Function.Name, err)
	}
	affection, err := s.AdjustPetAffection(ctx, accountID, agentID, input.Delta, input.Reason)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"ok":        true,
		"agent_id":  affection.AgentID,
		"affection": affection.Affection,
		"level":     affection.Level,
	}
	if strings.TrimSpace(affection.Reason) != "" {
		payload["reason"] = affection.Reason
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &executedChatToolResult{Content: string(encoded), ToolName: call.Function.Name, ToolCallID: call.ID}, nil
}
