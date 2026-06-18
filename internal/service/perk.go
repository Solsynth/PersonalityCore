package service

import (
	"strings"

	"src.solsynth.dev/sosys/personality/internal/config"
)

// PerkLimits holds the resolved global-tier limits for a given perk level.
type PerkLimits struct {
	MaxHistoryMessages  int      // 0 = use base config
	MaxCompletionTokens int      // 0 = use model/agent default
	AllowVision         bool     // true if vision is allowed
	AllowFileSummary    bool     // true if file summary is allowed
	BlockedSkills       []string // skill names denied at this tier
}

// resolvePerkLimits resolves global-tier limits for the given perk level.
// Unset fields fall back to unrestricted (true for booleans, 0 for ints).
func (s *ConversationService) resolvePerkLimits(perkLevel int32) PerkLimits {
	limits := PerkLimits{
		MaxHistoryMessages: 0, // 0 = use base config
		MaxCompletionTokens: 0,
		AllowVision:        true,
		AllowFileSummary:   true,
	}

	tier, ok := s.cfg.Personality.PerkTiers[int(perkLevel)]
	if !ok {
		return limits
	}

	if tier.MaxHistoryMessages != nil {
		limits.MaxHistoryMessages = *tier.MaxHistoryMessages
	}
	if tier.MaxCompletionTokens != nil {
		limits.MaxCompletionTokens = *tier.MaxCompletionTokens
	}
	if tier.AllowVision != nil {
		limits.AllowVision = *tier.AllowVision
	}
	if tier.AllowFileSummary != nil {
		limits.AllowFileSummary = *tier.AllowFileSummary
	}
	if len(tier.BlockedSkills) > 0 {
		limits.BlockedSkills = append([]string(nil), tier.BlockedSkills...)
	}

	return limits
}

// isModelBlocked checks whether the given model is blocked at this perk level.
// Checks the model's own perkOverrides[perkLevel].blocked.
func (s *ConversationService) isModelBlocked(perkLevel int32, modelName string) bool {
	modelRef := strings.TrimSpace(modelName)
	if modelRef == "" {
		return false
	}

	parts := strings.SplitN(modelRef, "/", 2)
	if len(parts) != 2 {
		return false
	}
	providerID := strings.TrimSpace(parts[0])
	modelNameOnly := strings.TrimSpace(parts[1])

	for _, p := range s.cfg.Providers {
		if strings.EqualFold(strings.TrimSpace(p.ID), providerID) {
			if mc := p.ResolveModel(modelNameOnly); mc != nil {
				if ov, ok := mc.PerkOverrides[int(perkLevel)]; ok {
					if ov.Blocked != nil && *ov.Blocked {
						return true
					}
				}
			}
			break
		}
	}
	return false
}

// resolveMaxCompletionTokens resolves the effective max completion tokens
// using the resolution order:
// 1. agent perkOverrides[perkLevel]
// 2. model perkOverrides[perkLevel]
// 3. global tier maxCompletionTokens
// 4. agent config maxCompletionTokens
// 5. 0 (let provider decide)
func (s *ConversationService) resolveMaxCompletionTokens(perkLevel int32, agentDef agentDefLite) int {
	// 1. agent perk override
	if agentDef.PerkOverrides != nil {
		if ov, ok := agentDef.PerkOverrides[int(perkLevel)]; ok && ov.MaxCompletionTokens != nil {
			return *ov.MaxCompletionTokens
		}
	}

	// 2. model perk override
	modelRef := strings.TrimSpace(agentDef.Model)
	if modelRef != "" {
		parts := strings.SplitN(modelRef, "/", 2)
		if len(parts) == 2 {
			providerID := strings.TrimSpace(parts[0])
			modelNameOnly := strings.TrimSpace(parts[1])
			for _, p := range s.cfg.Providers {
				if strings.EqualFold(strings.TrimSpace(p.ID), providerID) {
					if mc := p.ResolveModel(modelNameOnly); mc != nil {
						if ov, ok := mc.PerkOverrides[int(perkLevel)]; ok && ov.MaxCompletionTokens != nil {
							return *ov.MaxCompletionTokens
						}
					}
					break
				}
			}
		}
	}

	// 3. global tier
	if tier, ok := s.cfg.Personality.PerkTiers[int(perkLevel)]; ok && tier.MaxCompletionTokens != nil {
		return *tier.MaxCompletionTokens
	}

	// 4. agent config
	if agentDef.MaxCompletionTokens != nil {
		return *agentDef.MaxCompletionTokens
	}

	// 5. let provider decide
	return 0
}

// isSkillAllowed checks whether the given skill is allowed at this perk level.
// Returns true if allowed, false if blocked.
func (s *ConversationService) isSkillAllowed(perkLevel int32, skillName string) bool {
	tier, ok := s.cfg.Personality.PerkTiers[int(perkLevel)]
	if !ok || len(tier.BlockedSkills) == 0 {
		return true
	}
	skillLower := strings.ToLower(strings.TrimSpace(skillName))
	for _, blocked := range tier.BlockedSkills {
		if strings.ToLower(strings.TrimSpace(blocked)) == skillLower {
			return false
		}
	}
	return true
}

// agentDefLite is the minimal subset of agent.Definition needed for perk resolution.
// Avoids importing the agent package here.
type agentDefLite struct {
	Model               string
	MaxCompletionTokens *int
	PerkOverrides       map[int]config.AgentPerkOverride
}
