package service

import "testing"

func TestResolveOpenAIAgentModel(t *testing.T) {
	tests := []struct {
		name, agentID, model, wantAgent, wantModel string
		wantErr                                    bool
	}{
		{name: "agent default model", model: "assistant", wantAgent: "assistant"},
		{name: "agent selected provider model", model: "assistant/openai/gpt-4.1-mini", wantAgent: "assistant", wantModel: "openai/gpt-4.1-mini"},
		{name: "raw model proxy", model: "raw/openai/gpt-4.1-mini", wantAgent: "raw", wantModel: "openai/gpt-4.1-mini"},
		{name: "legacy agent id", agentID: "assistant", model: "openai/gpt-4.1-mini", wantAgent: "assistant", wantModel: "openai/gpt-4.1-mini"},
		{name: "reject ambiguous model", model: "openai/gpt-4.1-mini", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agentID, model, err := resolveOpenAIAgentModel(tc.agentID, tc.model)
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolveOpenAIAgentModel() error = %v, wantErr %v", err, tc.wantErr)
			}
			if agentID != tc.wantAgent || model != tc.wantModel {
				t.Fatalf("resolveOpenAIAgentModel() = (%q, %q), want (%q, %q)", agentID, model, tc.wantAgent, tc.wantModel)
			}
		})
	}
}
