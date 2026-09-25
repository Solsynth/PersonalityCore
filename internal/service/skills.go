package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/cloudwego/eino/schema"
	"gorm.io/datatypes"

	"src.solsynth.dev/sosys/persona/internal/agent"
	"src.solsynth.dev/sosys/persona/internal/database"
	"src.solsynth.dev/sosys/persona/internal/logging"
)

func decodeToolCallArgs(call schema.ToolCall, out any) error {
	return json.Unmarshal([]byte(call.Function.Arguments), out)
}

// loadSkillToolName is the caller-owned tool that loads a capability whose
// definitions live on the caller.
//
// The server never registers it — it has no tools to run — but it has to name
// it: a listing has to tell the model which tool loads a local entry, and a
// refusal has to say the same. It is the caller's own name under the client
// namespace, so the two sides have to agree on it.
const loadSkillToolName = clientToolNamespace + "load_skill"

// clientToolNamespace is what the server puts in front of every name that
// belongs to the caller: the tools it runs, and the skills it offers to load.
//
// The server adds it rather than asking the caller, which is what makes a
// caller-owned name unable to shadow a server-owned one however the caller
// names things, and what lets a listing say which side a capability lives on.
//
// It is deliberately not `local/`: OpenAI and Anthropic accept only
// `[a-zA-Z0-9_-]` in a function name, and one rejected name fails the whole
// run.
const clientToolNamespace = "local_"

// namespaceClientTools returns the caller's tools under the client namespace.
// It copies, because a caller's ToolInfo values are shared with whatever
// produced them.
func namespaceClientTools(tools []*schema.ToolInfo) []*schema.ToolInfo {
	if len(tools) == 0 {
		return nil
	}
	namespaced := make([]*schema.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		copied := *tool
		copied.Name = clientToolNamespace + tool.Name
		namespaced = append(namespaced, &copied)
	}
	return namespaced
}

// namespaceClientSkills returns the capabilities a caller declares it could
// load, under the same namespace as the tools loading one produces.
func namespaceClientSkills(skills []ClientSkill) []ClientSkill {
	if len(skills) == 0 {
		return nil
	}
	namespaced := make([]ClientSkill, 0, len(skills))
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		namespaced = append(namespaced, ClientSkill{
			Name:        clientToolNamespace + name,
			Description: skill.Description,
		})
	}
	return namespaced
}

// ClientSkill is a capability the caller can load on request.
//
// The caller declares these with every run. Its tools live on the caller, so
// the server can do no more than name the capability in `list_skills` and let
// the model load it through the caller.
type ClientSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// activatedSkills reads the server-owned skills a conversation has switched
// on. A thread that has activated nothing, or that predates the column, reads
// as empty.
func activatedSkills(thread *database.ConversationThread) map[string]bool {
	if thread == nil || len(thread.ActivatedSkills) == 0 {
		return map[string]bool{}
	}
	var names []string
	if err := json.Unmarshal(thread.ActivatedSkills, &names); err != nil {
		logging.Log.Warn().
			Str("conversation_id", thread.ID).
			Err(err).
			Msg("unreadable activated skills; treating the conversation as having none")
		return map[string]bool{}
	}
	active := make(map[string]bool, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			active[name] = true
		}
	}
	return active
}

// persistActivatedSkills records what the conversation has switched on, so the
// next run rebuilds the same tool set instead of making the model ask again.
// Stored sorted, so an unchanged set is recognisably unchanged.
//
// A failed write is reported, never fatal: the activation still applies to the
// turn that made it, and the worst case is the model asked again next time.
func (s *ConversationService) persistActivatedSkills(ctx context.Context, thread *database.ConversationThread, active map[string]bool) {
	if thread == nil {
		return
	}
	names := make([]string, 0, len(active))
	for name := range active {
		names = append(names, name)
	}
	sort.Strings(names)
	encoded, err := json.Marshal(names)
	if err != nil {
		return
	}
	if string(thread.ActivatedSkills) == string(encoded) {
		return
	}
	if err := s.db.WithContext(ctx).
		Model(&database.ConversationThread{}).
		Where("id = ?", thread.ID).
		Update("activated_skills", datatypes.JSON(encoded)).Error; err != nil {
		logging.Log.Error().
			Str("conversation_id", thread.ID).
			Err(err).
			Msg("recording activated skills failed")
		return
	}
	thread.ActivatedSkills = datatypes.JSON(encoded)
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
	"memory": {
		Name:        "memory",
		Description: "Search and manage durable user memories",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.memorySearchToolInfo(),
				s.memorySaveToolInfo(),
				s.memoryForgetToolInfo(),
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
	"files": {
		Name:        "files",
		Description: "Browse, upload, and manage files in Solar Drive",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.listFilesToolInfo(),
				s.getFileInfoToolInfo(),
				s.createFolderToolInfo(),
				s.uploadTextFileToolInfo(),
				s.recycleFileToolInfo(),
				s.restoreFileToolInfo(),
				s.listRecycleBinToolInfo(),
				s.getStorageQuotaToolInfo(),
			}
		},
	},
	"wallet": {
		Name:        "wallet",
		Description: "View Solar wallets and orders",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.listWalletsToolInfo(),
				s.listOrdersToolInfo(),
			}
		},
	},
	"notifications": {
		Name:        "notifications",
		Description: "View and manage Solar notifications",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.listNotificationsToolInfo(),
				s.getUnreadNotificationCountToolInfo(),
				s.markAllNotificationsReadToolInfo(),
			}
		},
	},
	"web_reader": {
		Name:        "web_reader",
		Description: "Read and extract content from web pages",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.readWebpageToolInfo(),
			}
		},
	},
	"web_search": {
		Name:        "web_search",
		Description: "Search the public web for current information",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.webSearchToolInfo(),
			}
		},
	},
	"relationships": {
		Name:        "relationships",
		Description: "View and manage Solar relationships (follow, unfollow, friends)",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.listRelationshipsToolInfo(),
				s.followAccountToolInfo(),
				s.unfollowAccountToolInfo(),
			}
		},
	},
	"search": {
		Name:        "search",
		Description: "Search for Solar Network accounts",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.searchAccountsToolInfo(),
			}
		},
	},
	"stickers": {
		Name:        "stickers",
		Description: "Browse, search, and load Solar sticker packs and individual stickers",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.listStickersToolInfo(),
				s.searchStickersToolInfo(),
				s.getPackStickersToolInfo(),
			}
		},
	},
	"surveys": {
		Name:        "surveys",
		Description: "View Solar surveys",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.listSurveysToolInfo(),
			}
		},
	},
	"leveling": {
		Name:        "leveling",
		Description: "View Solar leveling and experience history",
		Tools: func(s *ConversationService) []*schema.ToolInfo {
			return []*schema.ToolInfo{
				s.getMyLevelingToolInfo(),
			}
		},
	},
}

// abilityGatedSkills maps a skill to the agent ability that unlocks it. For
// these skills the ability is the whole gate: capable agents get the tools
// auto-loaded, every other agent never sees or activates them. Skills absent
// from this map are gated by buildToolInfos (chat, solar_network, self_notes)
// or by their OAuth session requirement (userSkillAbilities).
var abilityGatedSkills = map[string]string{
	"web_search": "web_search",
}

// skillAllowedForAgent reports whether an agent may use a skill at all.
func (s *ConversationService) skillAllowedForAgent(def agent.Definition, name string) bool {
	ability, gated := abilityGatedSkills[name]
	return !gated || agent.HasAbility(def, ability)
}

func (s *ConversationService) availableSkills(def agent.Definition, activeSkills map[string]bool, perkLevel int32) []Skill {
	var skills []Skill
	loaded := s.autoLoadedSkills(def, perkLevel)
	oauthReady := s.oauthReady()
	for name, skill := range skillRegistry {
		// Skills whose tools this agent already has must not be advertised:
		// activating them would be a no-op the model cannot observe.
		if loaded[name] || activeSkills[name] {
			continue
		}
		if !s.isSkillAllowed(perkLevel, name) {
			continue
		}
		if !s.skillAllowedForAgent(def, name) {
			continue
		}
		// OAuth-backed skills only produce failing tools without a session.
		if userSkillNames[name] && !oauthReady {
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

// listedSkill is one entry of the `list_skills` answer: what the capability
// is, and which tool loads it.
type listedSkill struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ActivateWith string `json:"activate_with"`
}

func (s *ConversationService) executeListSkillsToolCall(def agent.Definition, activeSkills map[string]bool, perkLevel int32, clientSkills []ClientSkill) *executedChatToolResult {
	clientSkills = namespaceClientSkills(clientSkills)
	listed := make([]listedSkill, 0, len(clientSkills)+4)
	for _, sk := range s.availableSkills(def, activeSkills, perkLevel) {
		listed = append(listed, listedSkill{
			Name:         sk.Name,
			Description:  sk.Description,
			ActivateWith: "activate_skill",
		})
	}
	// The caller's own capabilities are listed alongside the server's so the
	// model asks in one place. They are loaded through the caller instead,
	// because only the caller holds their tools.
	for _, sk := range clientSkills {
		name := strings.TrimSpace(sk.Name)
		if name == "" || activeSkills[name] {
			continue
		}
		listed = append(listed, listedSkill{
			Name:         name,
			Description:  strings.TrimSpace(sk.Description),
			ActivateWith: loadSkillToolName,
		})
	}
	if len(listed) == 0 {
		return &executedChatToolResult{
			Content:  `{"skills":[],"message":"No additional skills available."}`,
			ToolName: "list_skills",
		}
	}
	encoded, _ := json.Marshal(map[string]any{"skills": listed})
	return &executedChatToolResult{
		Content:  string(encoded),
		ToolName: "list_skills",
	}
}

// executeActivateSkillToolCall switches on one server-owned skill. It reports
// whether it changed anything, so the caller persists only real activations.
//
// A name the caller declared as its own is refused here with a pointer at the
// tool that loads it: the server has no tools to add for it, and pretending
// otherwise would leave the model calling tools that do not exist.
func (s *ConversationService) executeActivateSkillToolCall(call schema.ToolCall, activeSkills map[string]bool, def agent.Definition, clientSkills []ClientSkill) (*executedChatToolResult, bool) {
	var input struct {
		Skill string `json:"skill"`
	}
	if err := decodeToolCallArgs(call, &input); err != nil {
		return &executedChatToolResult{
			Content:    `{"ok":false,"error":"invalid arguments"}`,
			ToolName:   "activate_skill",
			ToolCallID: call.ID,
		}, false
	}
	skillName := strings.TrimSpace(input.Skill)
	skill, exists := skillRegistry[skillName]
	if !exists {
		for _, sk := range namespaceClientSkills(clientSkills) {
			if strings.TrimSpace(sk.Name) != skillName {
				continue
			}
			encoded, _ := json.Marshal(map[string]any{
				"ok":            false,
				"error":         "skill " + skillName + " runs on the caller",
				"activate_with": loadSkillToolName,
			})
			return &executedChatToolResult{
				Content:    string(encoded),
				ToolName:   "activate_skill",
				ToolCallID: call.ID,
			}, false
		}
		return &executedChatToolResult{
			Content:    `{"ok":false,"error":"skill not found: ` + skillName + `"}`,
			ToolName:   "activate_skill",
			ToolCallID: call.ID,
		}, false
	}
	if !s.skillAllowedForAgent(def, skillName) {
		return &executedChatToolResult{
			Content:    `{"ok":false,"error":"skill not available: ` + skillName + `"}`,
			ToolName:   "activate_skill",
			ToolCallID: call.ID,
		}, false
	}
	if activeSkills[skillName] {
		return &executedChatToolResult{
			Content:    `{"ok":true,"skill":"` + skillName + `","message":"Already active."}`,
			ToolName:   "activate_skill",
			ToolCallID: call.ID,
		}, false
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
	}, true
}
