// handoff_command.go implements /handoff: a structured, lossless exit from a
// session. Where compaction summarizes (lossily, via the model), handoff mines
// the transcript deterministically — original request, every user constraint,
// the plan with per-step status, files changed, recent tool activity, and git
// state — redacts secrets, and writes a markdown document a fresh session (in
// @code or any other agent) can start from with sonut context pollution.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// handoffDirName is where handoff documents land, relative to the workspace.
const handoffDirName = ".atcode/handoffs"

// handoffMaxAssistantChars caps the final-answer excerpt so a huge report
// doesn't dominate the document.
const handoffMaxAssistantChars = 4000

// handoffRecentTools is how many trailing tool calls are listed as activity.
const handoffRecentTools = 30

// handoffSecretPattern matches common credential shapes (HF, OpenAI, GitHub,
// AWS, Slack tokens and bearer headers) so a handoff never leaks a secret that
// passed through the transcript.
var handoffSecretPattern = regexp.MustCompile(
	`(hf_[A-Za-z0-9]{16,}|sk-[A-Za-z0-9_-]{16,}|ghp_[A-Za-z0-9]{16,}|gho_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{16,}|AKIA[0-9A-Z]{16}|xox[baprs]-[A-Za-z0-9-]{10,}|(?i:bearer\s+)[A-Za-z0-9._~+/-]{16,}=*)`)

func redactHandoffSecrets(s string) string {
	return handoffSecretPattern.ReplaceAllString(s, "[REDACTED]")
}

// handleHandoffCommand builds the handoff document from the current session
// state and writes it under .atcode/handoffs/. Returns the transcript status
// text describing where it landed.
func (m model) handleHandoffCommand(args string) (model, string) {
	doc := m.buildHandoffDocument(strings.TrimSpace(args))
	dir := filepath.Join(m.cwd, handoffDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return m, "Handoff\n" + err.Error()
	}
	path := filepath.Join(dir, "handoff-"+time.Now().Format("20060102-150405")+".md")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		return m, "Handoff\n" + err.Error()
	}
	rel := path
	if r, err := filepath.Rel(m.cwd, path); err == nil {
		rel = r
	}
	return m, renderCommandOutput(commandOutput{
		Title:  "Handoff",
		Status: commandStatusOK,
		Sections: []commandSection{{
			Title: "Written",
			Lines: []string{
				"document: " + rel,
				"secrets: redacted",
			},
		}},
		Hints: []string{"paste the document into a fresh session to continue with clean context"},
	})
}

// buildHandoffDocument assembles the markdown handoff from transcript rows,
// plan state, and live git output. Deterministic — no model call, no loss.
func (m model) buildHandoffDocument(note string) string {
	var b strings.Builder
	b.WriteString("# Session Handoff\n\n")
	b.WriteString("- Date: " + time.Now().Format("2006-01-02 15:04") + "\n")
	b.WriteString("- Working directory: " + m.cwd + "\n")
	if m.modelName != "" {
		b.WriteString("- Model: " + m.modelName + "\n")
	}
	if id := strings.TrimSpace(m.activeSession.SessionID); id != "" {
		b.WriteString("- Session: " + id + "\n")
	}
	if note != "" {
		b.WriteString("- Operator note: " + note + "\n")
	}

	// Git state (best-effort; omitted outside a repo).
	if out, err := exec.Command("git", "-C", m.cwd, "--no-optional-locks", "status", "--short", "--branch").Output(); err == nil {
		if status := strings.TrimSpace(string(out)); status != "" {
			b.WriteString("\n## Git state\n\n```\n" + status + "\n```\n")
		}
	}

	// User messages: the original request first, then every follow-up — these
	// carry the constraints and corrections that compaction usually loses.
	var userMsgs []string
	for _, row := range m.transcript {
		if row.kind == rowUser {
			if text := strings.TrimSpace(row.text); text != "" {
				userMsgs = append(userMsgs, text)
			}
		}
	}
	if len(userMsgs) > 0 {
		b.WriteString("\n## Original request\n\n" + userMsgs[0] + "\n")
	}
	if len(userMsgs) > 1 {
		b.WriteString("\n## Follow-up instructions & constraints (verbatim, in order)\n\n")
		for _, msg := range userMsgs[1:] {
			b.WriteString("- " + strings.ReplaceAll(msg, "\n", "\n  ") + "\n")
		}
	}

	// Plan with per-step status — decisions and progress in one place.
	if len(m.plan.steps) > 0 {
		b.WriteString("\n## Plan status\n\n")
		for _, step := range m.plan.steps {
			mark := " "
			switch step.status {
			case "completed":
				mark = "x"
			case "in_progress":
				mark = "~"
			case "failed":
				mark = "!"
			}
			b.WriteString("- [" + mark + "] " + step.content)
			if step.notes != "" {
				b.WriteString(" — " + step.notes)
			}
			b.WriteString("\n")
		}
	}

	// Files changed: union of every mutating tool result's changed files.
	seen := map[string]bool{}
	var changed []string
	for _, row := range m.transcript {
		for _, f := range row.changedFiles {
			if !seen[f] {
				seen[f] = true
				changed = append(changed, f)
			}
		}
	}
	if len(changed) > 0 {
		b.WriteString("\n## Files changed this session\n\n")
		for _, f := range changed {
			b.WriteString("- " + f + "\n")
		}
	}

	// Recent tool activity: the trail of what was actually done.
	var toolLines []string
	for _, row := range m.transcript {
		if row.kind == rowToolCall && row.tool != "" {
			line := row.tool
			if arg := strings.TrimSpace(row.arg); arg != "" {
				if len(arg) > 120 {
					arg = arg[:120] + "…"
				}
				line += " — " + arg
			}
			toolLines = append(toolLines, line)
		}
	}
	if len(toolLines) > handoffRecentTools {
		toolLines = toolLines[len(toolLines)-handoffRecentTools:]
	}
	if len(toolLines) > 0 {
		b.WriteString("\n## Recent tool activity (last " + fmt.Sprintf("%d", len(toolLines)) + ")\n\n")
		for _, line := range toolLines {
			b.WriteString("- " + line + "\n")
		}
	}

	// Last assistant answer: the most recent state-of-the-work report.
	for i := len(m.transcript) - 1; i >= 0; i-- {
		if m.transcript[i].kind == rowAssistant {
			text := strings.TrimSpace(m.transcript[i].text)
			if text == "" {
				continue
			}
			if len(text) > handoffMaxAssistantChars {
				text = text[:handoffMaxAssistantChars] + "\n… (truncated)"
			}
			b.WriteString("\n## Last assistant report\n\n" + text + "\n")
			break
		}
	}

	b.WriteString("\n## Next session\n\nContinue from the state above. Verify the plan's unfinished steps against the working tree before acting — the git state section is the ground truth.\n")
	return redactHandoffSecrets(b.String())
}
