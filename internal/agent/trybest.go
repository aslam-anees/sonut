package agent

// Try-Best monitor (ported from MiMo-Code's try-best-detector).
//
// Detects low-yield loops inside a turn:
//   - edit_repeat: near-identical diffs to the same file (Jaccard on 3-shingles)
//   - bash_retry:  same normalized command fails consecutively
//   - action_streak: consecutive edit/verify actions with no observable progress
//
// Integration policy (safe for existing systems): incidents inject a strategy-
// change nudge — they do NOT hard-stop the run. Hard stops remain the job of
// the existing exact-repeat / failure guards in guardrails.go.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/aistatine/sonut/internal/tools"
)

// toolCallArgsMap decodes a tool call's JSON arguments for harness monitors.
// On parse failure returns nil (callers treat missing command as no-op).
func toolCallArgsMap(arguments string) map[string]any {
	if strings.TrimSpace(arguments) == "" {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil
	}
	return args
}

const (
	tryBestEditWindow     = 12
	tryBestEditSimilarity = 0.8
	tryBestEditMatches    = 2
	tryBestActionStreak   = 4
	tryBestBashRetries    = 3
)

// TryBestReason is why the monitor fired.
type TryBestReason string

const (
	TryBestEditRepeat   TryBestReason = "edit_repeat"
	TryBestBashRetry    TryBestReason = "bash_retry"
	TryBestActionStreak TryBestReason = "action_streak"
)

// TryBestIncident is one detected low-yield loop signal.
type TryBestIncident struct {
	Reason     TryBestReason
	Tool       string
	Path       string
	Command    string
	Count      int
	Similarity float64
	Action     string // "edit" | "verify"
}

func (inc TryBestIncident) Nudge() string {
	switch inc.Reason {
	case TryBestEditRepeat:
		return "Try-best: near-identical edits repeated " + itoa(inc.Count) +
			" times on " + inc.Path +
			". STOP rewriting the same region. Re-read the file, change strategy " +
			"(different approach, smaller patch, or ask the user), and do NOT re-apply a similar diff."
	case TryBestBashRetry:
		return "Try-best: command failed " + itoa(inc.Count) +
			" times unchanged (`" + truncateRunes(inc.Command, 120) +
			"`). Do NOT retry the same command. Diagnose from the prior output, " +
			"fix the root cause, or pick a different command."
	case TryBestActionStreak:
		what := inc.Action
		if what == "" {
			what = "action"
		}
		msg := "Try-best: " + itoa(inc.Count) + " consecutive " + what +
			" steps with no progress. Pause thrashing — verify results, change approach, or report the blocker."
		if what == "edit" {
			msg += " If the file is truncated, append_file the NEXT chunk only; if rewriting, write_file with overwrite:true. Never re-issue an identical edit_file."
		}
		return msg
	default:
		return "Try-best: low-yield loop detected. Change strategy instead of repeating the same step."
	}
}

// TryBestMonitor tracks tool outcomes within a run.
type TryBestMonitor struct {
	edits  []tryBestEdit
	failed map[string]int
	verify map[string]tryBestVerify
	action *tryBestAction
	streak int
	// Disabled skips all detection (ATCODE_TRY_BEST=0).
	Disabled bool
}

type tryBestEdit struct {
	path     string
	shingles map[string]struct{}
}

type tryBestVerify struct {
	success bool
	result  string
}

type tryBestAction struct {
	kind     string // edit | verify
	progress bool
}

var tryBestVerifyCommand = regexp.MustCompile(
	`(?i)(?:^|[;&|]\s*|\s)(?:` +
		`bun\s+(?:test|typecheck|run\s+(?:test|typecheck|lint|build))|` +
		`npm\s+(?:test|run\s+(?:test|typecheck|lint|build))|` +
		`pnpm\s+(?:test|run\s+(?:test|typecheck|lint|build))|` +
		`yarn\s+(?:test|run\s+(?:test|typecheck|lint|build))|` +
		`pytest\b|python(?:3)?\s+-m\s+pytest\b|` +
		`cargo\s+(?:test|check|clippy|build)\b|` +
		`go\s+test\b|make\s+(?:test|check|lint|build)\b|tsc\b` +
		`)`,
)

var (
	tryBestEditTools = map[string]bool{
		"edit_file": true, "write_file": true, "apply_patch": true,
		"append_file": true, "streaming_file": true, "str_replace": true,
	}
	tryBestShellTools = map[string]bool{
		"bash": true, "exec_command": true,
	}
)

// NewTryBestMonitor builds a monitor. ATCODE_TRY_BEST=0 disables it.
func NewTryBestMonitor() *TryBestMonitor {
	disabled := os.Getenv("ATCODE_TRY_BEST") == "0" || os.Getenv("ATCODE_TRY_BEST") == "false"
	return &TryBestMonitor{Disabled: disabled}
}

// Observe records a completed tool call. Returns an incident when a low-yield
// loop is detected; nil otherwise. Soft signal only.
func (m *TryBestMonitor) Observe(tool string, args map[string]any, result ToolResult) *TryBestIncident {
	if m == nil || m.Disabled {
		return nil
	}
	if m.failed == nil {
		m.failed = make(map[string]int)
	}
	if m.verify == nil {
		m.verify = make(map[string]tryBestVerify)
	}

	if tryBestEditTools[tool] {
		// No-op edit (identical old/new or already-applied) is zero progress —
		// count toward action_streak so the model is nudged before the hard
		// consecutive-call stop. Live supermario: 3× identical edit_file.
		if result.Meta != nil && result.Meta["edit_noop"] == "true" {
			return m.trackAction(tryBestAction{kind: "edit", progress: false}, tool, "")
		}
		if result.Status == tools.StatusOK &&
			(strings.Contains(result.Output, "new_string is identical") ||
				strings.Contains(result.Output, "already contains new_string") ||
				strings.Contains(result.Output, "edit does nothing")) {
			return m.trackAction(tryBestAction{kind: "edit", progress: false}, tool, "")
		}
		return m.observeEdit(tool, result)
	}
	if tryBestShellTools[tool] {
		return m.observeBash(tool, args, result)
	}
	return nil
}

// Reset clears all state (e.g. after a strategy-change nudge is delivered).
func (m *TryBestMonitor) Reset() {
	if m == nil {
		return
	}
	m.edits = nil
	m.failed = nil
	m.verify = nil
	m.action = nil
	m.streak = 0
}

func (m *TryBestMonitor) observeEdit(tool string, result ToolResult) *TryBestIncident {
	if result.Status != tools.StatusOK {
		return m.trackAction(tryBestAction{kind: "edit", progress: false}, tool, "")
	}

	m.failed = make(map[string]int) // successful edit clears bash-fail streaks
	diff := result.Display.Preview
	paths := result.ChangedFiles
	if len(paths) == 0 && diff == "" {
		return m.trackAction(tryBestAction{kind: "edit", progress: false}, tool, "")
	}
	if len(paths) == 0 {
		paths = []string{"(unknown)"}
	}

	var first *TryBestIncident
	for _, p := range paths {
		normPath := filepath.Clean(p)
		normalized := normalizeDiff(diff)
		if normalized == "" {
			// Full-file write without a preview: fingerprint the summary path only.
			normalized = "path:" + normPath + " out:" + truncateRunes(result.Output, 200)
		}
		shingles := shingleSet(normalized, 3)
		matches := 0
		maxSim := 0.0
		for _, ev := range m.edits {
			if ev.path != normPath {
				continue
			}
			sim := jaccard(shingles, ev.shingles)
			if sim > maxSim {
				maxSim = sim
			}
			if sim > tryBestEditSimilarity {
				matches++
			}
		}
		m.edits = append(m.edits, tryBestEdit{path: normPath, shingles: shingles})
		if len(m.edits) > tryBestEditWindow {
			m.edits = m.edits[len(m.edits)-tryBestEditWindow:]
		}
		if matches >= tryBestEditMatches && first == nil {
			first = &TryBestIncident{
				Reason:     TryBestEditRepeat,
				Tool:       tool,
				Path:       p,
				Count:      matches + 1,
				Similarity: maxSim,
			}
		}
	}
	if first != nil {
		return first
	}
	// Edits without a similarity incident still count toward action_streak
	// (MiMo: consecutive edit/verify with no progress signal).
	return m.trackAction(tryBestAction{kind: "edit", progress: false}, tool, "")
}

func (m *TryBestMonitor) observeBash(tool string, args map[string]any, result ToolResult) *TryBestIncident {
	raw := stringArgLoose(args, "command", "cmd")
	if raw == "" {
		m.resetAction()
		return nil
	}
	normalized := normalizeCommand(raw)
	success := result.Status == tools.StatusOK
	// exec_command may still be running (session poll) — not a terminal outcome.
	if result.Meta != nil && result.Meta["session_id"] != "" && result.Meta["exit_code"] == "" {
		return nil
	}
	out := normalizeResult(result.Output)
	verifying := tryBestVerifyCommand.MatchString(normalized)

	var previous *tryBestVerify
	if verifying {
		if v, ok := m.verify[normalized]; ok {
			previous = &v
		}
	}
	progress := success || (previous != nil && previous.result != out)
	if verifying {
		m.verify[normalized] = tryBestVerify{success: success, result: out}
	}

	count := 0
	if !success {
		if progress {
			count = 1
		} else {
			count = m.failed[normalized] + 1
		}
		m.failed[normalized] = count
	} else {
		delete(m.failed, normalized)
	}

	var retry *TryBestIncident
	if count >= tryBestBashRetries {
		retry = &TryBestIncident{
			Reason:  TryBestBashRetry,
			Tool:    tool,
			Command: normalized,
			Count:   count,
		}
	}
	if !verifying {
		return retry
	}
	action := m.trackAction(tryBestAction{kind: "verify", progress: progress}, tool, normalized)
	if retry != nil {
		return retry
	}
	return action
}

func (m *TryBestMonitor) trackAction(next tryBestAction, tool, command string) *TryBestIncident {
	if next.progress || m.action == nil || m.action.kind != next.kind {
		m.action = &next
		if next.progress {
			m.streak = 0
		} else {
			m.streak = 1
		}
		return nil
	}
	m.streak++
	if m.streak < tryBestActionStreak {
		return nil
	}
	return &TryBestIncident{
		Reason:  TryBestActionStreak,
		Tool:    tool,
		Command: command,
		Count:   m.streak,
		Action:  next.kind,
	}
}

func (m *TryBestMonitor) resetAction() {
	m.action = nil
	m.streak = 0
}

func stringArgLoose(args map[string]any, keys ...string) string {
	if args == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := args[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

func normalizeDiff(diff string) string {
	diff = strings.ReplaceAll(diff, "\r\n", "\n")
	var parts []string
	for _, line := range strings.Split(diff, "\n") {
		if (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) &&
			!strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") {
			parts = append(parts, string(line[0])+" "+strings.Join(strings.Fields(line[1:]), " "))
		}
	}
	return strings.Join(parts, " ")
}

func shingleSet(value string, size int) map[string]struct{} {
	tokens := strings.Fields(value)
	out := make(map[string]struct{})
	if len(tokens) < size {
		if len(tokens) > 0 {
			out[strings.Join(tokens, "\x00")] = struct{}{}
		}
		return out
	}
	for i := 0; i <= len(tokens)-size; i++ {
		out[strings.Join(tokens[i:i+size], "\x00")] = struct{}{}
	}
	return out
}

func jaccard(left, right map[string]struct{}) float64 {
	if len(left) == 0 && len(right) == 0 {
		return 0
	}
	intersection := 0
	for k := range left {
		if _, ok := right[k]; ok {
			intersection++
		}
	}
	union := len(left) + len(right) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

var (
	tmpPathRE  = regexp.MustCompile(`(?:/private)?/tmp/[\w.-]+`)
	longNumRE  = regexp.MustCompile(`\b\d{6,}\b`)
	seedFlagRE = regexp.MustCompile(`--seed(?:=|\s+)\S+`)
	timeRE     = regexp.MustCompile(`(?i)\b\d+(?:\.\d+)?(?:ms|s|sec|seconds?)\b`)
)

func normalizeCommand(command string) string {
	s := tmpPathRE.ReplaceAllString(command, "<TMP>")
	s = longNumRE.ReplaceAllString(s, "<NUM>")
	s = seedFlagRE.ReplaceAllString(s, "<SEED>")
	return strings.Join(strings.Fields(s), " ")
}

func normalizeResult(value string) string {
	s := tmpPathRE.ReplaceAllString(value, "<TMP>")
	s = longNumRE.ReplaceAllString(s, "<NUM>")
	s = timeRE.ReplaceAllString(s, "<TIME>")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= 2000 {
		return s
	}
	const marker = " <TRUNCATED> "
	head := (2000 - len(marker)) / 2
	if head < 1 {
		return s[:2000]
	}
	return s[:head] + marker + s[len(s)-(2000-len(marker)-head):]
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
