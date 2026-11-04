package tools

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/aistatine/sonut/internal/redaction"
	"github.com/aistatine/sonut/internal/sandbox"
	"github.com/aistatine/sonut/internal/streamjson"
)

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

type RunOptions struct {
	PermissionGranted bool
	PermissionMode    string
	Autonomy          string
	Sandbox           *sandbox.Engine
	ToolCallID        string
	SessionID         string
	Model             string
	ReasoningEffort   string
	Depth             int
	Cwd               string
	// FileTracker, when set, records the version of each file read or written this
	// session so write_file/edit_file can refuse to clobber a file that changed on
	// disk outside @code since it was last read. nil disables the feature entirely
	// (the read/write tools behave exactly as before).
	FileTracker *FileTracker
	// EnabledTools / DisabledTools carry the run's operator tool filters so a
	// filter-aware tool (tool_search) never discloses or loads an operator-hidden
	// tool. They use the same allow/deny semantics as the agent's filter gate:
	// denied if listed in DisabledTools; if EnabledTools is non-empty, allowed
	// only when listed in it.
	EnabledTools  []string
	DisabledTools []string
	// Progress, when set, is called with each stream-json event emitted by a
	// specialist child process while it runs. Used by the TUI to show live
	// tool-call progress in the specialist card. nil is a no-op (the default
	// for every non-Task tool).
	Progress func(streamjson.Event)
	// ReportProgress carries bounded, typed progress from a tool implementation.
	// It is display/stream metadata only and is never appended to model context.
	ReportProgress func(Progress)
	// OnConfirmation, when set, is called by write_file to ask the user for
	// confirmation before overwriting an existing file. Returns true if confirmed,
	// false if denied. If nil or if the context is not interactive, write_file
	// falls back to returning an error message to the model.
	OnConfirmation func(context.Context, string) (bool, error)
	// PrecomputedCommit is used by streaming file tools. The agent has already
	// run the normal argument, permission, sandbox, and beforeTool checks; the
	// registry invokes this closure at the mutation boundary so the staged file
	// is promoted only after those checks succeed. It is intentionally optional
	// and has no effect on ordinary tool calls.
	PrecomputedCommit func() Result
}

type sandboxAwareTool interface {
	RunWithSandbox(ctx context.Context, args map[string]any, engine *sandbox.Engine) Result
}

type optionsAwareTool interface {
	RunWithOptions(ctx context.Context, args map[string]any, options RunOptions) Result
}

// fullOptionsAwareTool is used by tools that need both the per-run cwd and the
// sandbox engine. It prevents a lossy either/or dispatch through
// optionsAwareTool versus sandboxAwareTool.
type fullOptionsAwareTool interface {
	RunWithFullOptions(ctx context.Context, args map[string]any, options RunOptions) Result
}

// deferredTool is an optional interface a tool implements to mark itself
// deferred-eligible: when many such tools are registered, the agent loop may
// withhold their full schema and advertise them via tool_search instead.
type deferredTool interface {
	Deferred() bool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// IsDeferred reports whether a tool opts into deferred loading. A tool is
// deferred-eligible only if it implements deferredTool and its Deferred()
// method returns true; tools that do not implement the interface are eager.
func IsDeferred(t Tool) bool {
	d, ok := t.(deferredTool)
	return ok && d.Deferred()
}

// deferralEligibleTool lets a tool keep counting toward the deferral threshold
// even when it is dynamically exposed eagerly (Deferred()==false). A tool whose
// Deferred() flips false at runtime — e.g. the swarm coordination tools once a
// swarm is active — would otherwise lower the global eligible count and risk
// dropping it below DeferThreshold, which would deactivate deferral for ALL
// tools (force-exposing every MCP schema). Implementing this keeps the count
// stable so un-deferring one tool can never force-expose others.
type deferralEligibleTool interface {
	DeferralEligible() bool
}

// IsDeferralEligible reports whether a tool counts toward the DeferThreshold.
// A currently-deferred tool always counts. A tool may ALSO opt in via
// DeferralEligible() to keep counting while exposed eagerly (see above). This is
// the count partitionTools uses for the active-gate; whether a tool is actually
// hidden is still decided by IsDeferred.
func IsDeferralEligible(t Tool) bool {
	if IsDeferred(t) {
		return true
	}
	d, ok := t.(deferralEligibleTool)
	return ok && d.DeferralEligible()
}

func (registry *Registry) Register(tool Tool) {
	if registry == nil || tool == nil {
		return
	}
	name := strings.TrimSpace(tool.Name())
	if name == "" {
		return
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.tools[name] = tool
}

func (registry *Registry) Get(name string) (Tool, bool) {
	if registry == nil {
		return nil, false
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	tool, ok := registry.tools[name]
	return tool, ok
}

// toolNameAliases maps tool names that models trained on other harnesses
// commonly emit to the canonical @code tool, so a near-miss call dispatches
// instead of dying with "Unknown tool". Only unambiguous mappings belong here.
var toolNameAliases = map[string]string{
	"shell": "exec_command", "run_command": "exec_command", "run": "exec_command",
	"execute": "exec_command", "execute_command": "exec_command",
	"run_terminal_cmd": "exec_command", "terminal_cmd": "exec_command",
	"read": "read_file", "cat": "read_file", "view": "read_file",
	"view_file": "read_file", "open_file": "read_file",
	"write": "write_file", "create_file": "write_file",
	"write_to_file": "write_file", "save_file": "write_file",
	"edit": "edit_file", "str_replace": "edit_file", "str_replace_editor": "edit_file",
	"search_replace": "edit_file", "replace_in_file": "edit_file",
	"ls": "list_directory", "list_files": "list_directory", "list_dir": "list_directory",
	"search": "grep", "grep_search": "grep", "code_search": "grep", "ripgrep": "grep",
	"find_files": "glob", "file_search": "glob", "glob_search": "glob",
	"fetch": "web_fetch", "fetch_url": "web_fetch", "http_get": "web_fetch",
	"search_web": "web_search", "google_search": "web_search",
	"todo_write": "update_plan", "write_todos": "update_plan", "set_plan": "update_plan",
	"browser": "web_browser", "playwright": "web_browser",
}

// Resolve canonicalizes a model-emitted tool name: exact match wins, then a
// case/whitespace-normalized match, then the alias table. Unknown names are
// returned unchanged so the caller's error path still names what was asked.
func (registry *Registry) Resolve(name string) string {
	if registry == nil {
		return name
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if _, ok := registry.tools[name]; ok {
		return name
	}
	normalized := strings.ToLower(strings.TrimSpace(name))
	if _, ok := registry.tools[normalized]; ok {
		return normalized
	}
	if canonical, ok := toolNameAliases[normalized]; ok {
		if _, ok := registry.tools[canonical]; ok {
			return canonical
		}
	}
	if canonical := registry.resolveMCPAliasLocked(normalized); canonical != "" {
		return canonical
	}
	return name
}

// resolveMCPAliasLocked recovers the unambiguous near-misses local models
// commonly emit for MCP tools:
//   - mcp__server__tool (the widespread double-underscore spelling)
//   - mcp_server_search when the remote name is server_search
//   - the bare remote tool name when only one configured server provides it
//
// Ambiguous matches deliberately return empty so dispatch still fails closed.
func (registry *Registry) resolveMCPAliasLocked(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if strings.HasPrefix(name, "mcp__") {
		name = "mcp_" + strings.ReplaceAll(strings.TrimPrefix(name, "mcp__"), "__", "_")
	}
	var matches []string
	for candidate := range registry.tools {
		canonical := strings.ToLower(candidate)
		if !strings.HasPrefix(canonical, "mcp_") {
			continue
		}
		matched := canonical == name
		if !matched && strings.HasPrefix(name, "mcp_") {
			// A model may collapse a repeated server prefix:
			// mcp_firecrawl_search -> mcp_firecrawl_firecrawl_search.
			rest := strings.TrimPrefix(name, "mcp_")
			server, short, ok := strings.Cut(rest, "_")
			if ok {
				matched = strings.HasPrefix(canonical, "mcp_"+server+"_") &&
					(strings.HasSuffix(canonical, "_"+short) || strings.HasSuffix(canonical, "_"+server+"_"+short))
			}
		}
		if !matched && !strings.HasPrefix(name, "mcp_") {
			matched = strings.HasSuffix(canonical, "_"+name)
		}
		if matched {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

// availableToolNames is the sorted registry listing embedded in unknown-tool
// errors so the model can self-correct with a real name on the next call.
func (registry *Registry) availableToolNames() string {
	if registry == nil {
		return ""
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	names := make([]string, 0, len(registry.tools))
	for name := range registry.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func (registry *Registry) All() []Tool {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	tools := make([]Tool, 0, len(registry.tools))
	names := make([]string, 0, len(registry.tools))
	for name := range registry.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		tools = append(tools, registry.tools[name])
	}
	return tools
}

func (registry *Registry) Run(ctx context.Context, name string, args map[string]any) Result {
	return registry.RunWithOptions(ctx, name, args, RunOptions{})
}

func (registry *Registry) RunWithOptions(ctx context.Context, name string, args map[string]any, options RunOptions) (result Result) {
	// Every return path passes through scrubResultSecrets exactly once, so denial,
	// permission, and unknown-tool error messages (which can echo secret-bearing
	// args/paths) are redacted at the boundary just like tool output.
	defer func() {
		result = scrubResultSecrets(result)
		// Lifecycle metadata is intentionally derived after redaction. This keeps
		// progress/UI diagnostics useful without creating a second secret-leak
		// path beside Result.Output.
		NormalizeResultLifecycle(&result, ctx.Err())
	}()

	name = registry.Resolve(name)
	tool, ok := registry.Get(name)
	if !ok {
		message := `Error: Unknown tool "` + name + `".`
		if available := registry.availableToolNames(); available != "" {
			message += " Use one of the available tools exactly as named: " + available + "."
		}
		return errorResult(message)
	}
	if rejecter, ok := tool.(PrePermissionRejecter); ok {
		if res, rejected := rejecter.RejectBeforePermission(args); rejected {
			return res
		}
	}
	// GUI browser-open commands: open the host browser (after yes when needed)
	// instead of dying inside the sandbox as outside_workspace / NSOSStatusError
	// and burning turns of thinking.
	if res, intercepted := registry.interceptBrowserOpen(ctx, name, args, options); intercepted {
		return res
	}

	permission := effectiveToolPermission(tool, args)
	sandboxGrantAuthorized := false
	var sandboxDecision *sandbox.Decision
	if options.Sandbox != nil {
		d := options.Sandbox.Evaluate(ctx, sandbox.Request{
			ToolName:          name,
			SideEffect:        sandbox.SideEffect(tool.Safety().SideEffect),
			Permission:        sandbox.Permission(permission),
			PermissionGranted: options.PermissionGranted,
			PermissionMode:    sandbox.PermissionMode(options.PermissionMode),
			Args:              args,
			Reason:            tool.Safety().Reason,
		})
		sandboxDecision = &d
		if d.Action == sandbox.ActionDeny {
			res := errorResult(d.ErrorString())
			res.SandboxDecision = sandboxDecision
			return res
		}
		if d.Action == sandbox.ActionPrompt && !options.PermissionGranted {
			res := errorResult("Error: Sandbox approval required for " + name + ": " + d.Reason)
			res.SandboxDecision = sandboxDecision
			return res
		}
		// A persistent grant OR a sandbox auto-allow authorizes a prompt tool to run
		// without a separately-recorded PermissionGranted; the sandbox is the safety
		// boundary.
		sandboxGrantAuthorized = d.Action == sandbox.ActionAllow && (d.GrantMatched || d.AutoAllowed)
	}

	switch permission {
	case PermissionAllow:
	case PermissionPrompt:
		if !options.PermissionGranted && !sandboxGrantAuthorized {
			res := errorResult("Error: Permission required for " + name + ": " + tool.Safety().Reason + ` The tool is marked "prompt" and was not executed.`)
			res.SandboxDecision = sandboxDecision
			return res
		}
	default:
		res := errorResult("Error: Permission denied for " + name + ": " + tool.Safety().Reason)
		res.SandboxDecision = sandboxDecision
		return res
	}
	if options.PrecomputedCommit != nil {
		res := options.PrecomputedCommit()
		// Streaming edit sessions capture the file before the model's whole tool
		// batch executes. If an earlier write/edit in that same batch changed the
		// target, the prepared closure is intentionally stale. Let edit_file run
		// once against the current bytes; its normal exact/fuzzy/conflict checks
		// decide whether that rebase is safe. Without this escape hatch the model
		// receives a false commit failure and commonly retries the same old_string.
		if name == "edit_file" && res.Status == StatusError &&
			strings.Contains(res.Output, ErrFileChangedOnDisk.Error()) {
			options.PrecomputedCommit = nil
		} else {
			if res.SandboxDecision == nil {
				res.SandboxDecision = sandboxDecision
			}
			return res
		}
	}

	// Shell commands frequently run formatters/code generators that rewrite a
	// file previously read by @code. Treat those command-owned changes as the new
	// session baseline so the next surgical edit does not false-fail as an
	// "external stale file". Stat before/after and read only paths that changed.
	shellSnapshot := map[string]trackedDiskState(nil)
	if tool.Safety().SideEffect == SideEffectShell && options.FileTracker != nil {
		shellSnapshot = options.FileTracker.snapshotDiskState()
		defer func() {
			// A failed command may have made only a partial, unknown mutation.
			// Keep the old baseline in that case so later writes fail closed.
			if result.Status == StatusOK {
				options.FileTracker.refreshChangedSince(shellSnapshot)
			}
		}()
	}

	if optioned, ok := tool.(fullOptionsAwareTool); ok {
		res := optioned.RunWithFullOptions(ctx, args, options)
		if res.SandboxDecision == nil {
			res.SandboxDecision = sandboxDecision
		}
		return res
	}

	if optioned, ok := tool.(optionsAwareTool); ok {
		res := optioned.RunWithOptions(ctx, args, options)
		if res.SandboxDecision == nil {
			res.SandboxDecision = sandboxDecision
		}
		return res
	}

	if options.Sandbox != nil {
		if sandboxed, ok := tool.(sandboxAwareTool); ok {
			res := sandboxed.RunWithSandbox(ctx, args, options.Sandbox)
			res.SandboxDecision = sandboxDecision
			return res
		}
	}
	res := tool.Run(ctx, args)
	res.SandboxDecision = sandboxDecision
	return res
}

func effectiveToolPermission(tool Tool, args map[string]any) Permission {
	if permissioner, ok := tool.(ArgsPermissioner); ok {
		return permissioner.PermissionForArgs(args)
	}
	return tool.Safety().Permission
}

// scrubResultSecrets redacts known secret forms from every user-visible result
// field at the registry boundary, so structured diagnostics cannot create a
// second secret-leak path beside the model-facing output.
func scrubResultSecrets(res Result) Result {
	if scrubbed := redaction.RedactString(res.Output, redaction.Options{}); scrubbed != res.Output {
		res.Output = scrubbed
		res.Redacted = true
	}
	// Display.Summary can echo command/output fragments, so scrub it too: a caller
	// that prefers Display must not bypass the boundary redaction.
	if scrubbed := redaction.RedactString(res.Display.Summary, redaction.Options{}); scrubbed != res.Display.Summary {
		res.Display.Summary = scrubbed
		res.Redacted = true
	}
	// Display.Preview is a file head / diff that can contain secrets, so scrub it at
	// the same boundary as Output/Summary before it reaches the card.
	if scrubbed := redaction.RedactString(res.Display.Preview, redaction.Options{}); scrubbed != res.Display.Preview {
		res.Display.Preview = scrubbed
		res.Redacted = true
	}
	// Meta values carry model-controlled strings (e.g. glob pattern, bash cwd) and
	// are forwarded into the transcript, so they are part of the boundary too.
	for key, value := range res.Meta {
		if scrubbed := redaction.RedactString(value, redaction.Options{}); scrubbed != value {
			res.Meta[key] = scrubbed
			res.Redacted = true
		}
	}
	if res.Error != nil {
		original := *res.Error
		res.Error.Message = redaction.RedactString(res.Error.Message, redaction.Options{})
		res.Error.Details = redaction.RedactString(res.Error.Details, redaction.Options{})
		res.Error.Hint = redaction.RedactString(res.Error.Hint, redaction.Options{})
		if res.Error.Message != original.Message || res.Error.Details != original.Details || res.Error.Hint != original.Hint {
			res.Redacted = true
		}
	}
	return res
}

func CoreReadOnlyTools(workspaceRoot string) []Tool {
	return CoreReadOnlyToolsScoped(workspaceRoot, nil)
}
func CoreReadOnlyToolsScoped(workspaceRoot string, scope PathScope) []Tool {
	return []Tool{
		NewScopedReadFileTool(workspaceRoot, scope),
		// Batch multi-file read (qwen-code read_many_files) — one round-trip for
		// several files under a shared byte budget; local-hybrid eager.
		NewScopedReadManyFilesTool(workspaceRoot, scope),
		NewScopedReadMinifiedFileTool(workspaceRoot, scope),
		NewScopedListDirectoryTool(workspaceRoot, scope),
		NewScopedGlobTool(workspaceRoot, scope),
		NewScopedGrepTool(workspaceRoot, scope),
		// lsp_navigate is semantic code navigation (definition/references/impl/
		// workspace_symbol) via the language server; read-only, scoped to the
		// workspace, and degrades to a clear "unavailable" when no server is
		// installed for the file type.
		NewScopedLSPNavigateTool(workspaceRoot, scope),
		// skill reads reusable instruction files from the skills dir (it resolves
		// skills.DefaultDir itself); read-only, so it is safe in the core/MCP set.
		NewSkillTool(""),
		NewAskUserTool(),
		NewRequestPermissionsTool(),
	}
}

func CoreWriteTools(workspaceRoot string) []Tool { return CoreWriteToolsScoped(workspaceRoot, nil) }
func CoreWriteToolsScoped(workspaceRoot string, scope PathScope) []Tool {
	return []Tool{
		NewScopedWriteFileTool(workspaceRoot, scope),
		NewScopedAppendFileTool(workspaceRoot, scope),
		NewScopedEditFileTool(workspaceRoot, scope),
		NewScopedApplyPatchTool(workspaceRoot, scope),
		NewUpdatePlanTool(),
	}
}

func CoreShellTools(workspaceRoot string) []Tool { return CoreShellToolsScoped(workspaceRoot, nil) }
func CoreShellToolsScoped(workspaceRoot string, scope PathScope) []Tool {
	execManager := newExecSessionManager()
	return []Tool{
		NewScopedExecCommandTool(workspaceRoot, scope, execManager),
		NewWriteStdinTool(execManager),
		NewScopedBashTool(workspaceRoot, scope),
	}
}

func CoreNetworkTools() []Tool {
	tools := []Tool{NewWebFetchTool()}
	// Only offer the built-in web_search when a backend is actually configured.
	// Registering it unconfigured makes the model waste calls (and high-risk
	// permission prompts) on a tool that can only return "no backend configured";
	// an MCP-provided search tool (e.g. Exa) stands on its own without it.
	if defaultSearchBackend() != nil {
		tools = append(tools, NewWebSearchTool())
	}
	return tools
}

func CoreTools(workspaceRoot string) []Tool { return CoreToolsScoped(workspaceRoot, nil) }
func CoreToolsScoped(workspaceRoot string, scope PathScope) []Tool {
	tools := append([]Tool{}, CoreReadOnlyToolsScoped(workspaceRoot, scope)...)
	tools = append(tools, CoreWriteToolsScoped(workspaceRoot, scope)...)
	tools = append(tools, CoreShellToolsScoped(workspaceRoot, scope)...)
	tools = append(tools, CoreNetworkTools()...)
	return tools
}
