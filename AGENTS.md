# AGENTS.md

This file guides future AI agents working in `PersonalityCore`.

## Project Purpose

`PersonalityCore` is a Solar Network backend service for agentic chat.

It currently provides:
- config-driven agent definitions
- REST APIs for conversations
- SSE streaming for chat runs
- gRPC APIs for internal callers
- Postgres persistence for conversations, messages, and runs
- Eino-based model execution

Agents are server-defined. Clients do not create or edit agents in v1.

## Repo Shape

- `cmd/main.go`: service entrypoint
- `cmd/tui/main.go`: local terminal test client
- `internal/app`: runtime wiring for HTTP, gRPC, DB, registry, and executor
- `internal/config`: TOML loading, defaults, `agents.dir`, `providersDir`, and prompt-file resolution
- `internal/agent`: agent registry and model provider execution
- `internal/service`: conversation and run lifecycle
- `internal/server`: Gin router and auth middleware
- `internal/handler`: HTTP and SSE handlers
- `internal/grpcsvc`: gRPC service implementation
- `internal/database`: GORM models and DB setup
- `internal/tui`: local HTTP/SSE TUI client

## Architecture Rules

Follow these boundaries:

- Keep HTTP request/response handling in `internal/handler`.
- Keep business logic in `internal/service`.
- Keep provider/model wiring in `internal/agent`.
- Keep config parsing and file-resolution logic in `internal/config`.
- Keep auth concerns in `internal/server` and `internal/identity`.
- Do not let handlers talk to GORM directly.

If adding a feature, prefer extending `service.ConversationService` first, then expose it via HTTP and gRPC.

## Agent Configuration

Agents are data-driven through TOML.

Supported patterns:
- inline `[[agents.items]]` in the main config
- split files under `agents.dir`
- inline `systemPrompt`
- file-backed `systemPromptFile`
- primary capability field `abilities`
- legacy compatibility alias `toolScopes`

Important behavior:
- `systemPromptFile` is resolved relative to the config file that declared that agent
- agent `model` must use `<provider>/<model>` format
- only enabled agents are exposed
- `humanizer` is a composite ability that enables `memory`, `saved_memory`, `cross_conversation_memory`, `mood`, and `relationship`

When changing agent config behavior:
- preserve backward compatibility for inline `systemPrompt`
- preserve backward compatibility for legacy `toolScopes`
- keep relative-path behavior deterministic
- add config tests for both main-config and split-file cases

## Provider Configuration

Providers are loaded from:
- inline `[[providers]]`
- split files under `providersDir`

Current supported provider types:
- `openai`
- `openai-compatible`

Azure-style OpenAI is supported through provider fields, not a separate provider type:
- `byAzure = true`
- `baseUrl`
- `apiVersion`

If adding a new provider type:
- implement it in `internal/agent/executor.go`
- validate provider config at executor creation
- keep the agent-facing model reference format as `<provider>/<model>`
- add tests for provider resolution and failure cases

## API Conventions

HTTP:
- base path is `/api`
- list endpoints use `take` and `offset`
- list endpoints return `X-Total`
- conversation CRUD is REST-style
- chat execution is `POST /api/conversations/:id/runs`
- SSE event names are part of the contract; do not rename casually

SSE events currently used:
- `run.started`
- `message.delta`
- `message.completed`
- `run.completed`
- `run.failed`
- `heartbeat`

gRPC:
- gRPC service is registered in `internal/app/app.go`
- protobuf source belongs in sibling repo `../Spec`
- generated Go bindings belong in sibling repo `../Golaunch`

If you change protobuf:
1. Edit `../Spec/proto/*.proto`
2. Commit and push `../Spec`
3. Run generation from `../Golaunch`
4. Update `PersonalityCore` to consume the generated bindings

Do not edit generated files in `../Golaunch/proto` manually.

## Auth And Local Development

HTTP auth modes:
- offline mode: every request becomes one configured mock user
- Solar auth mode: shared gRPC auth client
- local dev header mode: `X-Account-Id` if enabled

Important behavior:
- offline mode is intended for local testing and TUI use
- all offline requests must map to the same configured mock account
- Solar auth may use self-signed TLS, so `tlsSkipVerify` support matters

Do not break offline mode when changing middleware.

## Persistence Rules

Main tables:
- `conversation_threads`
- `conversation_messages`
- `conversation_runs`

Current expectations:
- every record is scoped to `account_id`
- every thread is bound to one `agent_id`
- final assistant messages are persisted
- token-by-token stream chunks are not persisted

If changing persistence:
- preserve account scoping checks
- avoid storing partial SSE chunks unless there is a strong product reason
- update GORM models and tests together

## TUI Client

The TUI is a local test tool, not a production UI.

Expectations:
- it should keep working with offline mode
- it should exercise the real REST + SSE APIs
- keep it minimal and operational

Avoid coupling server internals into the TUI. It should behave like an external client.

## Logging

Generation lifecycle logs live in `internal/service/conversation.go`.

Current expectations:
- `info` level should show run creation, generation start, completion, and failure
- `debug` level should show model preparation, humanizer overlay injection, and stream/chunk details
- if you add a major generation phase, add a log at the service layer rather than scattering logs in handlers

## Testing Expectations

Before finishing work, run:

```bash
go test ./...
```

Add or update tests when changing:
- config loading
- auth behavior
- provider resolution
- SSE parsing
- ownership and conversation access rules

## Editing Guidance

When making changes:
- prefer small, layered edits over broad rewrites
- preserve current file responsibilities
- keep JSON field names and event names stable unless a contract change is intended
- update `README.md` when config shape or developer workflow changes

## Common Safe Extensions

Good next-step areas:
- agent-scoped tool execution
- richer run metadata and usage tracking
- more provider backends
- better run/retry inspection endpoints
- more robust conversation querying

Higher-risk areas that need extra care:
- auth middleware
- protobuf contracts
- config loading semantics
- SSE event contract
- model/provider resolution

## When Unsure

Prefer these references in order:
1. current code in this repo
2. `README.md`
3. sibling Solar repos such as `../FileSystem` for service conventions
4. sibling repos `../Spec` and `../Golaunch` for protobuf workflow

If a change affects public API or cross-repo contracts, treat it as a compatibility change and update documentation in the same pass.
