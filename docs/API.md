# PersonalityCore API Reference

Base URL: `<Solar Network Base URL>/personality`

When using with the gateway, no need to add `/api` prefix, only the `/personality` is enough.

All endpoints require Solar authentication unless noted otherwise. Auth is handled via one of:

- **Solar auth**: Bearer token validated against the configured auth service.
- **Offline mode**: Every request maps to a single configured mock account (local dev).
- **Dev header mode**: `X-Account-Id` header (when `allowDevIDs` is enabled).

The OpenAI-compatible completion endpoint additionally accepts an AI-only `sat_...`
credential. It cannot authenticate any other endpoint and does not grant Solar
Network account access.

---

## Native stateful responses

```
POST /api/responses
```

This is the native PersonalityCore generation endpoint. It is independent of
the OpenAI compatibility endpoint and persists each turn as a conversation run.
The gateway form is `POST /responses`.

**Request body**

```json
{
  "agent_id": "assistant",
  "input": "Hello"
}
```

The first request creates a conversation. Continue it by sending the returned
response `id` as `previous_response_id`:

```json
{
  "previous_response_id": "01JF...",
  "input": "What did I just say?"
}
```

`conversation_id` may be supplied instead when the caller stores the
conversation handle. `input` may be a string or an array of text messages.
`instructions` is optional and is included with the current turn.

Client-owned function tools use the flat Responses-style shape:

```json
{
  "tools": [{
    "type": "function",
    "name": "lookup_weather",
    "description": "Look up weather.",
    "parameters": {
      "type": "object",
      "properties": {"city": {"type": "string"}},
      "required": ["city"]
    }
  }]
}
```

PersonalityCore executes configured server tools itself. If the model requests
a client tool, the response has `status: "requires_action"` and function-call
items in `output`:

```json
{
  "id": "01JF...",
  "status": "requires_action",
  "output": [{
    "type": "function_call",
    "call_id": "call_1",
    "name": "lookup_weather",
    "arguments": "{\"city\":\"Taipei\"}"
  }]
}
```

The client executes those calls and continues with the same response ID:

```json
{
  "previous_response_id": "01JF...",
  "tool_outputs": [{
    "call_id": "call_1",
    "name": "lookup_weather",
    "output": {"temperature": 22}
  }]
}
```

Server and client tools can be supplied together. Server tool calls execute
before a client tool call is returned.

**Response**

```json
{
  "id": "01JF...",
  "object": "personality.response",
  "status": "completed",
  "agent_id": "assistant",
  "conversation_id": "01JG...",
  "model": "openai/gpt-4o",
  "output_text": "You said hello.",
  "output": [
    {
      "type": "message",
      "role": "assistant",
      "content": [{"type": "output_text", "text": "You said hello."}]
    }
  ]
}
```

---

## OpenAI-compatible chat completions

```
POST /v1/chat/completions
```

`/api/v1/chat/completions` is also available when the service is behind an
API-prefix-only route. This endpoint is stateless: it does **not** create
conversation, message, or run records. Supply the complete history again on
each request, including assistant tool calls and tool results.

`model` selects the agent: use `agent` for its default configured model, or
`agent/provider/model` to select another provider model available to that
agent, for example `assistant/openai/gpt-4.1-mini`. The legacy Solar extension
`agent_id` remains supported; when used, `model` is `provider/model`.
The agent's configured system prompt is always prepended. `messages`, `tools`,
and tool-result messages follow the OpenAI Chat Completions format.

`raw` is a reserved virtual agent for transparent upstream access. Use
`raw/provider/model`, for example `raw/openai/gpt-4.1-mini`. It does not load
an agent configuration, system prompt, or any server-side tools; client-provided
OpenAI tools are still passed through to the upstream model.

```json
{
  "model": "assistant/openai/gpt-4.1-mini",
  "messages": [{"role": "user", "content": "What's the weather?"}],
  "tools": [{
    "type": "function",
    "function": {
      "name": "get_weather",
      "description": "Get the local weather",
      "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}
    }
  }]
}
```

Tools passed in `tools` are client-owned: a returned `tool_calls` response must
be executed by the client and the resulting `role: "tool"` message submitted
in the next request. Set `server_tools: true` to also expose the selected
agent's built-in tools. Built-in tools execute on the server and their results
are fed back to the model before a response is returned. Client tool names may
not duplicate server tool names. Server tool side effects (such as creating a
task) are intentional; only chat history persistence is disabled.

`stream: true` uses OpenAI SSE framing and ends with `data: [DONE]`. The
current compatibility layer emits one terminal completion chunk per request.

### AI-only credentials

Create and manage AI-only credentials with normal Solar authentication:

```
POST /api/openai/credentials
GET /api/openai/credentials
DELETE /api/openai/credentials/:id
```

The service returns the raw `sat_...` token only from `POST`; retain it when
created because it cannot be retrieved later. The database retains only its
SHA-256 hash. `DELETE` permanently revokes the credential.

#### Create an AI-only credential

```json
{
  "name": "Local coding client",
  "agent_ids": ["assistant"],
  "providers": ["openai"],
  "models": ["openai/gpt-4.1-mini"],
  "usage_limit": "5",
  "usage_currency": "golds"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Human-readable credential name; maximum 128 characters. |
| `agent_ids` | string array | no | Allowed enabled agent IDs. Empty or omitted allows every enabled agent and `raw`. |
| `providers` | string array | no | Allowed configured provider IDs. Empty or omitted allows every provider. |
| `models` | string array | no | Allowed configured `provider/model` references. Empty or omitted allows every model. |
| `usage_limit` | string | yes | Non-negative decimal cap in `usage_currency`; `"0"` is unlimited. |
| `usage_currency` | string | no | Currency for the cap; defaults to the configured billing currency. It must match every selected model's pricing currency. |

All specified agents, providers, and models are validated when the credential is
created. A model must be explicitly configured under its provider.

**Response** `201 Created`

```json
{
  "credential": {
    "id": "01JF...",
    "name": "Local coding client",
    "token_prefix": "sat_a1b2c3d4...",
    "agent_ids": ["assistant"],
    "providers": ["openai"],
    "models": ["openai/gpt-4.1-mini"],
    "usage_limit": "5.00000000",
    "usage_used": "0",
    "usage_currency": "golds",
    "enabled": true,
    "created_at": "2026-08-01T00:00:00Z",
    "updated_at": "2026-08-01T00:00:00Z"
  },
  "token": "sat_..."
}
```

`GET` returns `{ "data": [credential, ...] }` using the credential metadata
above, without the raw token or token hash. `DELETE` returns `204 No Content`;
it returns `404 Not Found` when the credential is not owned by the caller or is
already revoked.

#### Use an AI-only credential

Send the credential only to a stateless completion endpoint:

```bash
curl -X POST "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer sat_..." \
  -H "Content-Type: application/json" \
  -d '{"model":"assistant/openai/gpt-4.1-mini","messages":[{"role":"user","content":"Hello"}]}'
```

AI-only credentials work with `POST /v1/chat/completions` and
`POST /api/v1/chat/completions` only. They always disable `server_tools`, even
when the request sets it to `true`; client-provided OpenAI function tools remain
available. The credential's agent, provider, and model allowlists are checked
against the resolved completion target.

Usage is calculated from configured per-million-token model pricing after the
provider responds. An already exhausted credential is rejected before another
provider request. When a response crosses the cap, that response is recorded
and the API returns `402 Payment Required`; later requests are rejected before
generation. An unpriced model records zero usage.

For credential-authenticated completion failures, the endpoint returns the
OpenAI-compatible error object:

```json
{
  "error": {
    "message": "AI credential usage limit exceeded",
    "type": "invalid_request_error"
  }
}
```
---

## Agents

## Models

### List configured models

```
GET /api/models
```

Returns every model explicitly configured under `[[providers.models]]`, without
filtering out models blocked by the caller's perk level. `perk_overrides`
allows clients to show those restrictions before attempting a request.

When `[personality].onlyAllowListedModels = true`, model execution is limited
to this list; a valid provider/model reference that is not listed is rejected.

### List agents

```
GET /api/agents
```

Returns all enabled agents.

**Response** `200 OK`

```json
[
  {
    "id": "assistant",
    "name": "Assistant",
    "description": "A helpful assistant",
    "model": "openai/gpt-4o",
    "abilities": ["chat"],
    "system_prompt": "...",
    "enabled": true
  }
]
```

### Get agent

```
GET /api/agents/:id
```

**Response** `200 OK` — same shape as a single agent object above.
**Response** `404 Not Found` — agent does not exist or is disabled.

---

## Conversations

### Create conversation

```
POST /api/conversations
```

**Request body**

```json
{
  "agent_id": "assistant",
  "title": "Optional title"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `agent_id` | string | yes | Must match an enabled agent ID. |
| `title` | string | no | Defaults to `"New conversation"`. |

**Response** `201 Created`

```json
{
  "id": "01JF...",
  "account_id": "...",
  "agent_id": "assistant",
  "title": "Optional title",
  "created_at": "2025-01-01T00:00:00Z",
  "updated_at": "2025-01-01T00:00:00Z"
}
```

### List conversations

```
GET /api/conversations?take=20&offset=0
```

| Query param | Default | Max | Description |
|-------------|---------|-----|-------------|
| `take` | 20 | 200 | Page size. |
| `offset` | 0 | — | Pagination offset. |

**Response** `200 OK` — array of conversation objects.
**Header** `X-Total` — total count of conversations for the account.

### Get conversation

```
GET /api/conversations/:id
```

**Response** `200 OK` — conversation object.
**Response** `403 Forbidden` — conversation belongs to a different account.
**Response** `404 Not Found` — conversation does not exist.

---

## Messages

### Add message

```
POST /api/conversations/:id/messages
```

**Request body**

```json
{
  "content": "Hello, how are you?",
  "attachment_ids": ["abc123", "def456"]
}
```

**Response** `201 Created`

```json
{
  "id": "01JF...",
  "thread_id": "01JF...",
  "role": "user",
  "content": "Hello, how are you?",
  "sequence": 1,
  "created_at": "2025-01-01T00:00:00Z"
}
```

### List messages

```
GET /api/conversations/:id/messages?take=20&offset=0
```

**Response** `200 OK` — array of message objects ordered by `sequence ASC`.
**Header** `X-Total` — total message count.

---

## Billing

Billing is optional and is enabled by the service operator. Model prices use a
Wallet currency per 1M input/output tokens. Set the optional `currency` in a
model's `pricing` section (for example, `"golds"` or `"points"`); omitted
currency inherits `[billing].currency`. A model with no `pricing` configuration
is free, but hourly and daily run limits still apply. A blacklisted account
cannot use any Personality model, including free models.

Before a priced model runs, Personality checks that the account has a Wallet
payment wallet. The result (including a missing wallet) is cached for 10
minutes. Free models do not require a payment wallet.

Usage is settled daily at 00:00 UTC. A spending quota is the maximum unpaid
gold amount before an immediate Wallet transaction is attempted. Set it to
`"0"` to use daily settlement only. If an immediate or daily transaction fails,
the account is blacklisted.

Wallet transactions support two decimal places. Personality truncates a charge
to two decimals and retains any smaller remainder in the account's unpaid usage
ledger for a later settlement.

`[billing].payeeAccountId` is optional. When unset, Personality sends a null
`payee_account_id` to Wallet, allowing Wallet to select its default/system
payee.

The optional `[billing].serviceFeePercentage` is a non-negative decimal
percentage added after the model price and agent multiplier. For example,
`"5"` makes a 10-point model charge cost 10.5 points before Wallet's
two-decimal truncation.

### Get my billing policy

```
GET /api/billing/me
```

Returns account-visible limits and the self-managed spending quota. `null`
limits inherit the configured service default.

**Response** `200 OK`

```json
{
  "hourly_run_limit": null,
  "daily_run_limit": null,
  "spending_quota": "20",
  "blacklisted": false,
  "usage": {
    "hourly_runs": {"used": 4, "max": 30},
    "daily_runs": {"used": 18, "max": null}
  }
}
```

`usage.hourly_usage` and `usage.daily_usage` contain current amount usage and
the resolved maximum for each configured currency. A `null` `max` is unlimited.

### Set my spending quota

```
PUT /api/billing/me/spending-quota
```

Only the authenticated account's spending quota is changed. It cannot change
the administrator-managed hourly/daily run thresholds or blacklist status.

**Request body**

```json
{
  "spending_quota": "20"
}
```

`spending_quota` is a non-negative decimal gold amount. Use `"0"` to disable
immediate settlement for this account and settle only during the daily UTC job.

### Settle billing now

```
POST /api/billing/me/settle
```

Immediately settles every unpaid usage balance for the authenticated account.
This endpoint remains available to blacklisted accounts: a successful settlement
automatically clears their billing blacklist. The daily settlement job skips
blacklisted accounts, so it never repeatedly retries a known failing payment.

### Admin account policy

All admin billing endpoints require a Solar superuser or the Padlock permission
`personality.billing.manage`.

```
GET /api/admin/billing/accounts/:accountId
PUT /api/admin/billing/accounts/:accountId
POST /api/admin/billing/accounts/:accountId/unblacklist
```

`PUT` accepts `hourly_run_limit`, `daily_run_limit`,
`instant_billing_wall`, `blacklisted`, and `blacklist_reason`. The first two
are non-negative integers (`0` is unlimited); the wall is a non-negative gold
decimal. `POST .../unblacklist` clears a blacklist after the account is
resolved by an administrator.

## Runs

A run executes the agent model against the conversation history and produces an assistant response.

### Create run (non-streaming)

```
POST /api/conversations/:id/runs
```

**Request body**

```json
{
  "message": "What is the capital of France?",
  "stream": false
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `message` | string | yes | User message content. |
| `stream` | bool | no | `false` (default) returns a JSON response. `true` opens an SSE stream. |
| `attachment_ids` | array of strings | no | File IDs from Solar Network FileSystem. Resolved to image URLs automatically. |
| `input_parts` | array | no | Multimodal input (images, extra text). See [Input parts](#input-parts). |

**Response** `200 OK`

```json
{
  "thread": { ... },
  "run": {
    "id": "01JF...",
    "status": "completed",
    "model": "openai/gpt-4o"
  },
  "request_message": { ... },
  "response_message": { ... },
  "content": "The capital of France is Paris."
}
```

**Error** `4xx` — agent not found, conversation access denied, model error, etc.

### Create run (streaming)

```
POST /api/conversations/:id/runs
```

Same request body as above with `"stream": true`.

**Response** `200 OK` with `Content-Type: text/event-stream`

Events are sent as SSE frames. See [SSE Events](#sse-events) below.

### List runs

```
GET /api/conversations/:id/runs?take=20&offset=0
```

**Response** `200 OK` — array of run objects.
**Header** `X-Total` — total run count.

### Get run

```
GET /api/conversations/:id/runs/:runId
```

**Response** `200 OK` — run object.
**Response** `404 Not Found` — run does not exist or access denied.

---

## Autonomous Runs

Trigger a run from an external system without a prior conversation.

```
POST /api/agents/:id/autonomous-runs
```

**Request body**

```json
{
  "prompt": "Say hello to the user",
  "target_account_name": "username",
  "trigger": "external_webhook"
}
```

**Response** `200 OK` — run result (same shape as non-streaming run result).

### Internal: Start autonomous conversation

```
POST /api/internal/agents/:id/start-conversation
```

Requires `X-Autonomous-Secret` header matching the server config.

**Request body**

```json
{
  "target_account_id": "123",
  "target_account_name": "username",
  "prompt": "Initial message"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `target_account_id` | string | yes* | Account ID to start conversation with. |
| `target_account_name` | string | yes* | Account name alternative. *At least one of the two is required.* |
| `prompt` | string | no | Initial prompt for the conversation. |

---

## Input Parts

For multimodal input, pass `attachment_ids` alongside `message`:

```json
{
  "message": "What's in this image?",
  "attachment_ids": ["abc123", "def456"]
}
```

For extra text parts, use `input_parts`:

```json
{
  "message": "Compare these images",
  "attachment_ids": ["abc123", "def456"],
  "input_parts": [
    {"type": "text", "text": "Focus on the colors"}
  ]
}
```

| Part type | Fields |
|-----------|--------|
| `text` | `text` (required) |
| `image` | `attachment_id` (required). Resolved from Solar Network FileSystem. |

### Vision and summarization

Models declare supported modalities via the `modalities` field in their provider model config (e.g. `["image"]`, `["image", "audio", "video"]`). When a model supports `image`, image parts are sent directly to the model. When it does not, PersonalityCore automatically summarizes each image using the app-wide `visionModel` configured under `[personality]` and injects the summary as text. Summaries are cached and reused.

If no `visionModel` is configured, image parts for non-vision models are replaced with a placeholder.

---

## SSE Events

When streaming is enabled (`"stream": true`), the server sends events in this order:

### `run.started`

```json
{"conversation_id": "..."}
```

Emitted once when the run begins.

### `reasoning.delta`

```json
{"delta": "Let me think about this..."}
```

Emitted as the model produces reasoning/thinking content. Only sent if the model supports it.

### `tool_call.delta`

```json
{
  "id": "call_abc123",
  "name": "get_user_profile",
  "arguments": "{\"account_name\":\"alice\"}"
}
```

Emitted when the model requests a tool call. Each event is a complete tool call object (the model's tool call arguments are assembled before being emitted). Multiple `tool_call.delta` events may be sent per run if the agent uses chat tools.

### `message.delta`

```json
{"delta": "The capital"}
```

Emitted for each text content chunk. Accumulate to build the full response.

### `message.completed`

```json
{
  "content": "The capital of France is Paris.",
  "message_id": "01JF..."
}
```

Emitted when the full assistant message is assembled and persisted.

### `run.completed`

```json
{
  "run_id": "01JF...",
  "message_id": "01JF..."
}
```

Emitted when the run finishes successfully.

### `run.failed`

```json
{"error": "model rate limit exceeded"}
```

Emitted if the run fails. No further events follow.

### `heartbeat`

```json
{"ok": true}
```

Sent every 15 seconds to keep the connection alive.

---

## File Summaries

PersonalityCore can summarize image attachments for models that do not support vision natively. Summaries are cached in the database and reused across runs.

### Get file summary

```
GET /api/files/:id/summary
```

Returns the cached summary for an attachment. No authentication required.

**Response** `200 OK`

```json
{
  "attachment_id": "abc123",
  "summary": "A photo of a sunset over the ocean with orange and purple clouds.",
  "model": "openai/gpt-4.1-mini"
}
```

**Response** `404 Not Found` — no summary exists for this attachment.

### Generate file summary

```
POST /api/files/summary
```

Generates and caches a summary using the configured vision model. Requires authentication.

**Request body**

```json
{
  "attachment_id": "abc123"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `attachment_id` | string | one of both | File ID from Solar Network. Image URL is resolved from `solarNetwork.baseUrl`. |
| `image_url` | string | one of both | Direct image URL. |

**Response** `200 OK`

```json
{
  "attachment_id": "abc123",
  "summary": "A photo of a sunset over the ocean with orange and purple clouds.",
  "model": "openai/gpt-4.1-mini"
}
```

**Response** `400 Bad Request` — missing both fields or no vision model configured.
**Response** `500 Internal Server Error` — vision model error or attachment is not an image.

---

## Health

```
GET /health
```

**Response** `200 OK`

```json
{"ok": true}
```

No authentication required.

---

## Error Responses

All error responses follow this shape:

```json
{"error": "human-readable error message"}
```

| Status | Meaning |
|--------|---------|
| 400 | Bad request / validation error |
| 403 | Access denied (conversation belongs to another account) |
| 404 | Resource not found |
| 500 | Internal server error |

---

## Example: Streaming with curl

```bash
curl -N -X POST http://localhost:8090/api/conversations/01JF.../runs \
  -H "Content-Type: application/json" \
  -H "X-Account-Id: my-account" \
  -d '{"message": "Hello!", "stream": true}'
```

Output:

```
event: run.started
data: {"conversation_id":"01JF..."}

event: reasoning.delta
data: {"delta":"The user said hello..."}

event: message.delta
data: {"delta":"Hi"}

event: message.delta
data: {"delta":" there"}

event: message.delta
data: {"delta":"!"}

event: message.completed
data: {"content":"Hi there!","message_id":"01JF..."}

event: run.completed
data: {"run_id":"01JF...","message_id":"01JF..."}
```

## Example: Streaming with JavaScript (EventSource)

```javascript
const response = await fetch(`/api/conversations/${threadId}/runs`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json', 'X-Account-Id': accountId },
  body: JSON.stringify({ message: 'Hello!', stream: true }),
});

const reader = response.body.getReader();
const decoder = new TextDecoder();
let buffer = '';

while (true) {
  const { done, value } = await reader.read();
  if (done) break;
  buffer += decoder.decode(value, { stream: true });

  const lines = buffer.split('\n');
  buffer = lines.pop(); // keep incomplete line

  let event = '';
  for (const line of lines) {
    if (line.startsWith('event: ')) {
      event = line.slice(7).trim();
    } else if (line.startsWith('data: ')) {
      const data = JSON.parse(line.slice(6));
      switch (event) {
        case 'message.delta':
          appendText(data.delta);
          break;
        case 'reasoning.delta':
          showThinking(data.delta);
          break;
        case 'tool_call.delta':
          showToolCall(data.name, data.arguments);
          break;
        case 'message.completed':
          finalizeMessage(data.content, data.message_id);
          break;
        case 'run.completed':
          console.log('Done:', data.run_id);
          break;
        case 'run.failed':
          console.error('Failed:', data.error);
          break;
      }
    }
  }
}
```
