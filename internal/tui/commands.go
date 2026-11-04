package tui

import (
	"strings"
)

type commandKind int

const (
	commandEmpty commandKind = iota
	commandPrompt
	commandHelp
	commandClear
	commandExit
	commandTools
	commandSkills
	commandMCP
	commandPermissions
	commandPS
	commandStop
	commandSandboxSetup
	commandFeedback
	commandHW
	commandContext
	commandSec
	commandConfig
	commandDebug
	commandDoctor
	commandPlan
	commandSearch
	commandResume
	commandRetitle
	commandSpec
	commandInit
	commandCompact
	commandRewind
	commandEffort
	commandStyle
	commandTheme
	commandTranscript
	commandBash
	commandImage
	commandAddDir
	commandSelfCorrect
	commandTurns
	commandCommit
	commandReview
	commandMemory
	commandExport
	commandVim
	commandTasks
	commandGit
	commandSignOut
	commandBtw
	commandMode
	commandBuild
	commandVoice
	commandPong
	commandIntegrate
	commandShare
	commandNetwork
	commandNonstop
	commandHistory
	commandCD
	commandBrowser
	commandTrash
	commandKnowledge
	commandResearch
	commandDream
	commandDistill
	commandEvolve
	commandEnterprise
	commandGraph
	commandHandoff
	commandHide
	commandUnknown
)

type commandGroup string

const (
	commandGroupSession commandGroup = "session"
	commandGroupModel   commandGroup = "model"
	commandGroupRuntime commandGroup = "runtime"
	commandGroupTools   commandGroup = "tools"
	commandGroupMeta    commandGroup = "meta"
)

type commandDefinition struct {
	name        string
	aliases     []string
	usage       string
	group       commandGroup
	description string
	kind        commandKind
}

type parsedCommand struct {
	kind commandKind
	text string
	name string
}

var commandDefinitions = []commandDefinition{
	{
		name:        "/hw",
		aliases:     []string{"/hardware"},
		usage:       "/hw",
		group:       commandGroupModel,
		description: "Re-detect hardware (CUDA / Apple Silicon / CPU) and refresh the cached atcode engine tuning.",
		kind:        commandHW,
	},
	{
		name:        "/feedback",
		usage:       "/feedback <message>",
		group:       commandGroupMeta,
		description: "Send feedback to the Aistatine team.",
		kind:        commandFeedback,
	},
	{
		name:        "/mode",
		usage:       "/mode [ask|auto]",
		group:       commandGroupRuntime,
		description: "Show or toggle tool permission mode (ask = prompt each time, auto = auto-approve).",
		kind:        commandMode,
	},
	{
		name:        "/permissions",
		usage:       "/permissions",
		group:       commandGroupRuntime,
		description: "Show the active permission mode and sandbox grants.",
		kind:        commandPermissions,
	},
	{
		name:        "/build",
		usage:       "/build [on|off]",
		group:       commandGroupSession,
		description: "Toggle build mode: @code works as an app builder (scaffold, implement, and preview complete web apps).",
		kind:        commandBuild,
	},
	{
		name:        "/integrate",
		usage:       "/integrate [supabase|oauth|hosting] [secret]",
		group:       commandGroupSession,
		description: "Attach optional integration secrets (Supabase, OAuth, hosting) for build mode. Apps build fine without them; configuring one unlocks it.",
		kind:        commandIntegrate,
	},
	{
		name:        "/share",
		usage:       "/share [on|off]",
		group:       commandGroupSession,
		description: "Mirror this session to your phone or another device: view the chat live and send prompts from a browser on the same network.",
		kind:        commandShare,
	},
	{
		name:        "/voice",
		usage:       "/voice [on|off|status|setup|listen|stop|language|voice|speed|shortcut|input|output|doctor|models]",
		group:       commandGroupSession,
		description: "Configure local multilingual voice input and concise spoken responses.",
		kind:        commandVoice,
	},
	{
		name:        "/pong",
		usage:       "/pong",
		group:       commandGroupSession,
		description: "Open the Pong easter egg (Space is reserved for voice hold when voice is ready).",
		kind:        commandPong,
	},
	{
		name:        "/network",
		usage:       "/network [on|off]",
		group:       commandGroupRuntime,
		description: "Toggle internet access for web tools and shell commands (on by default). Offline mode fails soft without error noise.",
		kind:        commandNetwork,
	},
	{
		name:        "/plan",
		usage:       "/plan",
		group:       commandGroupSession,
		description: "Show planning mode status.",
		kind:        commandPlan,
	},
	{
		name:        "/ps",
		usage:       "/ps",
		group:       commandGroupRuntime,
		description: "List running background terminal sessions.",
		kind:        commandPS,
	},
	{
		name:        "/stop",
		usage:       "/stop [session_id]",
		group:       commandGroupRuntime,
		description: "Stop running background terminal sessions.",
		kind:        commandStop,
	},
	{
		name:        "/sandbox-setup",
		usage:       "/sandbox-setup",
		group:       commandGroupRuntime,
		description: "Run native sandbox setup for this platform.",
		kind:        commandSandboxSetup,
	},
	{
		name:        "/tools",
		usage:       "/tools",
		group:       commandGroupTools,
		description: "List registered tools.",
		kind:        commandTools,
	},
	{
		name:        "/skills",
		usage:       "/skills",
		group:       commandGroupTools,
		description: "List bundled and installed skills.",
		kind:        commandSkills,
	},
	{
		name:        "/context",
		usage:       "/context",
		group:       commandGroupSession,
		description: "Show current workspace and runtime context.",
		kind:        commandContext,
	},
	{
		name:        "/sec",
		usage:       "/sec [open|rescan|diff|routes|atlas|activity|tools|signals|processes|help]",
		group:       commandGroupTools,
		description: "Open the unified native security workspace: posture, risks, diff-scoped scan, live egress, tool surfaces, controls, and grounded AI guidance.",
		kind:        commandSec,
	},
	{
		name:        "/image",
		usage:       "/image <path> | clear",
		group:       commandGroupSession,
		description: "Attach image/PDF to the next message. /image view inspects Format·Dimensions·Size·Path; /image clear removes all.",
		kind:        commandImage,
	},
	{
		name:        "/add-dir",
		usage:       "/add-dir [path]",
		group:       commandGroupRuntime,
		description: "Grant write access to a directory outside the workspace for this session; bare form lists current write roots.",
		kind:        commandAddDir,
	},
	{
		name:        "/clear",
		usage:       "/clear",
		group:       commandGroupMeta,
		description: "Clear the visible transcript.",
		kind:        commandClear,
	},
	{
		name:        "/search",
		aliases:     []string{"/find"},
		usage:       "/search <query>",
		group:       commandGroupTools,
		description: "Search local session events. Requires a query argument.",
		kind:        commandSearch,
	},
	{
		name:        "/hide",
		usage:       "/hide session | off",
		group:       commandGroupSession,
		description: "Exclude this session from the auto-captured knowledge graph (/hide off re-enables).",
		kind:        commandHide,
	},
	{
		name:        "/knowledge",
		aliases:     []string{"/kb"},
		usage:       "/knowledge [view|add <title> :: <content>|search <query>|web <query>|list|stats|export|open browser]",
		group:       commandGroupTools,
		description: "Open the native Knowledge Atlas; add, list, search local BM25, explicitly search the web, inspect sources/timeline/insights, or export the optional graph.html.",
		kind:        commandKnowledge,
	},
	{
		name:        "/research",
		usage:       "/research <query>",
		group:       commandGroupTools,
		description: "Run a native, citation-preserving web research pass in the Knowledge Atlas.",
		kind:        commandResearch,
	},
	{
		name:        "/dream",
		usage:       "/dream [focus]",
		group:       commandGroupSession,
		description: "Consolidate verified session knowledge into project memory.",
		kind:        commandDream,
	},
	{
		name:        "/distill",
		usage:       "/distill [focus]",
		group:       commandGroupSession,
		description: "Find repeated workflows and propose the smallest reusable skill or command.",
		kind:        commandDistill,
	},
	{
		name:        "/evolve",
		usage:       "/evolve [focus]",
		group:       commandGroupSession,
		description: "Review memory and repeated work, then make a small verified improvement to @code.",
		kind:        commandEvolve,
	},
	{
		name:        "/enterprise",
		aliases:     []string{"/ent"},
		usage:       "/enterprise",
		group:       commandGroupSession,
		description: "Show the enterprise security layer: egress counters, persisted audit trail size, and which hardening capabilities (RBAC, redaction, SSRF guard, rate limits) are active.",
		kind:        commandEnterprise,
	},
	{
		name:        "/graph",
		aliases:     []string{"/graphify"},
		usage:       "/graph [path|explain <node>|path <a> <b>|open]",
		group:       commandGroupTools,
		description: "Map the project into a knowledge graph (graphify-style, fully local) and query it instead of grepping: explain a concept, trace paths, open the interactive graph.html.",
		kind:        commandGraph,
	},
	{
		name:        "/mcp",
		aliases:     []string{"/mcp-status"},
		usage:       "/mcp",
		group:       commandGroupTools,
		description: "Open the MCP manager to inspect servers, tools, permissions, OAuth, and connection checks.",
		kind:        commandMCP,
	},
	{
		name:        "/resume",
		aliases:     []string{"/sessions"},
		usage:       "/resume [id]",
		group:       commandGroupSession,
		description: "List recent sessions or show resume guidance.",
		kind:        commandResume,
	},
	{
		name:        "/retitle",
		usage:       "/retitle",
		group:       commandGroupSession,
		description: "Generate concise titles for resumable sessions.",
		kind:        commandRetitle,
	},
	{
		name:        "/spec",
		usage:       "/spec <task>",
		group:       commandGroupSession,
		description: "Draft an implementation spec for review before editing.",
		kind:        commandSpec,
	},
	{
		name:        "/init",
		usage:       "/init",
		group:       commandGroupSession,
		description: "Investigate the repo and generate an AGENTS.md for the agent.",
		kind:        commandInit,
	},
	{
		name:        "/compact",
		usage:       "/compact [status|instructions]",
		group:       commandGroupSession,
		description: "Compact (summarize) the session context. Pass 'status' to show state or custom text to guide the summarization.",
		kind:        commandCompact,
	},
	{
		name:        "/transcript",
		usage:       "/transcript",
		group:       commandGroupSession,
		description: "Toggle the detailed transcript view.",
		kind:        commandTranscript,
	},
	{
		name:        "/rewind",
		usage:       "/rewind [latest|<sequence>]",
		group:       commandGroupSession,
		description: "Restore workspace files to a checkpoint and truncate the session.",
		kind:        commandRewind,
	},
	{
		name:        "/effort",
		usage:       "/effort [auto|flash|super]",
		group:       commandGroupModel,
		description: "Set thinking mode: auto (agent decides per turn — default), flash (fast, concise private thinking), or super (maximum deep thinking).",
		kind:        commandEffort,
	},
	{
		name:        "/btw",
		usage:       "/btw <question>",
		group:       commandGroupSession,
		description: "Ask a quick side question without interrupting the main conversation.",
		kind:        commandBtw,
	},
	{
		name:        "/handoff",
		usage:       "/handoff [note]",
		group:       commandGroupSession,
		description: "Write a structured, lossless handoff document (request, constraints, plan, files, git state) for continuing in a fresh session.",
		kind:        commandHandoff,
	},
	{
		name:        "/style",
		usage:       "/style [list|balanced|concise|explanatory|review|caveman|normal]",
		group:       commandGroupSession,
		description: "Show or set the response style preference.",
		kind:        commandStyle,
	},
	{
		name:        "/selfcorrect",
		aliases:     []string{"/sc"},
		usage:       "/selfcorrect [status|on|off|tests|full|lsp]",
		group:       commandGroupSession,
		description: "Show or set post-edit self-correction depth (LSP-only default; on/tests/full add the project test plan; off/lsp disable tests, LSP-only).",
		kind:        commandSelfCorrect,
	},
	{
		name:        "/turns",
		usage:       "/turns [n]",
		group:       commandGroupSession,
		description: "Show or set the per-run tool-turn budget for this session (raise it for long multi-step tasks).",
		kind:        commandTurns,
	},
	{
		name:        "/help",
		usage:       "/help",
		group:       commandGroupMeta,
		description: "Show available commands.",
		kind:        commandHelp,
	},
	{
		name:        "/doctor",
		aliases:     []string{"/health"},
		usage:       "/doctor [fix|--connectivity]",
		group:       commandGroupRuntime,
		description: "Show diagnostics, open safe fixes, or run provider connectivity checks.",
		kind:        commandDoctor,
	},
	{
		name:        "/config",
		usage:       "/config [recaps on|off]",
		group:       commandGroupRuntime,
		description: "Show active configuration; toggle settings (recaps on|off).",
		kind:        commandConfig,
	},
	{
		name:        "/debug",
		aliases:     []string{"/debug-mode"},
		usage:       "/debug",
		group:       commandGroupRuntime,
		description: "Show debug mode status.",
		kind:        commandDebug,
	},
	{
		name:        "/theme",
		usage:       "/theme [list|auto|name]",
		group:       commandGroupSession,
		description: "Pick a color theme (no arg opens the picker; auto detects the terminal background).",
		kind:        commandTheme,
	},
	{
		name:        "/exit",
		aliases:     []string{"/quit"},
		usage:       "/exit",
		group:       commandGroupMeta,
		description: "Exit @code.",
		kind:        commandExit,
	},
	{
		name:        "/commit",
		usage:       "/commit [message]",
		group:       commandGroupSession,
		description: "Stage all changes and commit with an AI-generated message (or supply your own).",
		kind:        commandCommit,
	},
	{
		name:        "/trash",
		aliases:     []string{"/bin"},
		usage:       "/trash",
		group:       commandGroupSession,
		description: "List files in the soft-delete TTL bin (.atcode/ttl) with their expiry times.",
		kind:        commandTrash,
	},
	{
		name:        "/review",
		aliases:     []string{"/code-review"},
		usage:       "/review [diff|branch]",
		group:       commandGroupSession,
		description: "Ask the model to review changed files for bugs, style, and security.",
		kind:        commandReview,
	},
	{
		name:        "/memory",
		aliases:     []string{"/mem", "/ctx"},
		usage:       "/memory [32k|64k|128k|256k|list|show <file>]",
		group:       commandGroupModel,
		description: "Show or change the model context window size, then hot-restart the model server in-place. /memory 256k sets 256k context and restarts the atcode engine immediately. /memory list shows project memory files.",
		kind:        commandMemory,
	},
	{
		name:        "/export",
		usage:       "/export [path]",
		group:       commandGroupSession,
		description: "Export the current session transcript to a Markdown file.",
		kind:        commandExport,
	},
	{
		name:        "/vim",
		usage:       "/vim [on|off]",
		group:       commandGroupRuntime,
		description: "Toggle vim-style editing mode in the composer (normal/insert modes, hjkl motions, etc.).",
		kind:        commandVim,
	},
	{
		name:        "/tasks",
		usage:       "/tasks",
		group:       commandGroupSession,
		description: "Show the active task list for the current session.",
		kind:        commandTasks,
	},
	{
		name:        "/git",
		usage:       "/git [status|log|diff]",
		group:       commandGroupSession,
		description: "Quick git info: status, log, or diff for the current repo.",
		kind:        commandGit,
	},
	{
		name:        "/signout",
		aliases:     []string{"/logout", "/sign-out"},
		usage:       "/signout",
		group:       commandGroupMeta,
		description: "Sign out and clear the saved authentication session.",
		kind:        commandSignOut,
	},
	{
		name:        "/nonstop",
		aliases:     []string{"/keep-going", "/keepgoing"},
		usage:       "/nonstop [on|off]",
		group:       commandGroupSession,
		description: "Toggle nonstop mode: when on, @code auto-continues after each run while plan tasks remain open (no more 'should I continue?' pauses).",
		kind:        commandNonstop,
	},
	{
		name:        "/history",
		usage:       "/history",
		group:       commandGroupSession,
		description: "Browse all past sessions: search, press Enter to restore, d to delete.",
		kind:        commandHistory,
	},
	{
		name:        "/cd",
		aliases:     []string{"/chdir"},
		usage:       "/cd <path>",
		group:       commandGroupSession,
		description: "Change the working directory. Grants write access to the new path for this session.",
		kind:        commandCD,
	},
	{
		name:        "/browser",
		usage:       "/browser [show|hide]",
		group:       commandGroupRuntime,
		description: "Show or hide the automation browser window (headful vs headless). No args shows the current mode.",
		kind:        commandBrowser,
	},
}

func parseCommand(input string) parsedCommand {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return parsedCommand{kind: commandEmpty}
	}

	// "!cmd" is a shell escape (the footer advertises "! bash"): run it directly.
	if strings.HasPrefix(trimmed, "!") {
		return parsedCommand{kind: commandBash, text: strings.TrimSpace(trimmed[1:])}
	}

	if strings.HasPrefix(trimmed, "/") {
		name, args := splitCommand(trimmed)
		command, ok := resolveCommand(name)
		if ok {
			return parsedCommand{kind: command.kind, name: command.name, text: args}
		}
		// Keep the retired spelling as an invisible compatibility redirect.
		// It is deliberately absent from commandDefinitions so autocomplete,
		// help, and discovery expose one canonical security surface: /sec.
		if name == "/security" {
			return parsedCommand{kind: commandSec, name: "/sec", text: args}
		}
		return parsedCommand{kind: commandUnknown, text: trimmed}
	}

	return parsedCommand{kind: commandPrompt, text: trimmed}
}

func splitCommand(input string) (string, string) {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return "", ""
	}

	name := parts[0]
	args := strings.TrimSpace(input[len(name):])
	return strings.ToLower(name), args
}

func resolveCommand(name string) (commandDefinition, bool) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	for _, command := range commandDefinitions {
		if normalized == command.name {
			return command, true
		}
		for _, alias := range command.aliases {
			if normalized == alias {
				return command, true
			}
		}
	}
	return commandDefinition{}, false
}

func listCommandNames() []string {
	names := make([]string, 0, len(commandDefinitions))
	for _, command := range commandDefinitions {
		names = append(names, command.name)
		names = append(names, command.aliases...)
	}
	return names
}

func formatCommandHelpLines() []string {
	return formatGroupedCommandHelpLines()
}

func formatGroupedCommandHelpLines() []string {
	lines := make([]string, 0, len(commandDefinitions)+len(commandGroupOrder()))
	for _, group := range commandGroupOrder() {
		groupLines := commandHelpLinesForGroup(group)
		if len(groupLines) == 0 {
			continue
		}
		lines = append(lines, string(group)+":")
		lines = append(lines, groupLines...)
	}
	return lines
}

func formatGroupedCommandHelp() string {
	lines := []string{"Commands", "status: info"}
	for _, group := range commandGroupOrder() {
		groupLines := commandHelpLinesForGroup(group)
		if len(groupLines) == 0 {
			continue
		}
		lines = append(lines, commandGroupTitle(group))
		lines = append(lines, groupLines...)
	}
	lines = append(lines, "hint: submit plain text to ask @code")
	return strings.Join(lines, "\n")
}

func commandHelpLinesForGroup(group commandGroup) []string {
	lines := []string{}
	for _, command := range commandDefinitions {
		if command.group != group {
			continue
		}
		lines = append(lines, "  "+formatCommandHelpLine(command))
	}
	return lines
}

func formatCommandHelpLine(command commandDefinition) string {
	label := command.usage
	if len(command.aliases) > 0 {
		label += " (" + strings.Join(command.aliases, ", ") + ")"
	}
	return label + " - " + command.description
}

func commandSelectionRequiresInput(name string) bool {
	command, ok := resolveCommand(name)
	return ok && commandUsageRequiresInput(command.usage)
}

func commandRequiredInputHint(name string) string {
	command, ok := resolveCommand(name)
	if !ok {
		return ""
	}
	placeholder := commandUsageRequiredPlaceholder(command.usage)
	if placeholder == "" {
		return ""
	}
	return "[" + placeholder + "]"
}

func commandUsageRequiresInput(usage string) bool {
	return commandUsageRequiredPlaceholder(usage) != ""
}

func commandUsageRequiredPlaceholder(usage string) string {
	optionalDepth := 0
	var placeholder strings.Builder
	inPlaceholder := false
	for _, char := range usage {
		if inPlaceholder {
			if char == '>' {
				return strings.TrimSpace(placeholder.String())
			}
			placeholder.WriteRune(char)
			continue
		}
		switch char {
		case '[':
			optionalDepth++
		case ']':
			if optionalDepth > 0 {
				optionalDepth--
			}
		case '<':
			if optionalDepth == 0 {
				inPlaceholder = true
			}
		}
	}
	return ""
}

func commandGroupOrder() []commandGroup {
	return []commandGroup{
		commandGroupModel,
		commandGroupSession,
		commandGroupRuntime,
		commandGroupTools,
		commandGroupMeta,
	}
}

func commandGroupTitle(group commandGroup) string {
	switch group {
	case commandGroupModel:
		return "Model"
	case commandGroupSession:
		return "Session"
	case commandGroupRuntime:
		return "Runtime"
	case commandGroupTools:
		return "Tools"
	case commandGroupMeta:
		return "Meta"
	default:
		return string(group)
	}
}
