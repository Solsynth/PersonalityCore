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
- `model`
- model defaults such as `temperature`, `topP`, and `maxCompletionTokens`
- `toolScopes`
- `enabled`

At runtime:
- clients list available agents
- clients create conversations bound to an `agent_id`
- clients send messages and trigger runs
- runs can be non-streaming JSON or streaming SSE

Tool scope is already part of the agent definition, but actual tool execution is not enabled yet.

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
- `llm.apiKey`
- `llm.baseUrl`
- `llm.model`
- `agents.items`
- `agents.dir`

You can define agents in two ways:

1. Inline in the main config:

```toml
[[agents.items]]
id = "support"
name = "Support"
description = "General support assistant"
systemPrompt = "You are the Solar Network support assistant."
model = "gpt-4.1-mini"
toolScopes = []
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
systemPrompt = "You help users draft clean copy."
model = "gpt-4.1-mini"
toolScopes = []
enabled = true
```

The service merges inline agents and `agents.dir/*.toml` at startup.

Startup fails when:
- no enabled agents exist
- an agent id is duplicated
- required fields like `id` or `name` are missing

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
- `OPENAI_API_KEY`
- `OPENAI_BASE_URL`
- `OPENAI_MODEL`
- `DATABASE_DSN`

For fully local testing, a common setup is:

```toml
[auth]
offline = true

[llm]
apiKey = "..."
```

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

Implemented RPCs:
- `ListAgents`
- `GetAgent`
- `RunConversation`

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
- `toolScopes` are stored on agent definitions now for future rollout, but no tool calling is performed yet.
- The gRPC service is intended for internal Solar usage; the primary client API is REST + SSE.

## Verification

Current verification command:

```bash
go test ./...
```
