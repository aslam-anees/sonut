package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	sonutSandbox "github.com/aistatine/sonut/internal/sandbox"
	"github.com/aistatine/sonut/internal/secrets"
	"github.com/aistatine/sonut/internal/security"
)

const defaultBashTimeoutMS = 120000
const maxBashTimeoutMS = 600000

type bashTool struct {
	baseTool
	workspaceRoot string
	scope         PathScope
}

func NewBashTool(workspaceRoot string) Tool {
	return NewScopedBashTool(workspaceRoot, nil)
}

func NewScopedBashTool(workspaceRoot string, scope PathScope) Tool {
	shellGuidance := shellGuidanceForGOOS(runtime.GOOS)
	return bashTool{
		baseTool: baseTool{
			name:        "bash",
			description: "Execute a shell command inside the workspace (or an explicitly granted extra directory) after permission is granted. " + shellGuidance,
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"command":             {Type: "string", Description: "Shell command to execute using the host shell. " + shellGuidance},
					"cwd":                 {Type: "string", Description: "Directory to run the command in. Relative paths stay in the workspace; use an absolute path to run in a granted extra directory. Defaults to workspace root. Prefer cwd over cd when changing directories.", Default: "."},
					"timeout_ms":          {Type: "integer", Description: "Command timeout in milliseconds.", Default: defaultBashTimeoutMS, Minimum: intPtr(1), Maximum: intPtr(maxBashTimeoutMS)},
					"sandbox_permissions": {Type: "string", Enum: []string{string(SandboxPermissionsUseDefault), string(SandboxPermissionsWithAdditionalPermissions), string(SandboxPermissionsRequireEscalated)}, Description: "Per-command sandbox override. Defaults to `use_default`; use `with_additional_permissions` with `additional_permissions` for sandboxed file/network access, or `require_escalated` only when the command must run outside the sandbox, such as host/global process, socket, service, or desktop state hidden by sandbox namespaces.", Default: string(SandboxPermissionsUseDefault)},
					"additional_permissions": {
						Type:        "object",
						Description: "Sandboxed filesystem or network access for this command; only with `sandbox_permissions: \"with_additional_permissions\"`.",
						Properties:  additionalPermissionsProperties(),
					},
					"justification": {Type: "string", Description: "User-facing approval question for `require_escalated`; omit otherwise."},
					"prefix_rule":   {Type: "array", Items: &PropertySchema{Type: "string"}, Description: "Reusable approval prefix for this command, only with `sandbox_permissions: \"require_escalated\"`; keep it narrow, for example [\"git\", \"pull\"]."},
				},
				Required:             []string{"command"},
				AdditionalProperties: false,
			},
			safety: promptSafety(SideEffectShell, "Shell commands can read, write, or execute programs."),
		},
		workspaceRoot: normalizeWorkspaceRoot(workspaceRoot),
		scope:         scope,
	}
}

func (tool bashTool) Run(ctx context.Context, args map[string]any) Result {
	return tool.run(ctx, args, nil, "", security.OutboundMetadata{})
}

func (tool bashTool) RunWithSandbox(ctx context.Context, args map[string]any, engine *sonutSandbox.Engine) Result {
	return tool.run(ctx, args, engine, "", security.OutboundMetadata{})
}

func (tool bashTool) RunWithFullOptions(ctx context.Context, args map[string]any, options RunOptions) Result {
	return tool.run(ctx, args, options.Sandbox, options.Cwd, security.OutboundMetadata{SessionID: options.SessionID, ToolCallID: options.ToolCallID})
}

func (tool bashTool) run(ctx context.Context, args map[string]any, engine *sonutSandbox.Engine, runCwd string, metadata security.OutboundMetadata) Result {
	commandText, err := aliasedStringArg(args, []string{"command", "cmd", "script", "shell"}, "", true, false)
	if err != nil {
		return errorResult("Error: Invalid arguments for bash: " + err.Error())
	}
	cwd, err := stringArg(args, "cwd", ".", false)
	if err != nil {
		return errorResult("Error: Invalid arguments for bash: " + err.Error())
	}
	timeoutMS, err := intArg(args, "timeout_ms", defaultBashTimeoutMS, 1, maxBashTimeoutMS)
	if err != nil {
		return errorResult("Error: Invalid arguments for bash: " + err.Error())
	}
	sandboxPermissions, err := sandboxPermissionsArg(args)
	if err != nil {
		return errorResult("Error: Invalid arguments for bash: " + err.Error())
	}
	if issue := detectShellCommandIssue(commandText, runtime.GOOS); issue != nil {
		return shellIssueBlockResult(*issue)
	}

	// Rewrite background commands (trailing &) before detectBashBackgroundIssue
	// so the rewritten form (no trailing &, setsid-based) is what the rest of the
	// pipeline sees. The old path blocked & entirely and redirected to exec_command;
	// now we handle it natively for simple cases.
	if isBackgroundCommand(commandText) {
		commandText, _ = rewriteForBackground(commandText)
	}

	if issue := detectBashBackgroundIssue(commandText); issue != nil {
		return shellIssueBlockResult(*issue)
	}

	// Pre-execution safety: refuse interactive commands (editors, pagers, REPLs,
	// remote shells, etc.) that would hang the non-interactive agent until the
	// timeout fires. This runs before the command is built or launched.
	if interactive := sonutSandbox.DetectInteractiveCommand(commandText, runtime.GOOS); interactive.Interactive {
		return interactiveBlockResult(interactive)
	}

	absoluteCwd, relativeCwd, err := resolveScopedPath(tool.workspaceRoot, tool.scope, cwd, runCwd)
	if err != nil {
		return errorResult("Error running bash: " + err.Error())
	}
	if blockErr := flagExecCommandEgressWithMetadata(commandText, absoluteCwd, metadata); blockErr != nil {
		return errorResult("Error: bash blocked by security policy: " + blockErr.Error())
	}

	commandCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	meta := map[string]string{
		"cwd":        relativeCwd,
		"timeout_ms": strconv.Itoa(timeoutMS),
	}
	commandEngine := commandEngineForSandboxPermissions(engine, sandboxPermissions)
	if commandEngine == nil && sandboxPermissions == SandboxPermissionsRequireEscalated {
		meta["sandbox_permissions"] = string(SandboxPermissionsRequireEscalated)
	}
	command, plan, err := buildBashCommand(commandCtx, commandText, absoluteCwd, commandEngine)
	if err != nil {
		meta["exit_code"] = "-1"
		return Result{
			Status: StatusError,
			Output: "Error preparing sandboxed bash command: " + err.Error(),
			Meta:   meta,
		}
	}
	// Release any plan-scoped resources once the command has finished running.
	defer plan.Cleanup()
	addSandboxMeta(meta, plan)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	// Kill the shell as a process group on timeout and bound the post-kill I/O
	// wait, so a backgrounded child cannot outlive the command or hang Run().
	hardenProcessLifetime(command)

	// Capture sandbox denials when the plan opted in (macOS + Policy.MonitorDenials).
	// A no-op when MonitorTag is empty, so the default path is unchanged.
	monitor := sonutSandbox.StartDenialMonitor(context.Background(), plan.MonitorTag)
	err = command.Run()
	exitCode := commandExitCode(err)
	meta["exit_code"] = strconv.Itoa(exitCode)
	stderrText := appendSandboxBlocks(stderr.String(), monitor.Stop())

	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		return Result{
			Status: StatusError,
			Output: fmt.Sprintf("Error: Command timed out after %dms.", timeoutMS),
			Meta:   meta,
		}
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// A background child kept the shell's I/O pipes open past WaitDelay; the
		// group was killed and Go returned ErrWaitDelay. Any partial output is still
		// useful, so surface it together with a clear explanation.
		return Result{
			Status: StatusError,
			Output: "Error: a background process held the shell I/O pipes open past the wait deadline and was killed. " +
				"Use exec_command (without &) for servers and watchers — it keeps the process running in a persistent session.\n" +
				formatBashOutputForCommand(commandText, stdout.String(), stderrText, exitCode),
			Meta: meta,
		}
	}
	if err != nil {
		if exitCode < 0 {
			return Result{
				Status: StatusError,
				Output: "Error executing command: " + err.Error(),
				Meta:   meta,
			}
		}
		markLikelySandboxDenial(meta, plan, exitCode, stdout.String(), stderrText)
		return Result{
			Status: StatusError,
			Output: formatBashOutputWithShellHint(commandText, stdout.String(), stderrText, exitCode, meta),
			Meta:   meta,
		}
	}

	markLikelySandboxDenial(meta, plan, exitCode, stdout.String(), stderrText)
	if meta[SandboxLikelyDeniedMeta] == "true" {
		return Result{
			Status: StatusError,
			Output: formatBashOutputWithShellHint(commandText, stdout.String(), stderrText, exitCode, meta),
			Meta:   meta,
		}
	}
	return Result{
		Status: StatusOK,
		Output: formatBashOutputForCommand(commandText, stdout.String(), stderrText, exitCode),
		Meta:   meta,
	}
}

func commandEngineForSandboxPermissions(engine *sonutSandbox.Engine, sandboxPermissions SandboxPermissionOverride) *sonutSandbox.Engine {
	if sandboxPermissions == SandboxPermissionsRequireEscalated && (engine == nil || engine.UnsandboxedExecutionAllowed()) {
		return nil
	}
	return engine
}

// appendSandboxBlocks appends a <sandbox_blocks> block listing the denials
// the sandbox log monitor captured, so the model can see what was blocked. With no
// blocks the stderr is returned unchanged.
func appendSandboxBlocks(stderr string, blocks []string) string {
	if len(blocks) == 0 {
		return stderr
	}
	var builder strings.Builder
	builder.WriteString("<sandbox_blocks>\n")
	for _, block := range blocks {
		builder.WriteString(block)
		builder.WriteString("\n")
	}
	builder.WriteString("</sandbox_blocks>")
	if strings.TrimSpace(stderr) == "" {
		return builder.String()
	}
	return stderr + "\n" + builder.String()
}

func shellIssueBlockResult(issue shellIssue) Result {
	return Result{
		Status: StatusError,
		Output: appendShellIssueHint("", issue),
		Meta: map[string]string{
			"exit_code":   "-1",
			"shell_issue": issue.Kind,
		},
		Display: Display{
			Summary: issue.Message,
			Kind:    "shell",
		},
	}
}

func buildBashCommand(ctx context.Context, commandText string, absoluteCwd string, engine *sonutSandbox.Engine) (*exec.Cmd, sonutSandbox.CommandPlan, error) {
	spec := sonutSandbox.CommandSpec{
		Name: shellExecutable(),
		Args: shellArguments(commandText),
		Dir:  absoluteCwd,
	}
	if engine != nil {
		return engine.CommandContext(ctx, spec)
	}
	plan := sonutSandbox.CommandPlan{
		Backend: sonutSandbox.Backend{
			Name:    sonutSandbox.BackendUnavailable,
			Message: "sandbox engine not provided",
		},
		Wrapped: false,
		Name:    spec.Name,
		Args:    spec.Args,
		Dir:     spec.Dir,
	}
	command := exec.CommandContext(ctx, spec.Name, spec.Args...)
	command.Dir = spec.Dir
	return command, plan, nil
}

func addSandboxMeta(meta map[string]string, plan sonutSandbox.CommandPlan) {
	if plan.Backend.Name == "" {
		return
	}
	meta["sandbox_backend"] = string(plan.Backend.Name)
	if plan.TargetBackend != "" {
		meta["sandbox_target_backend"] = string(plan.TargetBackend)
	}
	meta["sandbox_wrapped"] = strconv.FormatBool(plan.Wrapped)
	if plan.EnforcementLevel != "" {
		meta["sandbox_enforcement_level"] = string(plan.EnforcementLevel)
	}
	if plan.DowngradeReason != "" {
		meta["sandbox_downgrade_reason"] = plan.DowngradeReason
	}
	meta["sandbox_requires_platform"] = strconv.FormatBool(plan.RequiresPlatformSandbox)
	if plan.Backend.Message != "" {
		meta["sandbox_message"] = plan.Backend.Message
	}
	if plan.SandboxDir != "" {
		meta["sandbox_cwd"] = plan.SandboxDir
	}
}

// interactiveBlockResult builds the structured tool Result returned when a
// command is refused before execution because it would hang the agent. The
// block is surfaced both in Output (clearly delimited) and in Meta/Display
// so downstream consumers and the TUI can render it consistently.
func interactiveBlockResult(detection sonutSandbox.InteractiveCommandResult) Result {
	message := fmt.Sprintf(
		"Error: Blocked interactive command %q before execution: %s. This would hang the non-interactive agent.\nSuggestion: %s",
		detection.Command, detection.Reason, detection.Suggestion,
	)
	return Result{
		Status: StatusError,
		Output: message,
		Meta: map[string]string{
			"exit_code":    "-1",
			"safety_block": "interactive_command",
			"safety_cmd":   detection.Command,
		},
		Display: Display{
			Summary: "Blocked interactive command: " + detection.Command,
			Kind:    "shell",
		},
	}
}

func shellExecutable() string {
	if runtime.GOOS == "windows" {
		return "cmd.exe"
	}
	return "/bin/bash"
}

func shellArguments(command string) []string {
	if runtime.GOOS == "windows" {
		return []string{"/d", "/s", "/c", command}
	}
	return []string{"-c", command}
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}

func formatBashOutput(stdout string, stderr string, exitCode int) string {
	return formatBashOutputForCommand("", stdout, stderr, exitCode)
}

// formatBashOutputForCommand formats stdout/stderr for the model view: secrets
// redaction, then MiMo-style token-efficient cleansing (ANSI/progress/shapes).
// The never-worse gate keeps output safe when cleansing does not shrink bytes.
func formatBashOutputForCommand(command, stdout, stderr string, exitCode int) string {
	parts := []string{}
	stdout = strings.TrimRight(stdout, "\r\n")
	stderr = strings.TrimRight(stderr, "\r\n")
	// Redact high-confidence secrets a command may have printed, so they are not
	// echoed back into the model context (additive to the configured-key scrub
	// done at the registry boundary).
	stdout, outFindings := secrets.Redact(stdout)
	stderr, errFindings := secrets.Redact(stderr)
	// Token-efficient clean for the LLM view (progress bars, ANSI, long lines,
	// command-shaped noise). Opt-out: ATCODE_BASH_RAW=1 or "# nofilter"/"# raw".
	if cleaned := CleanForModel(stdout, CleanOptions{Command: command}); cleaned.BytesOut < cleaned.BytesIn {
		stdout = cleaned.Text
	}
	if cleaned := CleanForModel(stderr, CleanOptions{Command: command}); cleaned.BytesOut < cleaned.BytesIn {
		stderr = cleaned.Text
	}
	if stdout != "" {
		parts = append(parts, "stdout:\n"+stdout)
	}
	if stderr != "" {
		parts = append(parts, "stderr:\n"+stderr)
	}
	if exitCode != 0 {
		parts = append(parts, fmt.Sprintf("exit_code: %d", exitCode))
	}
	if n := len(outFindings) + len(errFindings); n > 0 {
		parts = append(parts, fmt.Sprintf("[@code] redacted %d likely secret(s) from this output before showing it.", n))
	}
	if len(parts) == 0 {
		return "Command completed with no output."
	}
	return strings.Join(parts, "\n")
}

func formatBashOutputWithShellHint(command string, stdout string, stderr string, exitCode int, meta map[string]string) string {
	output := formatBashOutputForCommand(command, stdout, stderr, exitCode)
	if issue := detectShellOutputIssue(command, stdout+"\n"+stderr, runtime.GOOS); issue != nil {
		meta["shell_issue"] = issue.Kind
		output = appendShellIssueHint(output, *issue)
	}
	return output
}
