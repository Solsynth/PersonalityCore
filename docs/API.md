# PersonalityCore API Reference

Base URL: `<Solar Network Base URL>/personality`

When using with the gateway, no need to add `/api` prefix, only the `/personality` is enough.

All endpoints require authentication unless noted otherwise. Auth is handled via one of:

- **Solar auth**: Bearer token validated against the configured auth service.
- **Offline mode**: Every request maps to a single configured mock account (local dev).
- **Dev header mode**: `X-Account-Id` header (when `allowDevIDs` is enabled).

---

## Agents

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
