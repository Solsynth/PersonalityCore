# PersonalityCore

`PersonalityCore` is the Solar Network agent runtime service.

It provides:
- config-driven agent definitions
- REST APIs under `/api`
- SSE streaming for chat runs
- Postgres-backed conversation, message, and run persistence
- gRPC APIs for internal service-to-service usage

The current implementation is backend-only. Agents are defined on the server side through TOML, not created by clients.

## What It Does

Each agent can define:
- `id`
- `name`
- `description`
- `systemPrompt`
- `systemPromptFile`
- `model`
- model defaults such as `temperature`, `topP`, and `maxCompletionTokens`
- optional chat-specific output cap via `chatMaxCompletionTokens`
- `abilities`
- `enabled`

At runtime:
- clients list available agents
- clients create conversations bound to an `agent_id`
- clients send messages and trigger runs
- runs can be non-streaming JSON or streaming SSE

Abilities are part of the agent definition.

Current humanization-related abilities:
- `humanizer`: composite ability that enables all humanization features below
- `memory`: passive fact extraction and long-term remembered facts
- `saved_memory`: agent-owned deliberately saved memories
- `cross_conversation_memory`: recall from other recent conversations for the same user account + agent
- `mood`: rolling emotional tone
- `relationship`: familiarity and relationship posture

Chat integration ability:
- `chat`: enables Solar Network bot messaging through a configured bot account and keeps one websocket connection open per enabled integrated agent

These are server-side systems. They do not require client-side function-calling support.
For humanization, the current server behavior includes:
- passive fact extraction from user messages
- cross-conversation recall from other recent threads for the same user account + agent
- a distinct agent-owned saved-memory bucket for messages like `remember that ...`, `please remember ...`, or `don't forget ...`
- a separate agent-global self-note bucket keyed only by `agent_id`, shared across every conversation that uses the same agent

For Solar chat, humanizer state is keyed by the inbound sender's `account_id`, not the per-room synthetic conversation account. That lets impressions and memory carry across rooms and the direct run API when the same user account is involved.

The saved-memory bucket is meant to represent deliberate agentic memory, even though the current implementation still uses server-side heuristics until explicit tool-calling is added.
Agent-global self notes are different: they represent the agent's own stable identity, preferences, lore, and ongoing projects. Those notes are injected into the system prompt for every run of that agent.

## Project Layout

- [cmd/main.go](/Users/littlesheep/Documents/Projects/SolarNetwork/PersonalityCore/cmd/main.go:1): process entrypoint
- [internal/app/app.go](/Users/littlesheep/Documents/Projects/SolarNetwork/PersonalityCore/internal/app/app.go:1): runtime wiring
- [internal/config/config.go](/Users/littlesheep/Documents/Projects/SolarNetwork/PersonalityCore/internal/config/config.go:1): TOML config loading
- [internal/service/conversation.go](/Users/littlesheep/Documents/Projects/SolarNetwork/PersonalityCore/internal/service/conversation.go:1): conversation and run lifecycle
- [internal/handler/routes.go](/Users/littlesheep/Documents/Projects/SolarNetwork/PersonalityCore/internal/handler/routes.go:1): REST and SSE handlers
- [internal/grpcsvc/personality.go](/Users/littlesheep/Documents/Projects/SolarNetwork/PersonalityCore/internal/grpcsvc/personality.go:1): gRPC service

## Requirements

- Go `1.26.3`
- PostgreSQL
- an OpenAI-compatible chat endpoint

Optional:
- Solar auth gRPC service via `auth.target`

## Config

Start from [config.example.toml](/Users/littlesheep/Documents/Projects/SolarNetwork/PersonalityCore/config.example.toml:1).

Important sections:
- `database.dsn`
- `http.port`
- `grpc.port`
- `auth.target`
- `solarNetwork.baseUrl`
- `providersDir`
- `providers`
- `agents.items`
- `agents.dir`

Models are referenced by agents in `<provider>/<model>` form, for example:
- `openai/gpt-4.1-mini`
- `azure/gpt-4.1`

You can define agents in two ways:

1. Inline in the main config:

```toml
[[agents.items]]
id = "support"
name = "Support"
description = "General support assistant"
systemPrompt = "You are the Solar Network support assistant."
model = "openai/gpt-4.1-mini"
abilities = []
enabled = true
```

If you want the prompt in a separate file:

```toml
[[agents.items]]
id = "support"
name = "Support"
description = "General support assistant"
systemPromptFile = "./prompts/support.md"
model = "openai/gpt-4.1-mini"
abilities = []
enabled = true
```

2. Split across multiple TOML files in `agents.dir`:

```toml
[agents]
dir = "./agents.d"
```

Example extra file:

```toml
[agents]
[[agents.items]]
id = "writer"
name = "Writer"
description = "Writing assistant"
systemPromptFile = "../prompts/writer.md"
model = "openai/gpt-4.1-mini"
abilities = []
enabled = true
```

The service merges inline agents and `agents.dir/*.toml` at startup.
`systemPromptFile` is resolved relative to the config file that declares the agent, so split agent files can safely point at nearby prompt files.

Agents with `abilities = ["chat"]` also require a Solar bot integration block:

```toml
[solarNetwork]
baseUrl = "https://api.dyson.example"

[[agents.items]]
id = "support-bot"
name = "Support Bot"
description = "Replies in Solar chat as a bot"
systemPrompt = "You are the Solar support bot."
model = "openai/gpt-4.1-mini"
abilities = ["chat"]
enabled = true

[agents.items.solar-network-integration]
accountName = "support-bot"
accessToken = "..."
```

The integration block is server-only: public HTTP and gRPC agent metadata expose `abilities`, but never return the bot credentials.
Each enabled integrated agent maintains one websocket connection to `{solarNetwork.baseUrl}/ws`.
When a chat-linked Solar conversation is active, outbound remote messages should be sent by `send_chat_message` or `send_chat_message_batch`; `NO_REPLY` is the explicit silence token, and plain assistant text is forwarded as a fallback when the model skips tool calling.
Chat tool-calling also exposes `list_self_notes`, `save_self_note`, and `delete_self_note` so an agent can inspect and update its own persistent identity notes shared across all conversations.
Inbound Solar chat image attachments are passed to the model as multimodal image inputs using `{solarNetwork.baseUrl}/drive/files/{file_id}`.
When the agent replies in plain assistant text for a Solar chat conversation, each non-empty newline-delimited line is sent as a separate outbound chat message. In streaming mode, completed lines are sent immediately when the newline arrives.
For live inbound handling, a direct mention or reply to the bot opens a 5-minute active follow-up window so the bot can continue the current group-chat exchange.
Every run also appends explicit message timestamps in context and a final `Current date and time:` system message so the model can reason about chronology without depending on cache-retained earlier prompt sections.
When a thread grows beyond the live history window, older messages are automatically compacted into a persisted thread summary that is injected back into future runs as `Earlier compacted thread context:`.

Agents can also opt into `autonomous`:

```toml
[[agents.items]]
id = "support-bot"
name = "Support Bot"
description = "Replies in Solar chat and can proactively follow up"
systemPrompt = "You are the Solar support bot."
model = "openai/gpt-4.1-mini"
chatMaxCompletionTokens = 160
abilities = ["chat", "autonomous"]
enabled = true

[agents.items.solar-network-integration]
accountName = "support-bot"
accessToken = "..."

[agents.items.autonomous]
wakeInterval = "10m"
wakePrompt = "Check active rooms and DMs for proactive follow-up opportunities."
```

`autonomous` enables server-side agent-initiated runs. The current implementation supports:
- manual wake triggers through `POST /api/agents/:id/autonomous-runs`
- periodic wake-ups for agents with `autonomous.wakeInterval` set
- synthetic autonomous wake request messages persisted with `role = "system"` and metadata `source = "autonomous"`
- trusted outbound conversation starts through `POST /api/internal/agents/:id/start-conversation` with header `X-Autonomous-Secret`

If chat replies are too verbose, set a lower `chatMaxCompletionTokens` on that agent. This overrides `maxCompletionTokens` only for Solar/chat-style execution paths and leaves ordinary non-chat runs unchanged.

Current boundaries:
- periodic wakes currently target existing Solar-bound conversations only
- DM bindings are always eligible for periodic wakes
- periodic pickup of old group-chat messages is suppressed unless the latest inbound group message directly mentioned or replied to the bot
- if you want proactive Solar outreach, combine `autonomous` with `chat`
- lookup-only tool calls no longer terminate the Solar tool loop early; the model can inspect posts or profiles first, then decide whether to send a message

Example trusted start request:

```bash
curl -X POST http://127.0.0.1:8090/api/internal/agents/support-bot/start-conversation \
  -H 'Content-Type: application/json' \
  -H 'X-Autonomous-Secret: YOUR_SECRET' \
  -d '{"target_account_name":"alice","prompt":"Say hi and ask how her project is going."}'
```

If you already know the Solar account ID, you can also send `target_account_id` directly.

The TUI binary can call the same endpoint in one-shot mode:

```bash
go run ./cmd/tui \
  -base-url http://127.0.0.1:8090 \
  -agent-id support-bot \
  -autonomous-secret YOUR_SECRET \
  -start-user alice \
  -start-prompt "Say hi and ask how her project is going."
```

Providers can also be defined in two ways.

1. Inline in the main config:

```toml
providersDir = "./models.d"

[[providers]]
id = "openai"
type = "openai"
apiKey = "..."
baseUrl = ""
byAzure = false
apiVersion = ""
timeout = "90s"
maxCompletionTokens = 2048
temperature = 0.7
topP = 1.0
```

2. Split across multiple files in `providersDir`, for example `./models.d/azure.toml`:

```toml
[[providers]]
id = "azure"
type = "openai"
apiKey = "..."
baseUrl = "https://YOUR-RESOURCE.openai.azure.com"
byAzure = true
apiVersion = "2024-06-01"
timeout = "90s"
maxCompletionTokens = 2048
temperature = 0.7
topP = 1.0
```

The service merges inline providers and `providersDir/*.toml` at startup.

Example separated layout:

```text
config.toml
agents.d/
  support.toml
  writer.toml
models.d/
  openai.toml
  azure.toml
prompts/
  support.md
  writer.md
```

Example `config.toml`:

```toml
providersDir = "./models.d"

[agents]
dir = "./agents.d"

[auth]
offline = true
offlineAccountId = "local-dev"
autonomousSecret = ""
```

Example `agents.d/support.toml`:

```toml
[agents]
[[agents.items]]
id = "support"
name = "Support"
description = "General support assistant"
systemPromptFile = "../prompts/support.md"
model = "openai/gpt-4.1-mini"
abilities = []
enabled = true
```

Example with humanization scopes enabled:

```toml
[agents]
[[agents.items]]
id = "michan"
name = "Michan"
description = "A more person-like companion agent"
systemPromptFile = "../prompts/michan.md"
model = "deepseek/deepseek-v4-flash"
abilities = ["humanizer"]
enabled = true
```

Example `agents.d/writer.toml`:

```toml
[agents]
[[agents.items]]
id = "writer"
name = "Writer"
description = "Writing assistant"
systemPromptFile = "../prompts/writer.md"
model = "azure/gpt-4.1"
abilities = []
enabled = true
```

Example `models.d/openai.toml`:

```toml
[[providers]]
id = "openai"
type = "openai"
apiKey = "YOUR_OPENAI_KEY"
baseUrl = ""
byAzure = false
apiVersion = ""
timeout = "90s"
maxCompletionTokens = 2048
temperature = 0.7
topP = 1.0
```

Example `models.d/azure.toml`:

```toml
[[providers]]
id = "azure"
type = "openai"
apiKey = "YOUR_AZURE_OPENAI_KEY"
baseUrl = "https://YOUR-RESOURCE.openai.azure.com"
byAzure = true
apiVersion = "2024-06-01"
timeout = "90s"
maxCompletionTokens = 2048
temperature = 0.7
topP = 1.0
```

Example `prompts/support.md`:

```md
You are the Solar Network support assistant.
Answer clearly and keep replies operational.
```

Startup fails when:
- no enabled agents exist
- an agent id is duplicated
- required fields like `id` or `name` are missing
- no providers exist
- a provider id is duplicated

## Auth

HTTP routes require an account identity.

Supported modes:
- offline mode: set `auth.offline = true` to skip Solar auth entirely for local testing
- production mode: configure `auth.target` to use Solar auth via `src.solsynth.dev/sosys/go/pkg/auth`
- local/dev mode: if `auth.allowDevIds = true`, send `X-Account-Id: your-account-id`

Offline mode behavior:
- no auth token is required
- every request uses the same `auth.offlineAccountId`
- this is meant to simulate one fixed local user across the whole service instance

Example offline config:

```toml
[auth]
offline = true
offlineAccountId = "local-dev"
```

Solar Network note:
- Solar gRPC commonly uses self-signed TLS certificates
- when dialing Solar auth over TLS, set `auth.useTLS = true` and `auth.tlsSkipVerify = true`

Example Solar auth config:

```toml
[auth]
target = "grpcs://padlock:7003"
useTLS = true
tlsSkipVerify = true
offline = false
```

Example dev request header:

```http
X-Account-Id: user-123
```

## Run

```bash
go run ./cmd --config ./config.toml
```

Useful flags:
- `--config ./config.toml`
- `--pretty`

Useful environment variables:
- `CONFIG_PATH`
- `ZEROLOG_PRETTY=true`
- `LOG_LEVEL=debug`
- `DATABASE_DSN`

Generation logging:
- `LOG_LEVEL=info`: run creation, generation start, completion, and failure
- `LOG_LEVEL=debug`: model preparation, humanizer overlay injection, model invocation, and stream chunk summary

Example:

```bash
LOG_LEVEL=debug go run ./cmd --config ./config.toml --pretty
```

For fully local testing, a common setup is:

```toml
[auth]
offline = true

providersDir = "./models.d"

[[providers]]
id = "openai"
type = "openai"
apiKey = "..."

[[agents.items]]
id = "support"
name = "Support"
systemPrompt = "You are a helpful assistant."
model = "openai/gpt-4.1-mini"
```

## Local TUI Client

There is also a minimal terminal client for local testing.

It is designed for the offline mock-user mode:

```toml
[auth]
offline = true
offlineAccountId = "local-dev"
```

Start the server:

```bash
go run ./cmd --config ./config.toml
```

Then open the TUI in another terminal:

```bash
go run ./cmd/tui --base-url http://127.0.0.1:8090
```

Useful flags:
- `--base-url http://127.0.0.1:8090`
- `--agent-id support`
- `--stream=true`
- `--account-id user-123`

The `--account-id` flag is only useful when you are not using offline mode and want to send `X-Account-Id` in local dev mode.

Controls:
- `Enter`: send the current message
- `Ctrl+N`: create a new conversation for the current agent
- `Tab` / `Shift+Tab`: switch agents and start a fresh conversation
- `Ctrl+S`: toggle streaming SSE vs non-streaming JSON runs
- `Q`: quit

The TUI will:
- load enabled agents from `/api/agents`
- create a conversation automatically on startup
- append replies live when streaming is enabled

## HTTP API

All endpoints live under `/api`.

### Agents

- `GET /api/agents`
- `GET /api/agents/:id`

Example:

```bash
curl http://localhost:8090/api/agents \
  -H 'X-Account-Id: user-123'
```

### Conversations

- `POST /api/conversations`
- `GET /api/conversations?take=20&offset=0`
- `GET /api/conversations/:id`
- `GET /api/conversations/:id/messages?take=20&offset=0`
- `POST /api/conversations/:id/messages`
- `POST /api/conversations/:id/runs`
- `GET /api/conversations/:id/runs?take=20&offset=0`
- `GET /api/conversations/:id/runs/:runId`

List endpoints follow Solar pagination style:
- request: `take`, `offset`
- response header: `X-Total`

### Create a conversation

```bash
curl -X POST http://localhost:8090/api/conversations \
  -H 'Content-Type: application/json' \
  -H 'X-Account-Id: user-123' \
  -d '{
    "agent_id": "support",
    "title": "Support session"
  }'
```

### Append a user message without running the model

```bash
curl -X POST http://localhost:8090/api/conversations/CONVERSATION_ID/messages \
  -H 'Content-Type: application/json' \
  -H 'X-Account-Id: user-123' \
  -d '{
    "content": "I need help with my account"
  }'
```

### Non-streaming run

```bash
curl -X POST http://localhost:8090/api/conversations/CONVERSATION_ID/runs \
  -H 'Content-Type: application/json' \
  -H 'X-Account-Id: user-123' \
  -d '{
    "message": "Summarize the issue and suggest next steps",
    "stream": false
  }'
```

### Vision / multimodal run

`POST /api/conversations/:id/runs` also accepts `input_parts` for multimodal user input.
Use `message` for the main text prompt, then append image parts by URL or base64.

Models declare which modalities they support via `modalities` in their provider model config. When a model supports `image`, image parts are sent directly. When it does not, PersonalityCore automatically summarizes images using the app-wide `visionModel` and injects the summary as text. Summaries are cached in the database.

```bash
curl -X POST http://localhost:8090/api/conversations/CONVERSATION_ID/runs \
  -H 'Content-Type: application/json' \
  -H 'X-Account-Id: user-123' \
  -d '{
    "message": "What is happening in this image?",
    "input_parts": [
      {
        "type": "image_url",
        "image_url": "https://example.com/photo.jpg",
        "detail": "high"
      }
    ],
    "stream": false
  }'
```

Base64 uploads are also supported:

```json
{
  "message": "Read the chart in this screenshot",
  "input_parts": [
    {
      "type": "image_url",
      "image_base64": "iVBORw0KGgoAAAANSUhEUgAA...",
      "mime_type": "image/png",
      "detail": "low"
    }
  ]
}
```

Example provider config with per-model modalities and embedding model typing:

```toml
[[providers]]
id = "openai"
type = "openai"
apiKey = "..."

  [[providers.models]]
  name = "gpt-4o"
  modalities = ["image", "audio", "video"]

  [[providers.models]]
  name = "gpt-4.1-mini"
  modalities = ["image"]

  [[providers.models]]
  name = "gpt-3.5-turbo"
  # no modalities — treated as text-only, images summarized via visionModel

  [[providers.models]]
  name = "text-embedding-3-small"
  type = "embedding"
  # embedding models are reserved for embedding RPCs
```

To enable image summarization for non-vision models, set `visionModel` under `[personality]`:

```toml
[personality]
visionModel = "openai/gpt-4.1-mini"
defaultEmbeddingModel = "openai/text-embedding-3-small"
```

If no `visionModel` is configured, image parts for non-vision models are replaced with a placeholder.

### Streaming run over SSE

```bash
curl -N -X POST http://localhost:8090/api/conversations/CONVERSATION_ID/runs \
  -H 'Content-Type: application/json' \
  -H 'X-Account-Id: user-123' \
  -d '{
    "message": "Explain this step by step",
    "stream": true
  }'
```

SSE events currently emitted:
- `run.started`
- `message.delta`
- `message.completed`
- `run.completed`
- `run.failed`
- `heartbeat`

## gRPC API

The shared protobuf contract lives in:
- [../Spec/proto/personality.proto](/Users/littlesheep/Documents/Projects/SolarNetwork/Spec/proto/personality.proto:1)

Generated Go bindings live in:
- [../Golaunch/proto/personality.pb.go](/Users/littlesheep/Documents/Projects/SolarNetwork/Golaunch/proto/personality.pb.go:1)
- [../Golaunch/proto/personality_grpc.pb.go](/Users/littlesheep/Documents/Projects/SolarNetwork/Golaunch/proto/personality_grpc.pb.go:1)

Implemented personality RPCs:
- `ListAgents`
- `GetAgent`
- `RunConversation`
- `Complete`

Implemented embedding RPCs:
- `GenerateEmbedding`
- `GenerateEmbeddings`

Embedding RPC behavior:
- single-text and batch generation are both supported
- set the default model with `personality.defaultEmbeddingModel`
- override per-call settings with gRPC metadata headers `x-embedding-model` and `x-embedding-dimensions`
- only provider models marked with `type = "embedding"` are allowed for embedding calls; all others are treated as completion/chat models

`RunConversation` behavior:
- if `conversation_id` is empty, the service creates a new conversation using `agent_id`
- if `conversation_id` is present, the run continues that conversation
- current gRPC execution is unary only

## Persistence Model

The service persists:
- conversation threads
- conversation messages
- conversation runs

Threads are owned by `account_id`, and every access is scoped to that owner.

Current behavior:
- a conversation is permanently bound to one `agent_id`
- user and assistant messages are stored
- the final run result is stored
- token-by-token chunks are streamed live but not individually persisted

## Notes

- The current model adapter uses `github.com/cloudwego/eino-ext/components/model/openai`.
- Multiple providers are now resolved from `[[providers]]` plus `providersDir/*.toml`.
- Provider type support is currently implemented for OpenAI-compatible backends via `type = "openai"` or `type = "openai-compatible"`.
- `abilities` are the primary agent capability field.
- `abilities` is the canonical agent capability field.
- `autonomous` is an initiation ability: it lets the server wake an agent without a fresh user message.
- The gRPC service is intended for internal Solar usage; the primary client API is REST + SSE.

## Verification

Current verification command:

```bash
go test ./...
```
