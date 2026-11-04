package tui

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/aistatine/sonut/internal/tools"
)

// handleAddDirCommand processes "/add-dir [path]". A bare "/add-dir" lists the
// current write roots; with a path it grants session-scoped write access via
// the sandbox engine's shared scope, so the policy gate, the OS profile of the
// next bash command, and the file tools all widen immediately.
func (m model) handleAddDirCommand(arg string) model {
	engine := m.agentOptions.Sandbox
	if engine == nil || engine.Scope() == nil {
		return m.appendSystemNotice("add-dir: sandbox scope is unavailable in this session.")
	}
	scope := engine.Scope()
	trimmed := strings.TrimSpace(arg)
	if trimmed == "" {
		return m.appendSystemNotice("Write roots:\n  " + strings.Join(scope.Roots(), "\n  ") + "\nUsage: /add-dir <path>  (grants are session-only; use sandbox.additionalWriteRoots in the global config to persist)")
	}
	root, err := scope.Add(trimmed)
	if err != nil {
		return m.appendSystemNotice("add-dir: " + err.Error())
	}
	return m.appendSystemNotice("write access added: " + root + " (this session only)")
}

// handleCDCommand processes "/cd <path>". It changes the model's working
// directory and grants write access to the new path so tool calls work without
// further per-call prompts.
func (m model) handleCDCommand(arg string) (model, tea.Cmd) {
	trimmed := strings.TrimSpace(arg)
	if trimmed == "" {
		return m.appendSystemNotice(fmt.Sprintf("cwd: %s\nUsage: /cd <path>", m.cwd)), nil
	}

	// Resolve to absolute path.
	abs := tools.ResolveWorkspaceCwd(m.cwd, trimmed)

	// Verify the target exists and is a directory.
	info, err := os.Stat(abs)
	if err != nil {
		return m.appendSystemNotice("cd: " + err.Error()), nil
	}
	if !info.IsDir() {
		return m.appendSystemNotice("cd: not a directory: " + abs), nil
	}

	// Grant write access to the new path (once, for this session).
	engine := m.agentOptions.Sandbox
	if engine != nil && engine.Scope() != nil {
		if _, addErr := engine.Scope().Add(abs); addErr != nil {
			return m.appendSystemNotice("cd: could not grant access: " + addErr.Error()), nil
		}
	}

	// Update the model working directory and refresh the git branch label.
	m.cwd = abs
	m.gitBranch = gitBranch(abs)

	// Propagate the change everywhere tools resolve paths, so relative writes,
	// exec default cwd, and the system-prompt workspace context all follow /cd
	// instead of silently staying on the launch directory.
	tools.SetSessionWorkingDir(abs)
	m.agentOptions.Cwd = abs
	_ = os.Chdir(abs)

	notice := fmt.Sprintf("Changed directory to: %s", abs)
	m = m.appendSystemNotice(notice)
	// Kick off a fresh git sweep for the new root, and re-prime the KV cache
	// in the background: the system prompt embeds the workspace (cwd, git
	// status, repo map), so /cd invalidates the primed prefix — without a
	// re-warm the next message pays a full multi-minute re-prefill in silence.
	return m, tea.Batch(gitSweepCmd(m.ctx, abs, false), m.warmupEffortCmd())
}

// appendSystemNotice appends a system transcript line and returns the updated
// model. Shared by the inline command handlers (/add-dir, /image) so notice
// plumbing lives in one place.
func (m model) appendSystemNotice(text string) model {
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
	return m
}
