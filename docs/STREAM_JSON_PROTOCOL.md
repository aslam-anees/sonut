# @code Stream-JSON Protocol

@code stream-json is a line-delimited protocol for headless clients such as
editor extensions and automation wrappers.

Every line is one JSON object. Empty input lines are ignored. Output events are
redacted before they are written to stdout.

## Version

Current schema version: `2`

Every input and output event must include:

```json
{ "schemaVersion": 2, "type": "..." }
```

Output events also include `runId`.

## Input Events

`at exec --input-format stream-json` accepts these JSONL events from stdin or
`--file`.

```json
{ "schemaVersion": 2, "type": "message", "role": "user", "content": "Inspect this repo." }
{ "schemaVersion": 2, "type": "prompt", "content": "Return only blockers." }
```

@code combines accepted input event content in order, separated by blank lines.
Unknown fields are rejected so protocol clients catch drift early.

Schema version `2` renamed sandbox permission metadata from `violation` to
`block`. Clients should read the optional `block` object on permission and tool
events when present.

## Output Events

`at exec --output-format stream-json` emits schema-versioned JSONL events.

```json
{ "schemaVersion": 2, "type": "run_start", "runId": "run_20260603_abc123", "sessionId": "atcode_20260603100000_abc123", "cwd": "/repo", "provider": "openai", "model": "gpt-4.1", "apiModel": "gpt-4.1" }
{ "schemaVersion": 2, "type": "text", "runId": "run_20260603_abc123", "delta": "..." }
{ "schemaVersion": 2, "type": "tool_call", "runId": "run_20260603_abc123", "id": "call_1", "name": "read_file", "args": { "path": "README.md" }, "sideEffect": "read" }
{ "schemaVersion": 2, "type": "tool_progress", "runId": "run_20260603_abc123", "id": "call_1", "name": "read_file", "lifecycle": "running", "phase": "reading", "message": "Reading README.md", "elapsedMs": 42 }
{ "schemaVersion": 2, "type": "permission_request", "runId": "run_20260603_abc123", "id": "call_2", "name": "write_file", "action": "prompt", "permission": "prompt", "permissionMode": "ask", "sideEffect": "write", "reason": "Creates or overwrites files." }
{ "schemaVersion": 2, "type": "permission_decision", "runId": "run_20260603_abc123", "id": "call_2", "name": "write_file", "action": "allow", "permission": "prompt", "permissionGranted": true, "decisionReason": "approved in TUI" }
{ "schemaVersion": 2, "type": "tool_result", "runId": "run_20260603_abc123", "id": "call_1", "status": "ok", "lifecycle": "succeeded", "output": "...", "truncated": false }
{ "schemaVersion": 2, "type": "usage", "runId": "run_20260603_abc123", "promptTokens": 12, "completionTokens": 8, "totalTokens": 20 }
{ "schemaVersion": 2, "type": "final", "runId": "run_20260603_abc123", "text": "..." }
{ "schemaVersion": 2, "type": "run_end", "runId": "run_20260603_abc123", "status": "success", "exitCode": 0 }
```

Permission events may include structured sandbox metadata:

```json
{ "schemaVersion": 2, "type": "permission_request", "runId": "run_20260603_abc123", "id": "call_3", "name": "bash", "action": "prompt", "permission": "prompt", "permissionMode": "ask", "sideEffect": "shell", "reason": "network access requires approval", "risk": { "level": "critical", "categories": ["network"] }, "block": { "code": "network", "toolName": "bash", "action": "prompt", "risk": { "level": "critical", "categories": ["network"] }, "reason": "network access requires approval", "recoverable": true } }
```

Errors are part of the protocol and are followed by `run_end`.

Headless `exec` has no interactive permission responder. If a prompt-gated tool
is not pre-approved, @code may emit `permission_request` followed by a denied
`tool_result`; interactive surfaces emit `permission_decision` when the user
allows, denies, or always-allows the request.

### Tool lifecycle and errors

Every dispatched tool call emits a `tool_progress` event with
`lifecycle: "running"`, then ends with a `tool_result` whose `lifecycle` is one
of `succeeded`, `failed`, `blocked`, `cancelled`, or `timed_out`. Consumers
should treat `tool_result` as authoritative if a stream ends early.

Failed results can include a structured `error` object in JSON output, or the
equivalent `errorKind`, `retryable`, and `hint` fields in stream JSON. These are
display metadata and do not replace the redacted `output` text. Retry only when
`retryable` is true and repeating the operation is safe.

```json
{ "schemaVersion": 2, "type": "error", "runId": "run_20260603_abc123", "code": "provider_error", "message": "...", "recoverable": false }
{ "schemaVersion": 2, "type": "run_end", "runId": "run_20260603_abc123", "status": "error", "exitCode": 3 }
```
