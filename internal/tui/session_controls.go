package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/aistatine/sonut/internal/agent"
	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/modelregistry"
	"github.com/aistatine/sonut/internal/sessions"
	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/usage"
)

var responseStyles = []string{"balanced", "concise", "explanatory", "review", "caveman"}

// defaultReasoningEffort returns the default effort when none is explicitly set.
// @code defaults to "auto": the agent decides thinking depth per turn.
func defaultReasoningEffort(explicit modelregistry.ReasoningEffort) modelregistry.ReasoningEffort {
	if explicit != "" {
		return explicit
	}
	return modelregistry.ReasoningEffortAuto
}

// effortDisplayName converts internal effort names to user-facing short names.
// @code exposes three modes: "auto" (agent decides per turn), "flash" (fast, no
// thinking), and "super" (max thinking).
func effortDisplayName(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "low", "minimal":
		return "flash"
	case "medium", "high", "xhigh", "max":
		return "super"
	case "", "auto":
		return "auto"
	}
	return effort
}

const tuiCompactionPreserveLast = 8
const tuiCompactionMaxPromptChars = 8000
const compactStatusRowID = "compact/status"

var compactFrames = []string{"⠂", "⠒", "⠲", "⠴"}

type SessionCompactor interface {
	CompactSession(context.Context, CompactRequest) (CompactResult, error)
}

type CompactRequest struct {
	SessionID             string
	ModelName             string
	ContextWindow         int
	SessionEventCount     int
	EstimatedTokens       int
	VisibleTranscriptRows int
	CompactRequests       int
	// CustomInstructions, when non-empty, is appended to the summarization
	// prompt so the user can guide what the summary should emphasise or preserve.
	// Mirrors sonut-code's "/compact [instructions]" feature.
	CustomInstructions string
}

type CompactResult struct {
	Compacted    bool
	BeforeTokens int
	AfterTokens  int
	Summary      string
	RemovedTurns int
}

type compactResultMsg struct {
	result             CompactResult
	err                error
	activeSession      sessions.Metadata
	sessionEvents      []sessions.Event
	transcript         []transcriptRow
	hasSessionSnapshot bool
}

// handleEffortCommand: manual effort switching is REMOVED — the harness routes
// every turn itself (flash-with-tiny-think for simple asks, deep thinking for
// real work; see classifyAutoEffort/classifyTurnShape). Live switching also
// invalidated the KV prefix mid-session and confused users when a pinned
// effort silently overrode the router. /effort now just explains the policy
// and re-arms auto in case an old session persisted a pinned value.
func (m model) handleEffortCommand(args string) (model, string) {
	m.reasoningEffort = modelregistry.ReasoningEffortAuto
	return m, m.effortStatusCard("auto",
		"Thinking depth is managed automatically per turn: simple asks get a tiny ~2s think, deep work gets full reasoning. Manual switching has been removed so it can never fight the router.")
}

// effortStatusCard renders the small inline confirmation card shown after a
// /effort <value> mutation (set, auto, unknown, unsupported). The body is a
// lime-bordered card so the transition from "picker open" -> "card collapsed"
// reads as the same surface the picker came from, instead of a separate grey
// status block.
func (m model) effortStatusCard(value string, detail string) string {
	active := effortDisplayName(value)
	fields := []commandField{
		{Key: "active effort", Value: active},
		{Key: "model", Value: displayValue(m.modelName, "atcode")},
	}
	return renderCommandCardTranscript(commandCard{
		Title:   "Effort",
		Summary: []string{"active effort: " + active},
		Sections: []commandCardSection{{
			Title:  "State",
			Fields: fields,
			Lines:  []string{detail},
		}},
	})
}

// isLlamaProvider reports whether the active provider is a local llama.cpp server.
func (m model) isLlamaProvider() bool {
	name := strings.ToLower(strings.TrimSpace(m.providerName))
	return name == "llama" || name == "llama.cpp" || name == "local" || name == ""
}

func (m model) effortDisplay() string {
	return effortDisplayName(string(m.reasoningEffort))
}

// effortDividerLabel returns a compact effort indicator for the composer rule.
// Shows "●auto", "●flash", or "●super" (or model name when effort is unset).
func (m model) effortDividerLabel() string {
	label := effortDisplayName(string(m.reasoningEffort))
	switch label {
	case "auto":
		return sonutTheme.accent.Render("●auto")
	case "flash":
		return sonutTheme.accent.Render("●flash")
	case "super":
		return sonutTheme.accent.Render("●super")
	}
	// Fallback: show model name (or "atcode" for local).
	name := strings.TrimSpace(m.modelName)
	if name == "" {
		name = "atcode"
	}
	return name
}

// cycleReasoningEffort advances the reasoning-effort ring: auto -> super ->
// flash -> auto. No-op (model unchanged) when the active model exposes no
// effort controls, so Ctrl+T stays quiet on non-reasoning models. Called from
// the Ctrl+T key case — a rare keypress — so the DefaultRegistry() lookup
// inside availableReasoningEfforts is fine here, but MUST NOT be called from
// the render path (the registry is rebuilt on every call).
func (m model) cycleReasoningEffort() (model, tea.Cmd) {
	// Manual effort switching is removed (see handleEffortCommand): the ring
	// key now just re-arms auto so the harness router is always in charge.
	m.reasoningEffort = modelregistry.ReasoningEffortAuto
	return m, nil
}

func reasoningEffortAllowed(efforts []modelregistry.ReasoningEffort, want modelregistry.ReasoningEffort) bool {
	for _, effort := range efforts {
		if effort == want {
			return true
		}
	}
	return false
}

func (m model) handleStyleCommand(args string) (model, string) {
	args = strings.TrimSpace(strings.ToLower(args))
	if args == "" || args == "list" {
		return m, m.styleText()
	}
	// "normal" is the spoken-language exit from caveman mode.
	if args == "normal" {
		args = defaultResponseStyle
	}
	if !responseStyleAllowed(args) {
		return m, "Style\nUnknown response style: " + args
	}
	m.responseStyle = args
	return m, strings.Join([]string{
		"Style",
		"active style: " + m.responseStyle,
		"Style preference is stored for this TUI session.",
	}, "\n")
}

func (m model) styleText() string {
	return renderCommandOutput(commandOutput{
		Title:  "Style",
		Status: commandStatusOK,
		Sections: []commandSection{{
			Title: "State",
			Lines: []string{
				"active style: " + m.responseStyle,
				"available: " + strings.Join(responseStyles, ", "),
			},
		}},
		Hints: []string{"use /style <value> to update this TUI session"},
	})
}

func defaultedResponseStyle(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if responseStyleAllowed(value) {
		return value
	}
	return defaultResponseStyle
}

func (m model) handleSelfCorrectCommand(args string) (model, string) {
	args = strings.TrimSpace(strings.ToLower(args))
	switch args {
	case "", "status":
		return m, m.selfCorrectText()
	case "on", "tests", "full":
		m.selfCorrectTests = true
	case "off", "lsp":
		m.selfCorrectTests = false
	default:
		return m, "Self-correct\nUsage: /selfcorrect [status|on|off|tests|full|lsp]"
	}
	return m, m.selfCorrectText()
}

// maxTurnsCeiling caps the per-session /turns budget so a typo (e.g. /turns 99999)
// can't set an absurd ceiling; real multi-step tasks fit comfortably under it. Shared
// with config so applyEnv enforces the same bound on an inherited SONUT_MAX_TURNS.
const maxTurnsCeiling = config.MaxTurnsCeiling

func (m model) handleTurnsCommand(args string) (model, string) {
	args = strings.TrimSpace(args)
	if args == "" || strings.EqualFold(args, "status") {
		return m, m.turnsText()
	}
	n, err := strconv.Atoi(args)
	if err != nil || n < 1 {
		return m, "Turns\nUsage: /turns <n> — set the per-run tool-turn budget for this session (a positive number, e.g. /turns 150)."
	}
	if n > maxTurnsCeiling {
		n = maxTurnsCeiling
	}
	m.agentOptions.MaxTurns = n
	// Propagate the budget to spawned sub-agents / swarm members (which inherit the
	// environment) so a delegated task gets the same budget, not config.json's default.
	config.SetMaxTurnsEnv(n)
	return m, m.turnsText()
}

func (m model) turnsText() string {
	return renderCommandOutput(commandOutput{
		Title:  "Turns",
		Status: commandStatusOK,
		Sections: []commandSection{{
			Title: "State",
			Lines: []string{fmt.Sprintf("max tool-turns per run: %d", m.agentOptions.MaxTurns)},
		}},
		Hints: []string{fmt.Sprintf("/turns <n> sets this session's tool-turn budget (max %d); raise it for long multi-step tasks like delegation/swarm runs", maxTurnsCeiling)},
	})
}

func (m model) selfCorrectText() string {
	depth := "LSP diagnostics only — fast, scoped to changed files"
	if m.selfCorrectTests {
		depth = "LSP diagnostics + project test plan (e.g. go test ./...) — slower, whole-repo"
	}
	return renderCommandOutput(commandOutput{
		Title:  "Self-correct",
		Status: commandStatusOK,
		Sections: []commandSection{{
			Title: "State",
			Lines: []string{
				"post-edit verification: " + depth,
				"auto-fix vs report-only: follows the active permission mode",
			},
		}},
		Hints: []string{"/selfcorrect on adds the full test suite; /selfcorrect off returns to LSP-only (default)"},
	})
}

func responseStyleAllowed(value string) bool {
	for _, style := range responseStyles {
		if value == style {
			return true
		}
	}
	return false
}

func (m model) handleCompactCommand(args string) (model, string, tea.Cmd) {
	normalized := strings.TrimSpace(strings.ToLower(args))
	if normalized == "status" {
		return m, m.compactText(false), nil
	}
	// Any non-empty, non-"status" arg is treated as custom summarization
	// instructions (mirrors sonut-code's "/compact [instructions]").
	customInstructions := strings.TrimSpace(args)
	if m.compactInFlight {
		return m, m.compactText(true), nil
	}
	m.compactRequests++
	m.lastCompactError = ""
	m.lastCompactResult = nil
	m.compactCustomInstructions = customInstructions
	if m.sessionCompactor == nil && !m.hasSessionBackedCompactor() {
		return m, m.compactText(true), nil
	}
	m.compactInFlight = true
	m.compactFrame = 0
	return m, m.compactText(true), tea.Batch(m.runCompact(), m.spinner.Tick)
}

func (m model) runCompact() tea.Cmd {
	request := m.compactRequest()
	if m.sessionCompactor != nil {
		compactor := m.sessionCompactor
		ctx := m.ctx
		return func() tea.Msg {
			result, err := compactor.CompactSession(ctx, request)
			return compactResultMsg{result: result, err: err}
		}
	}
	return func() tea.Msg {
		next, result, err := m.compactActiveSession()
		if err != nil {
			return compactResultMsg{err: err}
		}
		return compactResultMsg{
			result:             result,
			activeSession:      next.activeSession,
			sessionEvents:      append([]sessions.Event{}, next.sessionEvents...),
			transcript:         append([]transcriptRow{}, next.transcript...),
			hasSessionSnapshot: true,
		}
	}
}

// handleRewindCommand restores workspace files to a checkpoint and truncates the
// session log. "/rewind" or "/rewind latest" undoes the most recent checkpoint;
// "/rewind <n>" rewinds to a specific event sequence.
func (m model) handleRewindCommand(args string) (model, string) {
	if m.sessionStore == nil || m.activeSession.SessionID == "" {
		return m, "Rewind\nno active session to rewind."
	}
	if m.pending {
		return m, "Rewind\ncannot rewind while a run is in progress."
	}
	// A cancelled run's late flush hasn't appended its checkpoint events yet:
	// ApplyRewind would prune those checkpoint blobs as unreferenced, then the
	// flush would re-append pre-rewind events after the rewind marker.
	if len(m.flushRunIDs) > 0 {
		return m, "Rewind\ncannot rewind while a cancelled run is still flushing — retry in a moment."
	}
	arg := strings.TrimSpace(strings.ToLower(args))
	target, err := m.resolveRewindTarget(arg)
	if err != nil {
		return m, "Rewind\n" + err.Error()
	}
	report, err := m.sessionStore.ApplyRewind(m.activeSession.SessionID, m.cwd, target)
	if err != nil {
		return m, "Rewind\n" + err.Error()
	}

	// ApplyRewind truncated the persisted event log, restored files, and appended a
	// rewind marker — so reload the in-memory session state. Otherwise the dropped
	// events would still (a) be re-sent to the agent as ContextEvents on the next
	// prompt (sessionPrompt sends m.sessionEvents) and (b) linger in the transcript.
	// A reload FAILURE must be surfaced (and the stale in-memory context dropped),
	// not ignored — silently keeping m.sessionEvents would re-send rewound-away
	// events on the next prompt, defeating the rewind.
	if meta, getErr := m.sessionStore.Get(m.activeSession.SessionID); getErr == nil {
		m.activeSession = *meta
	}
	events, readErr := m.sessionStore.ReadEvents(m.activeSession.SessionID)
	if readErr != nil {
		m.sessionEvents = nil // drop stale context so it can't reach the next prompt
		return m, fmt.Sprintf("Rewind\nrewound to sequence %d, but reloading the session failed (in-memory context cleared): %s", target, readErr.Error())
	}
	m.sessionEvents = append([]sessions.Event{}, events...)
	rows := initialTranscript()
	rows = appendTranscriptRowsDedup(rows, transcriptRowsFromSessionEvents(events))
	m.transcript = rows
	// The rebuilt (post-rewind) transcript flushes fresh below a divider; the
	// pre-rewind scrollback above it stays, as scrollback cannot be un-printed.
	m.resetFlushFrontier("· rewound ·")

	summary := fmt.Sprintf("Rewound to sequence %d\n%d file(s) restored, %d deleted, %d skipped.",
		target, report.FilesRestored, report.FilesDeleted, len(report.Skipped))
	if len(report.Skipped) > 0 {
		summary += "\nskipped (not recoverable): " + strings.Join(report.Skipped, ", ")
	}
	return m, summary
}

// resolveRewindTarget maps a /rewind argument to a keep-through event sequence.
func (m model) resolveRewindTarget(arg string) (int, error) {
	events, err := m.sessionStore.ReadEvents(m.activeSession.SessionID)
	if err != nil {
		return 0, err
	}
	if arg == "" || arg == "latest" {
		lastCheckpoint := 0
		for _, ev := range events {
			if ev.Type == sessions.EventSessionCheckpoint && ev.Sequence > lastCheckpoint {
				lastCheckpoint = ev.Sequence
			}
		}
		if lastCheckpoint == 0 {
			return 0, fmt.Errorf("no checkpoints to rewind")
		}
		return lastCheckpoint - 1, nil // undo the most recent checkpoint
	}
	seq, err := strconv.Atoi(arg)
	if err != nil || seq < 0 {
		return 0, fmt.Errorf("usage: /rewind [latest|<sequence>]")
	}
	return seq, nil
}

func (m model) compactText(requested bool) string {
	if m.compactInFlight {
		return m.compactRunningText()
	}
	if requested && m.lastCompactResult != nil {
		return compactCompleteText(*m.lastCompactResult)
	}
	status := commandStatusInfo
	if requested {
		status = commandStatusWarning
		if m.lastCompactResult != nil {
			status = commandStatusOK
		}
		if m.lastCompactError != "" {
			status = commandStatusBlocked
		}
	}
	request := m.compactRequest()
	sections := []commandSection{
		{
			Title: "State",
			Lines: []string{
				"summary: " + m.compactionStatus(),
				"requested: " + boolText(m.compactRequests > 0),
				"model: " + displayValue(request.ModelName, "none"),
				"context window: " + compactContextWindowText(request.ContextWindow),
				"auto compaction: model adaptive at ~80% of context window",
				fmt.Sprintf("estimated transcript: %d tokens", request.EstimatedTokens),
				fmt.Sprintf("visible transcript rows: %d", request.VisibleTranscriptRows),
				m.compactabilityText(request),
			},
		},
	}
	if m.lastCompactResult != nil {
		sections = append(sections, commandSection{
			Title: "Result",
			Lines: compactResultLines(*m.lastCompactResult),
		})
	} else if m.lastCompactError != "" {
		sections = append(sections, commandSection{
			Title: "Result",
			Lines: []string{
				"compacted: no",
				"error: " + m.lastCompactError,
			},
		})
	} else if requested && m.sessionCompactor == nil {
		sections = append(sections, commandSection{
			Title: "Result",
			Lines: []string{
				"compacted: no",
				"reason: manual compactor unavailable",
			},
		})
	}
	return renderCommandOutput(commandOutput{
		Title:    "Compact",
		Status:   status,
		Sections: sections,
		Hints:    []string{"manual compaction affects this TUI session when a compactor is available"},
	})
}

func (m model) compactRunningText() string {
	return strings.Join([]string{
		"Compressing session",
		"Keep editing your draft; press Enter after compression finishes to send.",
		m.compactAnimationLine(),
	}, "\n")
}

func compactCompleteText(result CompactResult) string {
	lines := []string{
		"Compression complete",
		"Session summary saved.",
		"Ready for the next prompt.",
	}
	if result.Compacted && result.BeforeTokens > 0 && result.AfterTokens > 0 && result.AfterTokens < result.BeforeTokens {
		lines = append(lines, fmt.Sprintf("Estimated context reduced by %s tokens.", formatContextWindow(result.BeforeTokens-result.AfterTokens)))
	}
	return strings.Join(lines, "\n")
}

func (m model) compactionStatus() string {
	if m.compactInFlight {
		return "compacting context"
	}
	if m.lastCompactResult != nil {
		if m.lastCompactResult.Compacted {
			return "compacted manually"
		}
		return "manual compactor completed without changes"
	}
	if m.lastCompactError != "" {
		return "manual compaction failed"
	}
	if m.compactRequests > 0 {
		return "requested, awaiting manual compactor"
	}
	return "not compacted"
}

func (m model) compactRequest() CompactRequest {
	return CompactRequest{
		SessionID:             strings.TrimSpace(m.activeSession.SessionID),
		ModelName:             strings.TrimSpace(m.modelName),
		ContextWindow:         m.modelContextWindow(m.modelName),
		SessionEventCount:     len(m.sessionEvents),
		EstimatedTokens:       estimateTranscriptTokens(m.transcript),
		VisibleTranscriptRows: len(m.transcript),
		CompactRequests:       m.compactRequests,
		CustomInstructions:    m.compactCustomInstructions,
	}
}

func (m model) compactabilityText(request CompactRequest) string {
	if m.sessionCompactor == nil && !m.hasSessionBackedCompactor() {
		return "compactable: no (manual compactor unavailable)"
	}
	if m.sessionCompactor == nil && request.SessionEventCount <= tuiCompactionPreserveLast {
		return "compactable: no (not enough session history yet)"
	}
	if request.ContextWindow <= 0 {
		return "compactable: no (unknown model context window)"
	}
	if request.EstimatedTokens <= 0 {
		return "compactable: no (empty transcript)"
	}
	return "compactable: yes"
}

func (m model) hasSessionBackedCompactor() bool {
	return m.sessionStore != nil && strings.TrimSpace(m.activeSession.SessionID) != ""
}

func (m model) compactActiveSession() (model, CompactResult, error) {
	if m.sessionStore == nil || strings.TrimSpace(m.activeSession.SessionID) == "" {
		return m, CompactResult{}, fmt.Errorf("no active session to compact")
	}
	beforeEvents := append([]sessions.Event{}, m.sessionEvents...)
	beforeTokens := estimateTranscriptTokens(m.transcript)
	plan, err := m.sessionStore.PlanCompaction(m.activeSession.SessionID, sessions.CompactionOptions{
		PreserveLast:   tuiCompactionPreserveLast,
		MaxPromptChars: tuiCompactionMaxPromptChars,
	})
	if err != nil {
		return m, CompactResult{}, err
	}
	if plan.CompactableCount == 0 {
		return m, CompactResult{
			Compacted:    false,
			BeforeTokens: beforeTokens,
			AfterTokens:  beforeTokens,
			Summary:      "not enough session history to compact yet",
		}, nil
	}

	summary, err := m.summarizeCompactionPlan(plan, m.compactCustomInstructions)
	if err != nil {
		return m, CompactResult{}, err
	}
	// Clear custom instructions after use so a plain /compact next time
	// doesn't reuse the previous custom directive.
	m.compactCustomInstructions = ""
	if _, err := m.sessionStore.RecordCompaction(m.activeSession.SessionID, sessions.RecordCompactionInput{
		Plan:    plan,
		Summary: summary,
	}); err != nil {
		return m, CompactResult{}, err
	}
	if meta, err := m.sessionStore.Get(m.activeSession.SessionID); err == nil && meta != nil {
		m.activeSession = *meta
	}
	events, err := m.sessionStore.ReadRehydratedEvents(m.activeSession.SessionID)
	if err != nil {
		m.sessionEvents = nil
		return m, CompactResult{}, fmt.Errorf("reload compacted session: %w", err)
	}
	m.sessionEvents = append([]sessions.Event{}, events...)
	rows := initialTranscript()
	rows = appendTranscriptRowsDedup(rows, transcriptRowsFromSessionEvents(events))
	m.transcript = rows
	afterTokens := estimateTranscriptTokens(m.transcript)
	m.resetFlushFrontier(compactionDividerText(plan.CompactableCount, beforeTokens-afterTokens))

	return m, CompactResult{
		Compacted:    len(events) < len(beforeEvents)+1,
		BeforeTokens: beforeTokens,
		AfterTokens:  afterTokens,
		Summary:      summary,
		RemovedTurns: plan.CompactableCount,
	}, nil
}

// compactionDividerText renders the subtle transcript divider shown after a
// compaction, e.g. "— compacted 12 turns into summary (saved ~9K tokens) —".
func compactionDividerText(removedTurns, savedTokens int) string {
	if removedTurns <= 0 {
		return "· compacted ·"
	}
	turnWord := "turns"
	if removedTurns == 1 {
		turnWord = "turn"
	}
	if savedTokens <= 0 {
		return fmt.Sprintf("— compacted %d %s into summary —", removedTurns, turnWord)
	}
	return fmt.Sprintf("— compacted %d %s into summary (saved ~%s tokens) —", removedTurns, turnWord, formatContextWindow(savedTokens))
}

func (m model) summarizeCompactionPlan(plan sessions.CompactionPlan, customInstructions string) (string, error) {
	if m.provider == nil {
		return deterministicCompactionSummary(plan), nil
	}
	systemContent := "Summarize compacted @code session events for future coding context. Preserve user goals, decisions, files, tool outcomes, blockers, and exact next steps. Omit secrets and do not invent details."
	if customInstructions != "" {
		systemContent += "\n\nAdditional summarization instructions from the user: " + customInstructions
	}
	stream, err := m.provider.StreamCompletion(m.ctx, sonutruntime.CompletionRequest{
		Messages: []sonutruntime.Message{
			{Role: sonutruntime.MessageRoleSystem, Content: systemContent},
			{Role: sonutruntime.MessageRoleUser, Content: plan.SummaryPrompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("summarize compacted session: %w", err)
	}
	collected := sonutruntime.CollectStream(m.ctx, stream)
	if collected.Error != "" {
		return "", fmt.Errorf("summarize compacted session: %s", collected.Error)
	}
	summary := strings.TrimSpace(collected.Text)
	if summary == "" {
		return "", fmt.Errorf("summarize compacted session: empty summary")
	}
	return summary, nil
}

func deterministicCompactionSummary(plan sessions.CompactionPlan) string {
	lines := []string{
		fmt.Sprintf("Compacted earlier session context: %d event(s) summarized, %d recent event(s) preserved.", plan.CompactableCount, plan.PreservedCount),
	}
	if len(plan.CompactableEvents) > 0 {
		first := plan.CompactableEvents[0]
		last := plan.CompactableEvents[len(plan.CompactableEvents)-1]
		lines = append(lines, fmt.Sprintf("Compacted range: #%d %s through #%d %s.", first.Sequence, first.Type, last.Sequence, last.Type))
	}
	if len(plan.PreservedEvents) > 0 {
		first := plan.PreservedEvents[0]
		last := plan.PreservedEvents[len(plan.PreservedEvents)-1]
		lines = append(lines, fmt.Sprintf("Preserved recent range: #%d %s through #%d %s.", first.Sequence, first.Type, last.Sequence, last.Type))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func compactContextWindowText(window int) string {
	if window <= 0 {
		return "unknown"
	}
	return formatContextWindow(window) + " tokens"
}

func estimateTranscriptTokens(rows []transcriptRow) int {
	total := 0
	for _, row := range rows {
		text := row.text + "\n" + row.detail + "\n" + row.tool + "\n" + row.arg
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		total += agent.ApproxTextTokens(text) + 4
	}
	return total
}

func compactResultLines(result CompactResult) []string {
	lines := []string{
		"compacted: " + boolText(result.Compacted),
	}
	if result.BeforeTokens > 0 {
		lines = append(lines, fmt.Sprintf("before: %d tokens", result.BeforeTokens))
	}
	if result.AfterTokens > 0 {
		lines = append(lines, fmt.Sprintf("after: %d tokens", result.AfterTokens))
	}
	if summary := strings.TrimSpace(result.Summary); summary != "" {
		lines = append(lines, "summary: "+summary)
	}
	return lines
}

func (m model) compactAnimationLine() string {
	frame := compactFrames[m.compactFrame%len(compactFrames)]
	return frame + " Compressing history..."
}

func (m model) setCompactStatusRow(text string) model {
	row := transcriptRow{kind: rowSystem, id: compactStatusRowID, text: text}
	for i := len(m.transcript) - 1; i >= 0; i-- {
		if m.transcript[i].id == compactStatusRowID {
			m.transcript[i] = row
			return m
		}
	}
	m.transcript = appendTranscriptRow(m.transcript, row)
	return m
}

func (m model) recordUsageEvent(modelID string, event sonutruntime.Usage) (model, []transcriptRow) {
	if m.usageTracker == nil || strings.TrimSpace(modelID) == "" {
		return m, nil
	}
	normalized, runtimeUsage, err := usage.Normalize(event)
	if err != nil {
		return m, []transcriptRow{{kind: rowError, text: "usage: " + err.Error()}}
	}
	m.lastUsage = normalized
	m.lastUsageSeen = true
	if _, err := m.usageTracker.Record(usage.RecordInput{
		ModelID: modelID,
		Usage:   runtimeUsage,
		Source:  "tui",
	}); err != nil {
		if isUnpricedUsageError(err) {
			m.unpricedRequests++
			m.unpricedTokens += normalized.TotalTokens
			return m, nil
		}
		return m, []transcriptRow{{kind: rowError, text: "usage: " + err.Error()}}
	}
	return m, nil
}

func (m model) latestUsageTokens(summary usage.Summary) int {
	if m.lastUsageSeen && m.lastUsage.TotalTokens > 0 {
		return m.lastUsage.TotalTokens
	}
	if summary.LastRecord != nil && summary.LastRecord.Usage.TotalTokens > 0 {
		return summary.LastRecord.Usage.TotalTokens
	}
	// Neither the last usage event nor the tracker's last record carried a
	// token count (e.g. a tool-heavy turn whose most recent usage event
	// reported zero). Fall back to the session's accumulated total rather
	// than showing "0 tokens" while real work — and real spend — happened.
	if summary.TotalTokens > 0 {
		return summary.TotalTokens
	}
	return m.unpricedTokens
}

func isUnpricedUsageError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"unknown @code model",
		"unknown @code model",
		"missing model input pricing rate",
		"missing model output pricing rate",
		"invalid model cached input pricing rate",
		"no model cost tier covers",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (m model) usageSummaryText() string {
	if m.usageTracker == nil {
		return "usage unavailable"
	}
	summary := m.usageTracker.Summary()
	if summary.RecordCount == 0 && m.unpricedRequests == 0 {
		return "no usage yet"
	}
	if summary.RecordCount == 0 {
		return formatUnpricedUsage(m.unpricedRequests, m.unpricedTokens)
	}
	if m.unpricedRequests == 0 {
		return usage.FormatSummary(summary)
	}
	return usage.FormatSummary(summary) + "; " + formatUnpricedUsage(m.unpricedRequests, m.unpricedTokens)
}

func formatUnpricedUsage(requests int, tokens int) string {
	requestLabel := "requests"
	if requests == 1 {
		requestLabel = "request"
	}
	return fmt.Sprintf("%d %s, %d tokens, cost unavailable", requests, requestLabel, tokens)
}
