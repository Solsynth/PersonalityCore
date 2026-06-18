# System Prompt Structure

The system prompt sent to the model is assembled as a sequence of `SystemMessage` entries. Each section is appended only when its conditions are met; absent sections are skipped entirely.

---

## 1. Agent System Prompt

Always included if `def.SystemPrompt` is non-empty.

Source: agent config `systemPrompt` inline or `systemPromptFile`.

---

## 2. Character Consistency Overlay

Always included.

```
Stay in character for the entire conversation.
Do not describe yourself as an AI, assistant, language model, system prompt, or out-of-character narrator unless the existing role definition explicitly requires that framing.
Respond in your character's own tone, identity, and perspective.
Many different people may talk to you. Distinguish them carefully by sender identity, account, room context, and remembered history instead of blending them together.
```

---

## 3. Compacted Thread Context

Included when `thread.ContextSummary` is non-empty (set by the context compaction pass when history exceeds `maxHistoryMessages`).

```
Earlier compacted thread context:

<summary>
```

---

## 4. Solar Chat Overlay

Included only when the agent has the `chat` ability AND a solar room binding exists for this thread.

Sub-sections, in order:

| Sub-section | Condition | Content |
|---|---|---|
| Room type | always | "This is a DM" or "This is a group chat. Multiple different users may be speaking." |
| Tool instructions | always | Must use `send_chat_message` / `send_chat_message_batch` / `no_reply` tools. |
| Room ID | always | `Current room_id: "<id>"` |
| Room behavior | always | DM: "respond proactively, warmly". Group: "be selective, keep replies concise, avoid jumping into every message unless the bot was explicitly mentioned." |
| Inbound prompt | latest inbound meta exists | DM: "appropriate to reply proactively". Group + mentioned: "mentioned the bot. Decide whether to join." Group + not mentioned: "did not mention the bot." May include replied message content. |
| Engagement prompt | binding exists | Active window: "may continue proactively even without a fresh mention, but can still choose to stay silent." Passive: "do not reply unless the latest message directly mentioned the bot." |
| Sender identity | meta or binding | `username`, `display_name`, `account_id` of the latest sender. |
| Remote account | binding | `Current remote account: "<name>" (<id>)` |
| Sender profile | cached profile exists | `bio`, `gender`, `pronouns`, `location`, `birthday`, `language` from the sender's Solar account profile. Only non-empty fields are included. |
| Sender local time | cached profile has `time_zone` | `Current time for the sender (Asia/Shanghai): 2025-06-18 13:45 CST.` Invalid timezone values are silently skipped. |

---

## 5. Agent Identity Overlay

Included when humanize is enabled. Contains the agent's self-notes from the database, formatted as:

```
[category] key: content
```

Each note is a separate line. Notes without a category or key omit those prefixes.

---

## 6. Humanizer Overlay

Included when humanize is enabled AND the agent has humanizer-related abilities (`memory`, `saved_memory`, `cross_conversation_memory`, `mood`, `relationship`). The `humanizer` composite ability enables all of these.

```
Internal persona state:

Relationship context:
<summary>

Long-term memory:
<summary>

Deliberately saved memories:
<summary>

Cross-conversation recall:
<summary>

Current mood:
<mood> - <reason>

Use this state to stay emotionally and biographically consistent.
Treat stored memories as soft facts. If the user corrects them, prefer the new user input.
Do not expose these notes verbatim unless the user explicitly asks what you remember.
```

Each sub-section is included only when the corresponding ability is present and the data is non-empty.

| Sub-section | Ability |
|---|---|
| Relationship context | `relationship` |
| Long-term memory | `memory` |
| Deliberately saved memories | `saved_memory` |
| Cross-conversation recall | `cross_conversation_memory` |
| Current mood | `mood` |

---

## After the System Messages

The conversation history follows as standard `user` / `assistant` / `tool` messages, built from the database records. Image parts in user messages are either sent directly (vision-capable models) or summarized via the configured `visionModel` (non-vision models).

---

## Chat Agent Path

When the agent has the `chat` ability and a Solar bridge is active, the run takes a different execution path (`runWithChatTools`). Instead of generating free-form text, the model is called with `tool_choice: forced` and must reply exclusively through tools. Plain text output is ignored.

The agent definition is also narrowed: if `chatMaxCompletionTokens` is set, it overrides `maxCompletionTokens` for the chat path.

### Chat System Prompt Additions

The Solar overlay (section 4 above) is only attached on this path. It instructs the model to use tools and provides room/sender/engagement context.

### Tool Registration

Tools are organized into **skills** — loadable bundles that add capabilities on demand. This keeps the initial tool set small and saves context tokens.

**Always loaded** (every run):
- `list_skills` — discover loadable skills
- `activate_skill` — load a skill's tools
- `sequentialthinking` — structured reasoning

**Auto-loaded** (based on agent abilities, not shown in `list_skills`):
- `chat` ability → `send_chat_message`, `send_chat_message_batch`, `no_reply`
- `humanizer` / `self_notes` ability → `list_self_notes`, `save_self_note`, `delete_self_note`

**Loadable skills** (model calls `activate_skill` to load):

| Skill | Tools | Description |
|---|---|---|
| `solar_network` | `get_chat_message`, `get_user_profile`, `list_user_posts`, `get_post`, `list_post_replies` | Look up Solar Network users, posts, profiles, and messages |
| `chat` | `send_chat_message`, `send_chat_message_batch`, `no_reply` | Send and manage messages in Solar Network chats (non-chat agents only) |
| `self_notes` | `list_self_notes`, `save_self_note`, `delete_self_note` | Remember and recall personal details (agents without humanizer only) |
| `tasks` | `create_task`, `list_tasks`, `update_task`, `delete_task` | Create and manage scheduled tasks that run automatically |
| `surfing` | `list_feed`, `search_posts`, `create_post`, `reply_to_post`, `repost_post`, `get_post`, `get_post_replies`, `list_my_posts` | Browse, search, create, reply to, and repost posts on Solar Network |

When a skill is activated, its tools become available for the rest of the run. The tool model is rebuilt with the expanded tool set.

**Example flow:**
```
Model sees: list_skills, activate_skill, sequentialthinking
Model calls: list_skills → {skills: [{name: "solar_network", ...}]}
Model calls: activate_skill(skill: "solar_network") → {ok: true, tools: [...]}
Model now sees: + get_user_profile, list_user_posts, ...
Model calls: list_user_posts(account_name: "alice")
```

Non-chat agents with tools use `runWithGeneralTools` (same tool loop but without Solar reply-mode suppression). Agents without any registered tools use plain `Generate`.

### Tool Reference

| Tool | Description | Key Parameters |
|---|---|---|
| `send_chat_message` | Send a single Solar chat reply. Required for every reply. | `room_id`, `target_account_name`, `message` (required) |
| `send_chat_message_batch` | Send multiple Solar chat messages in order. Required for multi-part replies. | `room_id`, `target_account_name`, `messages` (required, string array) |
| `no_reply` | Explicitly choose not to reply. Use instead of leaving output empty. | *(none)* |
| `get_chat_message` | Fetch a single Solar chat message by ID. Use to read replied-to or forwarded content. | `room_id` (required), `message_id` (required) |
| `get_user_profile` | Fetch a Solar user's public profile. | `account_name` or `account_id` |
| `list_user_posts` | List recent public posts by a Solar account. | `account_name` (required), `offset`, `take` |
| `get_post` | Fetch one Solar post by ID. | `post_id` (required) |
| `list_post_replies` | List replies for a Solar post. | `post_id` (required), `offset`, `take` |
| `list_self_notes` | List the agent's persistent self-notes. | `category` (optional) |
| `save_self_note` | Create or update a persistent self-note. | `key` (required), `content` (required), `category` (optional) |
| `delete_self_note` | Delete a persistent self-note by key. | `key` (required) |
| `create_task` | Create a scheduled task (one-time or repeating). | `description` (required), `schedule_type` (required: `once`/`interval`), `run_at` (for once), `interval_secs` (for interval) |
| `list_tasks` | List the agent's scheduled tasks. | *(none)* |
| `update_task` | Update a scheduled task. | `task_id` (required), `description`, `enabled`, `interval_secs`, `run_at` (all optional) |
| `delete_task` | Delete a scheduled task. | `task_id` (required) |
| `list_feed` | Browse recent posts from the Solar Network feed. | `offset`, `take`, `shuffle` (all optional) |
| `search_posts` | Search posts by keyword. | `query` (required), `offset`, `take` |
| `create_post` | Create and publish a new post. | `content` (required), `title`, `tags` (optional) |
| `reply_to_post` | Reply to an existing post. | `post_id` (required), `content` (required) |
| `repost_post` | Repost/share a post with optional comment. | `post_id` (required), `comment` (optional) |
| `react_to_post` | React to a post with an emoji. | `post_id` (required), `symbol` (default thumb_up), `attitude` (Positive/Neutral/Negative) |
| `get_post` | Get a single post by ID. | `post_id` (required) |
| `get_post_replies` | Get replies to a post. | `post_id` (required), `offset`, `take` |
| `list_my_posts` | List posts by the agent's publisher. | `offset`, `take` |
| `sequentialthinking` | Structured step-by-step reasoning tool. | *(tool-defined)* |

### Reply Mode

The reply mode determines how outbound tools are handled:

| Mode | Condition | Behavior |
|---|---|---|
| `force_allow` | Bot was mentioned (`members_mentioned` contains the bot) | `no_reply` is overridden with an error; model must use `send_chat_message`. |
| `allow` | Active engagement window | Agent decides freely between `send_chat_message` and `no_reply`. |
| `suppress` | Passive (no mention, no active window) | `send_chat_message` / `send_chat_message_batch` calls are suppressed. |

### Surf Wake

When `[personality.surfing].enabled = true`, the `SurfScheduler` periodically fetches the latest feed and triggers an autonomous run for each agent that has the `surfing` ability. The run receives the latest posts as context and can use any activated surfing tools (reply, react, repost, create post) to interact.

Config:
```toml
[personality.surfing]
enabled = true
interval = "1h"  # how often to wake
prompt = "Browse the feed and interact with interesting posts."
```

The scheduler uses the agent's first existing conversation to resolve the target account. Agents with no conversations are skipped.
