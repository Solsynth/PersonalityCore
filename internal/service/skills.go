package service

import (
	"encoding/json"
	"strings"

	"github.com/cloudwego/eino/schema"

	"src.solsynth.dev/sosys/personality/internal/agent"
)

func decodeToolCallArgs(call schema.ToolCall, out any) error {
	return json.Unmarshal([]byte(call.Function.Arguments), out)
}

type Skill struct {
	Name        string
	Description string
	Tools       func(s *ConversationService) []*schema.ToolInfo
}

var skillRegistry = map[string]Skill{
	"chat": {
		Name:        "chat",
		Description: "Send and manage messages in Solar Network chats",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.sendChatToolInfo(),
				s.sendChatBatchToolInfo(),
				s.noReplyToolInfo(),
			}
		},
	},
	"solar_network": {
		Name:        "solar_network",
		Description: "Look up Solar Network users, posts, profiles, and messages",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.getChatMessageToolInfo(),
				s.getUserProfileToolInfo(),
				s.listUserPostsToolInfo(),
				s.getPostToolInfo(),
				s.listPostRepliesToolInfo(),
			}
		},
	},
	"self_notes": {
		Name:        "self_notes",
		Description: "Remember and recall personal details across conversations",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.listSelfNotesToolInfo(),
				s.saveSelfNoteToolInfo(),
				s.deleteSelfNoteToolInfo(),
			}
		},
	},
	"tasks": {
		Name:        "tasks",
		Description: "Create and manage scheduled tasks that run automatically",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.createTaskToolInfo(),
				s.listTasksToolInfo(),
				s.updateTaskToolInfo(),
				s.deleteTaskToolInfo(),
			}
		},
	},
	"surfing": {
		Name:        "surfing",
		Description: "Browse, search, create, reply to, and repost posts on Solar Network",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.listFeedToolInfo(),
				s.searchPostsToolInfo(),
				s.createPostToolInfo(),
				s.replyToPostToolInfo(),
				s.repostPostToolInfo(),
				s.reactToPostToolInfo(),
				s.getPostSurfingToolInfo(),
				s.getPostRepliesSurfingToolInfo(),
				s.listMyPostsToolInfo(),
			}
		},
	},
}

func (s *ConversationService) availableSkills(def agent.Definition, activeSkills map[string]bool) []Skill {
	var skills []Skill
	isChat := agent.HasAbility(def, "chat")
	for name, skill := range skillRegistry {
		// chat skill is auto-loaded for chat agents, skip in discovery
		if name == "chat" && isChat {
			continue
		}
		if activeSkills[name] {
			continue
		}
		skills = append(skills, skill)
	}
	return skills
}

func (s *ConversationService) resolveSkillTools(activeSkills map[string]bool) []*schema.ToolInfo {
	var tools []*schema.ToolInfo
	for name := range activeSkills {
		if skill, ok := skillRegistry[name]; ok {
			tools = append(tools, skill.Tools(s)...)
		}
	}
	return tools
}

func (s *ConversationService) listSkillsToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name:        "list_skills",
		Desc:        "List available skills that can be activated to add new tools. Use this to discover what capabilities you can load.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func (s *ConversationService) activateSkillToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: "activate_skill",
		Desc: "Activate a skill to load its tools into your available tool set. Use list_skills first to see what is available.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"skill": {
				Type:     schema.String,
				Desc:     "Name of the skill to activate.",
				Required: true,
			},
		}),
	}
}

func (s *ConversationService) executeListSkillsToolCall(def agent.Definition, activeSkills map[string]bool) *executedChatToolResult {
	skills := s.availableSkills(def, activeSkills)
	if len(skills) == 0 {
		return &executedChatToolResult{
			Content: `{"skills":[],"message":"No additional skills available."}`,
			ToolName: "list_skills",
		}
	}
	var items []string
	for _, sk := range skills {
		items = append(items, `{"name":"`+sk.Name+`","description":"`+strings.ReplaceAll(sk.Description, `"`, `\"`) + `"}`)
	}
	return &executedChatToolResult{
		Content: `{"skills":[` + strings.Join(items, ",") + `]}`,
		ToolName: "list_skills",
	}
}

func (s *ConversationService) executeActivateSkillToolCall(call schema.ToolCall, activeSkills map[string]bool) *executedChatToolResult {
	var input struct {
		Skill string `json:"skill"`
	}
	if err := decodeToolCallArgs(call, &input); err != nil {
		return &executedChatToolResult{
			Content:    `{"ok":false,"error":"invalid arguments"}`,
			ToolName:   "activate_skill",
			ToolCallID: call.ID,
		}
	}
	skillName := strings.TrimSpace(input.Skill)
	skill, exists := skillRegistry[skillName]
	if !exists {
		return &executedChatToolResult{
			Content:    `{"ok":false,"error":"skill not found: ` + skillName + `"}`,
			ToolName:   "activate_skill",
			ToolCallID: call.ID,
		}
	}
	if activeSkills[skillName] {
		return &executedChatToolResult{
			Content:    `{"ok":true,"skill":"` + skillName + `","message":"Already active."}`,
			ToolName:   "activate_skill",
			ToolCallID: call.ID,
		}
	}
	activeSkills[skillName] = true
	var toolNames []string
	for _, t := range skill.Tools(s) {
		toolNames = append(toolNames, t.Name)
	}
	return &executedChatToolResult{
		Content:    `{"ok":true,"skill":"` + skillName + `","tools":["` + strings.Join(toolNames, `","`) + `"]}`,
		ToolName:   "activate_skill",
		ToolCallID: call.ID,
	}
}
