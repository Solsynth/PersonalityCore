package agent

import (
	"fmt"
	"sort"
	"strings"

	"src.solsynth.dev/sosys/personality/internal/config"
)

type Definition struct {
	ID                  string                              `json:"id"`
	Name                string                              `json:"name"`
	Description         string                              `json:"description"`
	SystemPrompt        string                              `json:"system_prompt"`
	Model               string                              `json:"model"`
	Temperature         *float32                            `json:"temperature,omitempty"`
	TopP                *float32                            `json:"top_p,omitempty"`
	MaxCompletionTokens *int                                `json:"max_completion_tokens,omitempty"`
	ChatMaxCompletionTokens *int                            `json:"-"`
	Abilities           []string                            `json:"abilities"`
	Enabled             bool                                `json:"enabled"`
	Autonomous          config.AgentAutonomousConfig        `json:"-"`
	SolarIntegration    config.AgentSolarNetworkIntegration `json:"-"`
}

type Registry struct {
	agents map[string]Definition
	order  []string
}

func NewRegistry(cfgs []config.AgentConfig) (*Registry, error) {
	agents := make(map[string]Definition, len(cfgs))
	order := make([]string, 0, len(cfgs))

	for _, cfg := range cfgs {
		id := strings.TrimSpace(cfg.ID)
		if id == "" {
			return nil, fmt.Errorf("agent id is required")
		}
		if _, exists := agents[id]; exists {
			return nil, fmt.Errorf("duplicate agent id %q", id)
		}
		if strings.TrimSpace(cfg.Name) == "" {
			return nil, fmt.Errorf("agent %q name is required", id)
		}

		agents[id] = Definition{
			ID:                  id,
			Name:                strings.TrimSpace(cfg.Name),
			Description:         strings.TrimSpace(cfg.Description),
			SystemPrompt:        cfg.SystemPrompt,
			Model:               strings.TrimSpace(cfg.Model),
			Temperature:         cfg.Temperature,
			TopP:                cfg.TopP,
			MaxCompletionTokens: cfg.MaxCompletionTokens,
			ChatMaxCompletionTokens: cfg.ChatMaxCompletionTokens,
			Abilities:           append([]string(nil), cfg.Abilities...),
			Enabled:             cfg.Enabled,
			Autonomous:          cfg.Autonomous,
			SolarIntegration:    cfg.SolarNetworkIntegration,
		}
		order = append(order, id)
	}

	sort.Strings(order)
	return &Registry{agents: agents, order: order}, nil
}

func HasAbility(def Definition, ability string) bool {
	want := strings.TrimSpace(strings.ToLower(ability))
	for _, item := range def.Abilities {
		if strings.TrimSpace(strings.ToLower(item)) == want {
			return true
		}
	}
	return false
}

func (r *Registry) List() []Definition {
	if r == nil {
		return nil
	}
	result := make([]Definition, 0, len(r.order))
	for _, id := range r.order {
		agent := r.agents[id]
		if !agent.Enabled {
			continue
		}
		result = append(result, agent)
	}
	return result
}

func (r *Registry) Get(id string) (Definition, bool) {
	if r == nil {
		return Definition{}, false
	}
	agent, ok := r.agents[strings.TrimSpace(id)]
	if !ok || !agent.Enabled {
		return Definition{}, false
	}
	return agent, true
}
