# @code Architecture Guide

**@code** is a local-first AI coding agent built with Go. This document explains the system architecture, key components, and design decisions.

## High-Level Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    @code CLI / TUI                          │
│                   (cmd/atcode, TUI)                          │
└────────┬────────────────────────────────────────────────────┘
         │
┌────────▼────────────────────────────────────────────────────┐
│                     Core Orchestration                       │
│   ┌─────────────────────────────────────────────────────┐   │
│   │ Agent Runtime (internal/agent)                      │   │
│   │ • Message loop, tool dispatch, error handling      │   │
│   │ • Streaming responses, context management          │   │
│   └─────────────────────────────────────────────────────┘   │
└────────┬───────────────────────────────────────────────────┘
         │
    ┌────┴──────────────────────────────────────────┐
    │                                               │
┌───▼──────────┐  ┌──────────────┐  ┌────────────┐ │
│ Inference   │  │   Tools &    │  │ Features  │ │
│             │  │   Services   │  │           │ │
│ llama/      │  │              │  │ auth/     │ │
│             │  │ • file       │  │ profile/  │ │
│ • server    │  │ • bash       │  │ tips/     │ │
│ • config    │  │ • browser    │  │ sessions/ │ │
│ • warmup    │  │ • vision     │  │           │ │
│ • models    │  │ • mcp        │  │           │ │
└───┬──────────┘  └──────────────┘  └────────────┘ │
    │                                               │
    └───────────────────────────────┬──────────────┘
                                   │
┌──────────────────────────────────▼──────────────────┐
│            Storage & Persistence                    │
│  ┌──────────────┐  ┌───────────────┐               │
│  │ Sessions     │  │ Auth Tokens   │ (~/.atcode/)  │
│  │ Profiles     │  │ User Data     │               │
│  │ Model State  │  │ Config        │               │
│  └──────────────┘  └───────────────┘               │
└──────────────────────────────────────────────────────┘
         │
         │ Remote (optional)
         │
    ┌────▼──────────────────────────┐
    │ Supabase (Auth + Profiles)     │
    │ atcode-server (inference)      │
    └────────────────────────────────┘
```

## Core Packages

### `internal/agent/` — Orchestration
The main event loop that coordinates everything:
- Message processing
- Tool invocation and result handling
- Context management (token counting)
- Streaming responses
- Permission gates and sandboxing
- Session state

**Key Types**:
- `Agent` — Main orchestrator
- `Message` — Chat message with role/content
- `ToolCall` — Request to invoke a tool
- `ToolResult` — Response from a tool

### `internal/llama/` — Inference
Manages the local atcode engine:
- `server.go` — Process management, HTTP communication
- `config.go` — Device detection, context tier selection
- `warmup.go` — Model prefill and performance estimation
- `models.go` — Model download and management

**Flow**:
1. Detect hardware capabilities (RAM, CPU, GPU)
2. Select optimal context tier (32K/128K/256K)
3. Download model if needed
4. Start atcode-server process
5. Warm up with prefill (text + vision tokens)
6. Ready for inference

### `internal/auth/` — Authentication
Manages user authentication via Supabase:
- `supabase.go` — Supabase GoTrue client (sign-in, OAuth, tokens)
- `oauth.go` — Google OAuth 2.0 PKCE flow
- `storage.go` — Token persistence, session management
- `commands.go` — CLI commands (/auth login, logout, status)

**Flow**:
1. User runs `/auth login`
2. Choose method: Google OAuth or magic link email
3. Supabase handles OAuth/email verification
4. Session token stored in `~/.atcode/auth.json`
5. Token auto-refreshes before expiry
6. Available for profile operations and tips

### `internal/tools/` — Tool System
30+ tools for code, file, shell operations:
- `file_tools.go` — Read, write, edit, list files
- `bash.go` — Shell execution with sandbox
- `local_browser.go` — Browser automation
- `vision.go` — Image analysis, screenshots
- `web_fetch.go` — HTTP requests
- `registry.go` — Tool discovery and dispatch

**Permission Model**:
- Sandbox mode: Read-only, no execution
- Confirm mode: Prompt user for each action
- Unrestricted: Full access (default for local)

### `internal/profile/` — User Profiles
Persistent user preferences and metadata:
- `profile.go` — Profile CRUD, statistics, preferences

**Profile Data**:
- Theme, permissions, context tier
- Tool enablement, custom allow/block lists
- Statistics (sessions, tokens, favorite tools)
- Model defaults and settings

### `internal/tips/` — Contextual Tips
Smart suggestions based on user activity:
- `tips.go` — Tip database, keyword matching, scoring

**Features**:
- 15+ core tips (expandable)
- Keyword-based matching against user input
- Priority weighting + recency bias
- `/tips` command with random selection

### `internal/theme/` — Dark Blue Theme
@code's visual identity:
- `dark_blue.go` — Color palette and styling

**Colors**:
- Primary: Dark blue (#0051A5)
- Accent: Cyan (#00D4FF)
- Semantic: Green (success), Red (error), Orange (warning)
- Neutral: Off-white text on dark background

### `internal/sessions/` — Session Persistence
Stores and retrieves conversation history:
- `exec_session.go` — Session state and replay
- `store_test.go` — Session storage

**Operations**:
- Save after each turn
- Resume previous sessions
- List sessions
- Clear/delete sessions

### `internal/cli/` — Command Interface
CLI commands and TUI launcher:
- `app.go` — Main entry point
- `exec.go` — Batch execution mode
- `setup.go` — First-run configuration
- Many command modules

**Key Commands**:
- `at` — Interactive TUI
- `at exec` — Batch execution
- `at setup` — Configuration
- `at /auth` — Authentication

### `internal/tui/` — Terminal UI
Bubble Tea-based interactive interface:
- `run.go` — Main TUI loop
- `model.go` — UI state machine
- `rendering.go` — Output formatting
- Many component modules (panels, modals, etc.)

**Components**:
- Input area with slash commands
- Transcript view with scrollback
- Tool output display
- Sidebar (files, MCP servers, etc.)
- Status bar

## Data Flow: Chat Turn

```
User Input
    │
    ▼
TUI Command Parsing
    │
    ├─ Regular prompt → Package as user message
    ├─ Slash command (/resume, /compact, etc.) → Handle directly
    │
    ▼
Agent Runtime
    │
    ├─ Prepend system prompt
    ├─ Add context (relevant files, previous turns)
    ├─ Measure tokens
    │
    ▼
LLaMA.cpp Server
    │
    ├─ Tokenize
    ├─ Forward pass (inference)
    ├─ Token by token streaming
    │
    ▼
Response Processing
    │
    ├─ Detect tool calls (function calls in response)
    ├─ Invoke matching tools
    ├─ Collect tool results
    │
    ▼
Tool Results
    │
    ├─ Send back to LLM as <tool_result>
    ├─ LLM generates final response
    │
    ▼
Output
    │
    ├─ Stream to TUI
    ├─ Format with syntax highlighting
    ├─ Save to session history
    │
    ▼
Storage
    │
    ├─ Write to ~/.atcode/sessions/<session-id>.jsonl
    ├─ Update statistics
    ├─ Profile changes
```

## Configuration Hierarchy

```
1. Defaults (internal/profile/DefaultProfile)
2. ~/.atcode/config.json (device settings)
3. ~/.atcode/profile_<user-id>.json (user preferences)
4. Environment variables (ATCODE_CONTEXT_SIZE, ATCODE_GPU_LAYERS)
5. CLI flags (future)
```

## Authentication Flow

```
User Runs: sonut /auth login magic <email>
    │
    ▼
AuthManager.Login(method="magic", email="...")
    │
    ├─ SupabaseClient.SignInWithMagicLink(email)
    │  └─ POST /auth/v1/otp (sends email with OTP link)
    │
    ▼
User Clicks Email Link
    │
    ├─ Supabase callback: /auth/v1/callback?code=...&state=...
    │
    ▼
User Runs: sonut /auth verify <otp>
    │
    ├─ SupabaseClient.VerifyOTP(email, otp)
    │  └─ POST /auth/v1/verify (validates OTP)
    │
    ▼
TokenStorage.SaveSession(session)
    │
    └─ Write to ~/.atcode/auth.json (mode 0600)
```

## Model Inference Flow

```
User Prompt + Context
    │
    ▼
TokenCount()
    │
    ├─ System prompt: ~250 tokens
    ├─ Context: variable
    ├─ User input: variable
    ├─ Available space: (tier - above)
    │
    ▼
Decision
    │
    ├─ If tokens < available: proceed
    ├─ If tokens > available: trigger /compact
    ├─ If /compact fails: fallback tier (256K→128K→32K)
    │
    ▼
LLaMA.cpp Forward Pass
    │
    ├─ Tokenize (BPE)
    ├─ Attention (with flash attention)
    ├─ KV cache (q8_0 quantization)
    ├─ Sampling (top-p, top-k, temperature)
    │
    ▼
Token Stream
    │
    └─ To TUI for display
```

## Device Detection & Tier Selection

```
detect_capabilities()
    │
    ├─ OS: Darwin (macOS), Linux, Windows
    ├─ CPU: cores, threads
    ├─ RAM: physical memory in bytes
    ├─ GPU: type (Metal/CUDA/ROCm), memory
    │
    ▼
select_context_tier()
    │
    ├─ RAM >= 40GB: 256K tier
    ├─ RAM >= 20GB: 128K tier
    ├─ RAM >= 8GB: 32K tier
    │
    ▼
recommend_config()
    │
    ├─ GPU layers (0-999 based on VRAM)
    ├─ Batch size (64-512 based on RAM)
    ├─ Thread count (auto-detect or env var)
    │
    ▼
Config ready for atcode-server startup
```

## Error Handling Strategy

| Error Type | Handling |
|-----------|----------|
| **Token overflow** | Auto-fallback: 256K→128K→32K |
| **GPU OOM** | Reduce GPU layers or fallback to CPU |
| **atcode-server crash** | Restart with reduced layers |
| **Auth failure** | Graceful logout, prompt re-login |
| **Tool execution** | Sandbox denial, user confirmation, retry |
| **Network timeout** | Retry with backoff, offline mode |

## Performance Characteristics

| Operation | Typical Time |
|-----------|-------------|
| Model startup | 30-45s (download) + 10-20s (warmup) |
| First token | 50-200ms (depends on context) |
| Token generation | 3-15 tok/s (depends on hardware) |
| /compact turn | 5-10s (context compression) |
| Auth flow | 2-5s (network dependent) |

## Security Considerations

1. **Token Storage**: `~/.atcode/auth.json` with mode 0600 (owner read/write only)
2. **PKCE Flow**: OAuth 2.0 PKCE prevents code interception
3. **Sandbox**: All tool execution in restricted mode by default
4. **Telemetry**: Disabled by default, opt-in only
5. **Model**: Local inference, no data sent to cloud (except optional Supabase)

## Future Extensions

- **Web UI** (v2.0): Next.js frontend
- **Multi-model** (v2.0): Swap between different model sizes
- **Voice I/O** (v2.0): Whisper + text-to-speech
- **Plugin system** (v2.0): Custom MCP servers
- **Distributed sessions** (v2.0): Cloud backup
- **Desktop app** (v3.0): Electron wrapper
