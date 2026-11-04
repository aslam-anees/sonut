# @ code — Local-First AI Coding Agent

<p align="center">
  <strong>A terminal coding agent. Yours. Local. Always.</strong>
</p>

<p align="center">
  <a href="LICENSE"><img alt="license" src="https://img.shields.io/badge/license-MIT-blue"></a>
  <img alt="Go 1.26.4+" src="https://img.shields.io/badge/Go-1.26.4+-00ADD8?logo=go&logoColor=white">
  <img alt="atcode engine" src="https://img.shields.io/badge/atcode--engine-native-34E2EA">
  <img alt="atcode model" src="https://img.shields.io/badge/Model-atcode%20local-9d4edd">
</p>

**@code** is a terminal AI coding agent powered by the optimized **atcode engine**. Work entirely locally—no API keys, no cloud dependencies, no vendor lock-in. Your code, your data, your machine.

```bash
at
at exec "fix the failing test in ./pkg"
at exec --output-format stream-json < turns.jsonl
```

## Why @code

- **Local inference.** Run a supported model on your machine. Cloud providers, web tools, downloads, authentication, and opt-in analytics may make network requests; telemetry is disabled by default.
- **No API Keys.** The optimized atcode model runs on your hardware. No OpenAI/Anthropic/Gemini credentials needed.
- **Local session storage.** Local inference keeps prompts on your machine; configured cloud providers and explicit network tools can transmit content. Review your configuration before handling private code.
- **Works Offline.** Once the model warms up, works entirely without internet.
- **Terminal Native.** Dark blue TUI, live plan rendering, slash commands, resume support, tools galore.
- **Scriptable.** `at exec` for CI/CD, batch operations, isolated worktrees.
- **Extensible.** MCP servers, tools, hooks—all running locally under your control.
- **Single Binary.** Cross-platform (macOS/Linux/Windows). Go-based, statically linked.

## Open-source source release

This release publishes the Go source. Start with **From Source** below. Prebuilt binaries, npm packages, model downloads, and the separate engine have their own availability and licensing; this commit does not publish those artifacts. See [SECURITY.md](SECURITY.md) and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Install

### npm (Recommended)

```bash
npm install -g @aistatine/atcode
at
```

The npm package installs a small wrapper plus the @code binary for your platform from GitHub Releases. Supports Linux, macOS, and Windows on x64 and arm64.

### Linux/macOS Script

```bash
curl -fsSL https://raw.githubusercontent.com/aslam-anees/sonut/src/install/install.sh | bash
```

### Windows PowerShell

```powershell
irm https://raw.githubusercontent.com/aslam-anees/sonut/src/install/install.ps1 | iex
```

### From Source

Requires Go 1.26.4+:

```bash
git clone https://github.com/aslam-anees/sonut.git
cd sonut
go run ./cmd/atcode
```

Build a static binary:

```bash
go build -o at ./cmd/atcode
```

On Linux, also build the sandbox helper:

```bash
go build -o at-linux-sandbox ./cmd/atcode-linux-sandbox
```

Place both in the same directory on `PATH` (normally `~/.atcode/bin`).

## Quick Start

### Interactive TUI

Start the interactive terminal:

```bash
at
```

The setup wizard configures your local environment:

```bash
at setup
at doctor
```

### Keyboard Shortcuts

| Key | Action |
|---|---|
| `Enter` | Send prompt |
| `/` | Show slash commands |
| `Shift+Tab` | Cycle permission mode |
| `Ctrl+S` | Open or close the sidebar |
| `Ctrl+C` | Cancel or exit |
| `Ctrl+T` | Cycle effort mode |

### Effort Modes

@code defaults to **auto** — the agent decides thinking depth per turn based on task complexity.

- **auto** — Default. Agent picks flash or super per turn.
- **flash** — Fast, concise private thinking. Best for quick edits and refactoring.
- **super** — Maximum deep thinking. Best for architecture and complex problem-solving.

Set effort with `/effort auto|flash|super` or press `Ctrl+T` to cycle.

### Slash Commands

| Command | Purpose |
|---|---|
| `/help` | Show all available commands |
| `/resume [id]` | List saved sessions or resume one |
| `/compact [instructions]` | Compress context (drop old turns, keep summary) |
| `/effort [auto\|flash\|super]` | Set thinking mode (default: auto) |
| `/skills` | List bundled and installed skills |
| `/tools` | List registered tools |
| `/permissions` | Show sandbox mode and access grants |
| `/doctor [fix\|--connectivity]` | Run system diagnostics |
| `/context` | Show workspace and runtime context |
| `/sec [open\|rescan\|diff\|routes\|processes\|activity\|tools\|signals\|sensor\|help]` | Open the unified native security workspace with posture, prioritized risks, typed proxy/ingress routes, bounded process lineage, diff-scoped scan, live egress, tool surfaces, agent telemetry/behavior triage, controls, and a grounded AI advisor |
| `/config [recaps on\|off]` | Show or toggle configuration |
| `/clear` | Clear visible transcript |
| `/exit` | Exit @code |
| `/git [status\|log\|diff]` | Quick git status/diff |
| `/commit [message]` | Commit all changes with AI-generated message |
| `/cd <path>` | Change working directory (grants write access) |
| `/add-dir [path]` | Grant write access to external directories |
| `/rewind [latest\|<seq>]` | Restore files to a checkpoint and truncate session |
| `/export [path]` | Export session transcript to Markdown |

For a complete list, type `/help` in @code.

## Scripting & Headless Use

Run a single turn without the interactive TUI:

```bash
# Run a single turn
at exec "list all .ts files"

# Pipe JSON turns (ndjson format)
at exec --output-format stream-json < turns.jsonl

# Isolated worktree (doesn't touch your repo)
at exec --worktree /tmp/work "make test"

# Specify a session to resume
at exec --resume latest "continue the implementation"
```

## Configuration

Configuration stored in `~/.atcode/`:

```
~/.atcode/
├── config.json              # atcode engine settings, context tier
├── engine/
│   └── atcode-server        # Platform-native local inference engine
├── model/                   # Downloaded model binaries
│   ├── atcode-v1.gguf             # atcode v1.0 main model
│   └── atcode-v1.0-mmproj.gguf     # Vision projector
├── sessions/                # Saved conversation history
├── skills/                  # User-installed skills (SKILL.md files)
├── knowledge/               # User knowledge notes (markdown)
├── ttl/                     # Soft-delete trash (auto-purged after 24h)
└── auth.json                # Optional: authentication token
```

### Environment Variables

```bash
# Optional: Cloud provider authentication
export OPENAI_API_KEY=sk-...           # To use OpenAI models
export ANTHROPIC_API_KEY=sk-ant-...    # To use Claude (headless)

# atcode engine tuning (advanced)
export ATCODE_CONTEXT_SIZE=256000       # Max context (auto-fallback on OOM)
export ATCODE_GPU_LAYERS=999            # GPU layer count (auto-detect by default)
export ATCODE_LLAMA_PORT=2748           # Port for the atcode engine
export ATCODE_ENGINE=auto                # auto (default), required, or off
export ATENGINE_HOME=~/.atengine         # shared local @engine state/socket

# Optional: Telemetry (disabled by default)
export ATCODE_TELEMETRY=1

# Optional: Skills directory override
export ATCODE_SKILLS_DIR=~/.myskills
```

When the @engine daemon is installed and ready, `@code` uses its coding lane
over the owner-only local socket. `ATCODE_ENGINE=off` keeps the direct
llama.cpp rollback; `ATCODE_ENGINE=required` fails fast if the shared daemon is
not available. `/research <query>` opens the native Knowledge Atlas research
flow with source URLs and page previews.

## Features

### Core Tools

- **File Operations**: Read, write, list, diff, search, glob patterns
- **Shell Execution**: bash, sh, zsh with full output capture and exit codes
- **Browser Automation**: Screenshot, click, form fill, page read, JavaScript execution
- **Web Tools**: Fetch URLs, search the web (multiple backends), security checks
- **Code Execution**: Python, Node.js, shell scripts with sandboxing
- **Git Operations**: Status, diff, log, commit with AI-generated messages
- **MCP Servers**: Connect Model Context Protocol servers for extended functionality

### Session Management

- **Resume Sessions**: Pick up where you left off; full history searchable
- **Compact Context**: Automatically summarize old turns, preserve recent ones
- **Checkpoints**: `/rewind` to restore workspace files to any checkpoint
- **Handoffs**: `/handoff` to write lossless structured handoff documents for fresh sessions
- **Export**: `/export` to save session transcripts as Markdown

### Intelligence

- **[atcode-v1.0](https://huggingface.co/anees-aslam-ai/atcode-v1.0)**: optimized local model; runs on supported Apple Silicon, Linux, and Windows hardware
- **Smart Effort Modes**: auto (default, agent chooses), flash (fast, concise thinking), super (max thinking)
- **Context Tiers**: 32K (fast), 128K (balanced), 256K (max) with auto-fallback on OOM
- **Planning**: Live multi-step plan rendering in the sidebar; `/plan` to show status
- **Tips System**: Contextual suggestions based on task and history
- **KV Cache Optimization**: Prefix reuse, quantization, flash attention

### UX

- **Dark Blue Terminal Theme**: Low-light friendly, accent colors for actions
- **Live Streaming**: See natural answers, status, and tool calls as they happen; private reasoning stays private
- **Syntax Highlighting**: Code blocks colored by language
- **Scrollback History**: Full session retained in memory; navigate with arrow keys
- **Permission Modes**: sandbox (read-only), ask (prompt each tool call), auto (auto-approve every grantable tool; absolute policy denials remain blocked)
- **File Sidebar**: Quick access to modified/created files in this session
- **Agent Sidebar**: Active agents and multi-agent coordination

### Security & Privacy

- **Sandbox Modes**:
  - `read-only`: Only read operations; write attempts are blocked and explained
  - `ask`: Prompt for each tool call (default during onboarding)
  - `auto`: Auto-approve safe tool uses; deny dangerous egress
- **Unified Security**: `/sec` performs a bounded agentless workspace scan and opens a native, keyboard-first TUI for prioritized risks, evidence-backed paths, live session egress, outbound tool surfaces, normalized agent telemetry, transparent behavior triage, controls, and an optional AI advisor grounded only in the redacted report
- **Typed attack surface**: `/sec` extracts conservative Compose, Kubernetes, Dockerfile, GitHub Actions, and Terraform assets with configured-public, runtime-observed, potential, or unknown exposure states; static configuration is never presented as proof of internet reachability
- **Proxy route inventory**: `/sec routes` surfaces GoDoxy-style Compose labels, route files, and Kubernetes ingress mappings with target, TLS, authentication, middleware, and evidence metadata; it never performs discovery or claims reachability
- **Process provenance**: `/sec processes` and `/sec process pid|port|name|file <value>` provide bounded, read-only local lineage and socket inspection with secret argument redaction; no environments, elevation, or process control
- **Fix routing**: findings carry CODEOWNERS-based owner/service/fix targets; in Risks, press `x` to close `/sec` and hand a redacted, grounded fix request to the normal agent flow. It does not auto-commit, push, or contact owners.
- **Threat intel**: disabled by default and local-only until `.atcode/security.json` explicitly enables OSV exact-version enrichment. Results are bounded, cached under `.atcode/sec/cache/`, and never contain workspace content or tokens.
- **Runtime layers**: Tool Guard is application-aware, the existing sandbox remains the process/filesystem boundary, and the Linux eBPF sensor is an opt-in privileged helper. Use `/sec sensor status|start|stop|help`; macOS and Windows report unsupported, no packet payloads are captured, and HTTPS is not decrypted.
- **Audit Trail**: Enterprise mode logs all tool calls and permission decisions
- **SSRF Guard**: Web tools validate URLs to prevent localhost/private IP access
- **Local-Only by Default**: No telemetry, no cloud sync unless explicitly configured

## Performance

Measured on an Apple M4 with the optimized atcode model:

| Metric | Value |
|--------|-------|
| Generation | ~15 tok/s |
| Prompt prefill | ~60 tok/s |
| First response after warmup | sub-second (KV prefix cache) |

Other hardware will vary; run `/hw` in the TUI to see what atcode detected and the flags it chose.

**Auto-Optimization**:
- Context tier selection based on device RAM
- GPU detection (Metal on macOS, CUDA on Linux/Windows, CPU fallback)
- Dynamic batch sizes + flash attention
- Automatic OOM recovery (256K → 128K → 32K fallback)
- Model preloading / warmup in background
- **Unlimited-context ladder** (MiMo/qwen-inspired): soft-trim large tool dumps at ~65% window → hard-prune stale results at ~80% → paid LLM summary only if still over; repeated `read_file` of unchanged files short-circuits to a one-line placeholder

## Skills System

Skills are reusable instruction sets stored as `SKILL.md` markdown files. Bundled skills come with @code; user-installed skills live in `~/.local/share/atcode/skills/*/SKILL.md`.

```bash
at skills                           # List all available skills
at skills --show frontend-design    # Show a skill's content
```

Create your own skill:

```
~/.local/share/atcode/skills/my-skill/
├── SKILL.md
```

**SKILL.md format:**

```markdown
---
name: My Custom Skill
description: What this skill teaches the model
---

# Content

Instructions, patterns, examples, guidelines for the model to apply.
```

The model automatically loads and applies relevant skills based on task context.

## MCP Support

@code can use Model Context Protocol (MCP) servers to extend functionality:

```bash
at mcp list                     # List configured servers
at mcp add <name> -- <command>  # Add a local stdio server
at mcp add <name> --url <url>   # Add an HTTP/SSE server
at mcp check <name>             # Connect and verify its tools
at mcp tools list               # Show the active tool mapping
at mcp remove <name>            # Remove a server
```

MCP servers run locally under your control and expose tools, resources, and prompts to @code.

## Architecture

@code uses a clean, modular architecture:

```
cmd/atcode/                  # Main binary entry point (@code TUI)
cmd/atcode-linux-sandbox/    # Linux sandbox helper
internal/
├── cli/                    # TUI interface (Bubble Tea framework)
├── tui/                    # Terminal UI components and commands
├── llama/                  # Internal atcode engine integration
├── tools/                  # File, shell, browser, code execution
├── mcp/                    # Model Context Protocol proxy
├── agent/                  # Multi-agent and specialist coordination
├── sessions/               # Resume/persistence layer
├── skills/                 # Skills discovery and loading
├── auth/                   # OAuth + magic link authentication
├── config/                 # Configuration management
├── sandbox/                # System call filtering (Linux)
└── security/               # Permission modes and audit
```

## Privacy & Local-First Design

@code is designed to keep your work on your machine:

- **No telemetry by default** — set `ATCODE_TELEMETRY=1` to opt in
- **Sessions stay local** — saved to `~/.atcode/sessions/`
- **No cloud sync** — optional integrations (Supabase, etc.) are opt-in
- **Web requests logged** — `/sec` shows observed outbound decisions in its Activity tab
- **Coverage is explicit** — Surfaces labels tools as enforced, observed, best-effort, or not-covered; browser page subrequests remain outside Tool Guard’s complete view
- **Credentials local** — OAuth tokens stored in system keyring when available
- **Sandboxed execution** — Linux uses seccomp to limit system calls

If you enable cloud providers (OpenAI, Anthropic), those calls are made directly from your machine; @code does not proxy them through any service.

## Troubleshooting

### Model won't start

```bash
at doctor
at doctor fix
```

### Permission denied on read

You're in sandbox mode. Switch permission modes:

```bash
Shift+Tab                  # Cycle in TUI
/mode ask                  # Prompt mode
/mode auto                 # Auto-approve grantable tools and recoverable sandbox access
```

### Context window too small

Switch to a larger context tier:

```bash
/memory 256k               # Resize and restart the atcode engine
/memory list               # Show available tiers
```

### Session history lost

Sessions are auto-saved. Check `~/.atcode/sessions/` for .jsonl files or:

```bash
/resume                    # List all resumable sessions
/history                   # Browse and search past sessions
```

## Contributing

Contributions welcome! Areas we need help:

- Cross-platform testing (macOS/Linux/Windows GPU detection)
- Performance tuning (KV cache optimization, batch sizing)
- Documentation (setup guides, troubleshooting, API docs)
- Tool enhancements (new integrations, MCP servers)
- Skills (domain-specific instruction sets)

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

MIT. See [LICENSE](LICENSE) for details.

---

**Questions?** Open an issue on [GitHub](https://github.com/aslam-anees/sonut).

**Built with**:
- Go 1.26.4+
- Bubble Tea (TUI framework)
- atcode engine (local inference)
- atcode local model
