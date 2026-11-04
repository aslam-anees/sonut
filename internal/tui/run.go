package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"

	"github.com/aistatine/sonut/internal/agent"
	"github.com/aistatine/sonut/internal/llama"
	"github.com/aistatine/sonut/internal/wakelock"
)

// notifyTerminal is where the notifier's terminal escape sequences (bell,
// OSC-9) are written. It defaults to os.Stderr and is repointed at the SAVED
// real terminal when Run parks fd 2 in a log file — the notifier must keep
// talking to the terminal emulator, not the log.
var notifyTerminal io.Writer = os.Stderr

// Run starts the @code Bubble Tea shell and returns a process-style exit code.
func Run(ctx context.Context, options Options) int {
	startTime := time.Now()
	// Always reap owned llama-server processes on the way out — covers normal
	// quit, startup cancel, panic recovery paths that still unwind defer, and
	// cases where model.quit was not reached (e.g. program.Run error).
	defer llama.StopOwnedServers()
	defer func() {
		dumpTrace()
		if atcodeEnv("STARTUP_TRACE") == "1" {
			fmt.Fprintf(os.Stderr, "[startup] total TUI startup: %v\n", time.Since(startTime))
		}
	}()

	// The interactive shell needs a real terminal on stdin: with piped or
	// redirected input Bubble Tea blocks forever waiting for events that never
	// arrive (e.g. `echo "" | sonut`). Fail fast with guidance toward the headless
	// path instead of hanging. term.IsTerminal is a true TTY check (it rejects
	// pipes, regular files, and non-terminal char devices like /dev/null) and
	// fails closed — anything that is not a verified terminal blocks the shell.
	if !term.IsTerminal(os.Stdin.Fd()) {
		fmt.Fprintln(os.Stderr, "atcode: the interactive shell needs a terminal (stdin is not a TTY). For non-interactive use, run: at exec \"<prompt>\"")
		return 2
	}

	// Keep the OS awake for the whole interactive session so long agent turns,
	// builds, and model downloads are not interrupted by idle system sleep.
	// Best-effort and cross-platform; released on exit. Set SONUT_ALLOW_SLEEP=1
	// to opt out and let the machine sleep normally.
	defer wakelock.Acquire("atcode interactive session")()

	// Run the @code startup screen: login + llama server check + model warmup.
	// Skip if SONUT_SKIP_STARTUP is set (useful for testing / headless dev).
	if atcodeEnv("SKIP_STARTUP") == "" {
		llamaPort := options.LlamaPort
		if llamaPort == 0 {
			llamaPort = 2748
		}
		agentOpts := options.AgentOptions
		agentOpts.Cwd = options.Cwd
		agentOpts.Model = options.ModelName
		agentOpts.ProviderName = options.ProviderName
		agentOpts.ResponseStyle = options.ResponseStyle
		if options.Registry != nil {
			agentOpts.Registry = options.Registry
		}
		if options.PermissionMode != "" {
			agentOpts.PermissionMode = options.PermissionMode
		}
		if agentOpts.PermissionMode == "" {
			agentOpts.PermissionMode = agent.PermissionModeUnsafe
		}
		if !RunStartup(ctx, llamaPort, agentOpts) {
			return 0 // user quit during startup
		}
	}

	externalSink := options.RuntimeMessageSink
	var program *tea.Program
	options.RuntimeMessageSink = func(msg tea.Msg) {
		if externalSink != nil {
			externalSink(msg)
		}
		if program != nil {
			program.Send(msg)
		}
	}
	options.AltScreen = useAltScreen(options)

	// From here until exit Bubble Tea owns the terminal: park the process's
	// stderr in a log file so no stray write (Go log package, library noise,
	// child-process diagnostics) tears the alt-screen. The saved terminal
	// handle keeps the notifier's bell/OSC-9 sequences on the real terminal.
	terminalStderr, restoreStderr := redirectStderrToLog()
	notifyTerminal = terminalStderr
	defer restoreStderr()

	programOpts := []tea.ProgramOption{
		tea.WithContext(ctx),
		tea.WithInput(os.Stdin),
		tea.WithOutput(os.Stdout),
		tea.WithFilter(mouseEventFilter()),
	}
	// Honor the no-color.org spec ourselves: NO_COLOR set to ANY non-empty value
	// disables color. bubbletea/colorprofile only respects strconv.ParseBool-style
	// values, so NO_COLOR=yes / NO_COLOR=foo would otherwise leave the UI in full
	// color. Force the Ascii (no-color, bold-still-allowed) profile. (AUDIT-M3)
	if noColorRequested(os.Getenv) {
		programOpts = append(programOpts, tea.WithColorProfile(colorprofile.Ascii))
	}
	initialModel := newModel(ctx, options)
	if initialModel.wantsMouseCapture() {
		initialModel.mouseCapture = true
	}
	program = tea.NewProgram(initialModel, programOpts...)

	finalModel, err := program.Run()
	// The terminal is ours again: restore stderr before the exit-path prints
	// below (tui error, resume hint) so they reach the user, not the log file.
	restoreStderr()
	if err != nil {
		// Surface the failure: exiting 1 with zero diagnostics left users
		// guessing why the default chat surface died.
		fmt.Fprintln(os.Stderr, "@code: tui error:", err)
		return 1
	}
	printResumeHint(finalModel)
	return 0
}

func printResumeHint(finalModel tea.Model) {
	m, ok := finalModel.(model)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(m.activeSession.SessionID)
	if sessionID == "" {
		return
	}
	// Prefer the human-readable title so the command is useful when copied from
	// the terminal. Older sessions may not have one, so derive a first-prompt
	// title from the in-memory events before falling back to the exact id.
	title := strings.TrimSpace(m.activeSession.Title)
	if title == "" {
		title = firstUserMessageTitle(m.sessionEvents)
	}
	fmt.Fprintf(os.Stderr, "\nResume this session with:\n  at --resume %s\n", resumeHintArgument(title, sessionID))
}

// resumeHintArgument returns a shell-safe argument for the interactive
// --resume flag. Titles are convenient but a title of "latest" or one starting
// with a dash would be interpreted as a special flag value, so those cases use
// the exact session id instead.
func resumeHintArgument(title, sessionID string) string {
	value := strings.TrimSpace(title)
	if value == "" || strings.EqualFold(value, "latest") || strings.HasPrefix(value, "-") {
		value = strings.TrimSpace(sessionID)
	}
	return shellQuote(value)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func useAltScreen(_ Options) bool {
	return true
}

// noColorRequested reports whether the no-color.org spec is in effect: NO_COLOR set
// to any non-empty value. Checked here rather than via the colorprofile dependency,
// whose strconv.ParseBool gate ignores common values like NO_COLOR=yes. (AUDIT-M3)
func noColorRequested(getenv func(string) string) bool {
	return getenv("NO_COLOR") != ""
}
