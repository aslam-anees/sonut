package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/aistatine/sonut/internal/tools"
)

// TestWorkingPlanLine: the working indicator's second line carries the plan's
// done/total and the in-progress step while a plan is active, and is empty when
// there's no plan or the plan is complete.
func TestWorkingPlanLine(t *testing.T) {
	m := model{now: time.Now}
	if got := m.workingPlanLine(); got != "" {
		t.Errorf("no plan: want empty, got %q", got)
	}

	m.plan.steps = []planStep{
		{content: "Research the topic", status: "completed"},
		{content: "Add product catalog", status: "in_progress"},
		{content: "Write the docs", status: "pending"},
	}
	got := plainRender(t, m.workingPlanLine())
	if !strings.Contains(got, "plan 1/3") {
		t.Errorf("active plan line missing count: %q", got)
	}
	if !strings.Contains(got, "Add product catalog") {
		t.Errorf("active plan line missing current step: %q", got)
	}

	for i := range m.plan.steps {
		m.plan.steps[i].status = "completed"
	}
	if got := m.workingPlanLine(); got != "" {
		t.Errorf("complete plan: want empty, got %q", got)
	}
}

func TestActivityLabelsExposeSilentRunPhases(t *testing.T) {
	cases := map[string]string{
		"preparing context…":                     "analyzing input",
		"prefilling context… 43% (430/1000 tok)": "analyzing input… 43% (430/1000 tok)",
		"analyzing attached image…":              "analyzing input",
	}
	for input, want := range cases {
		if got := prefillActivityLabel(input); got != want {
			t.Errorf("prefillActivityLabel(%q) = %q, want %q", input, got, want)
		}
	}

	m := model{pending: true, prefillStatus: "preparing context…"}
	if got := m.workingActivity(); got != "analyzing input" {
		t.Fatalf("workingActivity during prefill = %q, want analyzing input", got)
	}
	if got := m.statusLineAction(); got != "analyzing input" {
		t.Fatalf("statusLineAction during prefill = %q, want analyzing input", got)
	}
}

func TestPrefillCompletionLeavesAnalysisPhase(t *testing.T) {
	m := newModel(t.Context(), Options{ModelName: "gpt-4.1"})
	m = m.beginRun(nil)
	runID := m.activeRunID
	next, _ := m.Update(prefillProgressMsg{runID: runID, processed: 100, total: 100})
	m = next.(model)
	if m.prefillStatus != "" {
		t.Fatalf("completed prefill should clear its status, got %q", m.prefillStatus)
	}
	if got := m.workingActivity(); got != "thinking through response" {
		t.Fatalf("completed prefill should hand off to generation, got %q", got)
	}
	if got := m.workingTokenIndicator(); !strings.Contains(got, "100/100 in") {
		t.Fatalf("completed prefill should keep useful input progress, got %q", got)
	}
	// A late progress heartbeat must not resurrect the completed analysis label
	// after the run response has closed the active run.
	m.pending = false
	next, _ = m.Update(prefillProgressMsg{runID: runID, processed: 100, total: 100})
	m = next.(model)
	if m.prefillStatus != "" {
		t.Fatalf("late prefill heartbeat resurrected status: %q", m.prefillStatus)
	}
}

func TestPlannerParserIgnoresBareMarkdownMarkers(t *testing.T) {
	items := parsePlannerOutputText("Plan:\n1. **Create index.html**\n2. Add styles\n-\n", 8)
	if len(items) != 2 {
		t.Fatalf("planner parser returned %d items, want 2: %#v", len(items), items)
	}
	if items[0].Content != "**Create index.html**" {
		t.Fatalf("planner parser changed markdown content: %q", items[0].Content)
	}
}

func TestPlannerSystemNoteRendersMarkdownFormatting(t *testing.T) {
	rendered := renderSystemNote("Plan:\n1. **Create index.html**\n2. Add styles", 80)
	plain := plainRender(t, rendered)
	if strings.Contains(plain, "**") {
		t.Fatalf("planner note leaked markdown markers:\n%s", plain)
	}
	if !strings.Contains(plain, "Create index.html") || !strings.Contains(plain, "Add styles") {
		t.Fatalf("planner note lost plan content:\n%s", plain)
	}
	if !strings.Contains(rendered, "\x1b[1;") {
		t.Fatalf("planner note did not preserve bold styling:\n%q", rendered)
	}
}

// TestHiddenPlumbingToolsSkippedFromTranscript: the plumbing tools (update_plan,
// tool_search) render nothing — their call AND result rows are dropped; real
// work tools still render.
func TestHiddenPlumbingToolsSkippedFromTranscript(t *testing.T) {
	rows := []transcriptRow{
		{kind: rowToolCall, tool: "update_plan", id: "c1", runID: 1},
		{kind: rowToolResult, tool: "update_plan", id: "c1", runID: 1, text: "10 steps · 2 done"},
		{kind: rowToolCall, tool: "tool_search", id: "c2", runID: 1},
		{kind: rowToolResult, tool: "tool_search", id: "c2", runID: 1, text: "select:swarm_spawn,…"},
		{kind: rowToolResult, tool: "bash", id: "c3", runID: 1, text: "ok"},
	}
	rc := buildRowContext(rows)
	for _, i := range []int{0, 1, 2, 3} {
		if !rc.skip(rows[i]) {
			t.Errorf("plumbing row %d (%s/%v) should be skipped", i, rows[i].tool, rows[i].kind)
		}
	}
	if rc.skip(rows[4]) {
		t.Error("a normal tool result (bash) must not be skipped")
	}

	// A FAILED plumbing result must still render — its error has to surface.
	failed := transcriptRow{kind: rowToolResult, tool: "update_plan", id: "c9", runID: 1, status: tools.StatusError, text: "tool result: update_plan error boom"}
	if buildRowContext([]transcriptRow{failed}).skip(failed) {
		t.Error("a failed plumbing result must NOT be skipped (the error must show)")
	}

	if !isHiddenPlumbingTool("update_plan") || !isHiddenPlumbingTool("tool_search") {
		t.Error("update_plan and tool_search must be hidden plumbing")
	}
	if isHiddenPlumbingTool("write_file") || isHiddenPlumbingTool("web_search") {
		t.Error("real work tools must NOT be hidden")
	}
}

// TestQuietGenerationHint: after a silent stretch during an active run, the
// working line shows a quiet cue with an advancing timer; while output is
// flowing (recent activity) or when idle, it stays empty. Zero-token silence
// is named as prompt prefill (local hybrid models re-prefill every turn);
// mid-stream silence keeps the classic "still generating…" wording.
func TestQuietGenerationHint(t *testing.T) {
	base := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	m := model{now: func() time.Time { return base.Add(30 * time.Second) }}
	m.activeRunID = 7
	m.turnStartedAt = base

	// Recently active (< quietWorkingHint) -> no hint.
	m.lastStreamActivity = base.Add(29 * time.Second) // 1s quiet
	if got := m.quietGenerationHint(); got != "" {
		t.Errorf("recent activity: want no hint, got %q", got)
	}

	// Quiet for >= the threshold with zero tokens -> prefill wait (not a hang).
	m.lastStreamActivity = base.Add(5 * time.Second) // 25s quiet at now
	m.turnStreamedRunes = 0
	if got := m.quietGenerationHint(); !strings.Contains(got, "prefill") && !strings.Contains(got, "first token") {
		t.Errorf("zero-token quiet stretch: want a prefill wait hint, got %q", got)
	}
	if line := plainRender(t, m.workingStatusLine()); !strings.Contains(line, "prefill") && !strings.Contains(line, "first token") {
		t.Errorf("working line should carry the prefill wait hint, got %q", line)
	}

	// Mid-stream silence (some tokens already arrived) -> classic cue.
	m.turnStreamedRunes = 400
	if got := m.quietGenerationHint(); !strings.Contains(got, "still generating") {
		t.Errorf("mid-stream quiet: want still-generating hint, got %q", got)
	}

	// No active run -> never a hint.
	idle := m
	idle.activeRunID = 0
	if got := idle.quietGenerationHint(); got != "" {
		t.Errorf("idle: want no hint, got %q", got)
	}
}

// TestQuietGenerationHintEscalatesPastHalfIdleTimeout: a heartbeating-but-
// silent stream (chatgpt/gpt-5.x, ollama reasoning models — see
// providerio.ErrStreamStalled) and a genuinely healthy-but-slow one look
// identical under the plain "still generating… Xs" cue — the ticking number
// is the only signal, whether real content is still coming or nothing ever
// will. Past half the provider's idle timeout the cue must say so explicitly
// and name when @code's own content-stall watchdog will act, rather than
// leaving the user to guess whether this is a hang.
func TestQuietGenerationHintEscalatesPastHalfIdleTimeout(t *testing.T) {
	// 30s idle timeout: half (15s) sits comfortably above quietWorkingHint (8s),
	// leaving a clean window to observe the plain cue before it escalates.
	t.Setenv("SONUT_STREAM_IDLE_TIMEOUT", "30s")
	base := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	m := model{now: func() time.Time { return base }}
	m.activeRunID = 7
	m.turnStartedAt = base

	// Mid-stream silence (tokens already arrived) past quietWorkingHint but
	// under half the idle timeout -> plain still-generating cue.
	m.turnStreamedRunes = 400
	m.lastStreamActivity = base.Add(-10 * time.Second)
	got := m.quietGenerationHint()
	if !strings.Contains(got, "still generating") {
		t.Fatalf("under half the idle timeout: want the plain cue, got %q", got)
	}
	if strings.Contains(got, "auto-recover") {
		t.Fatalf("under half the idle timeout: should not have escalated yet, got %q", got)
	}

	// Quiet for >= half the idle timeout -> escalates to name the watchdog and
	// its ceiling (providerio.ContentStallTimeout(30s) = 30s*1.2 = 36s here).
	m.lastStreamActivity = base.Add(-15 * time.Second)
	got = m.quietGenerationHint()
	if !strings.Contains(got, "unusually quiet") || !strings.Contains(got, "auto-recover") {
		t.Fatalf("past half the idle timeout: want an escalated cue naming the watchdog, got %q", got)
	}
	if !strings.Contains(got, "36s") {
		t.Fatalf("escalated cue should name the content-stall ceiling (ContentStallTimeout(30s) = 36s), got %q", got)
	}
}

// TestQuietHintHiddenWhenSidebarShowsIt: when the context sidebar is up (it
// carries the "generating…" pulse in ACTIVITY), the working line must NOT also
// show the hint — it appears in exactly one place.
func TestQuietHintHiddenWhenSidebarShowsIt(t *testing.T) {
	base := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	m := sidebarTestModel() // alt-screen + a plan -> sidebar active
	m.now = func() time.Time { return base.Add(30 * time.Second) }
	m.activeRunID = 7
	m.turnStartedAt = base
	m.lastStreamActivity = base.Add(2 * time.Second) // 28s quiet
	m.turnStreamedRunes = 400                        // mid-stream silence, not prefill
	if !m.sidebarActive() {
		t.Fatal("precondition: sidebar should be active for this model")
	}
	if line := plainRender(t, m.workingStatusLine()); strings.Contains(line, "still generating") {
		t.Errorf("working line must NOT duplicate the hint when the sidebar shows it:\n%s", line)
	}
	if act := plainRender(t, strings.Join(m.sidebarActivityLines(sidebarWidth(m.width), 10), "\n")); !strings.Contains(act, "generating") {
		t.Errorf("the sidebar ACTIVITY should carry the generating pulse instead:\n%s", act)
	}
}

func TestFormatWorkingElapsed(t *testing.T) {
	cases := map[time.Duration]string{
		0:                 "0s",
		8 * time.Second:   "8s",
		59 * time.Second:  "59s",
		64 * time.Second:  "1m04s",
		125 * time.Second: "2m05s",
		-3 * time.Second:  "0s",
	}
	for d, want := range cases {
		if got := formatWorkingElapsed(d); got != want {
			t.Errorf("formatWorkingElapsed(%s) = %q, want %q", d, got, want)
		}
	}
}

// The key fix: the live working line (spinner + verb + elapsed) is shown even
// AFTER partial text has streamed, so an upstream stall never looks frozen.
func TestInterimBlockShowsWorkingLineWithStreamedText(t *testing.T) {
	m := newModel(t.Context(), Options{ModelName: "gpt-4.1"})
	m.width = 100
	base := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return base.Add(12 * time.Second) }
	m.turnStartedAt = base
	m.streamingText = "partial answer so far"

	got := plainRender(t, m.interimBlock(96))
	if !strings.Contains(got, "partial answer so far") {
		t.Fatalf("interim block should keep the streamed text:\n%s", got)
	}
	// The working indicator (label + elapsed) moved to the footer status line.
	status := plainRender(t, m.workingStatusLine())
	if !strings.Contains(status, "12s") {
		t.Fatalf("working status line should show live elapsed (12s):\n%s", status)
	}
	if !strings.Contains(status, "writing") {
		t.Fatalf("working status line should show the writing phase label:\n%s", status)
	}
}

func TestSilentThinkingPhaseDescribesQuietPrefill(t *testing.T) {
	base := time.Date(2026, 6, 18, 23, 0, 0, 0, time.UTC)
	m := model{
		pending:       true,
		prefillStatus: "preparing context…",
		now:           func() time.Time { return base },
	}
	if got := m.silentThinkingPhase(); got != "preparing the request context…" {
		t.Fatalf("prefill phase = %q, want a natural context-preparation label", got)
	}

	m.prefillStatus = ""
	m.activeRunID = 1
	m.turnStartedAt = base
	m.lastStreamActivity = base.Add(-12 * time.Second)
	if got := m.silentThinkingPhase(); got != "waiting for the first response token…" {
		t.Fatalf("quiet phase = %q, want a first-token label", got)
	}
}

// The working line carries a live token estimate ("↑ <n> tok") that climbs as
// the model streams, replacing the old static scroll figure.
func TestWorkingTokenIndicatorEstimatesFromStreamedRunes(t *testing.T) {
	m := newModel(t.Context(), Options{ModelName: "gpt-4.1"})
	// Always visible during a run, starting at zero so the working line never
	// drops the counter (the bug: it blinked out during the initial think).
	if got := m.workingTokenIndicator(); !strings.Contains(got, "↑") || !strings.Contains(got, "0 tok") {
		t.Fatalf("at turn start the counter should read like ↑ 0 tok, got %q", got)
	}
	m.turnStreamedRunes = 4000 // ~1000 tokens at ~4 chars/token
	got := m.workingTokenIndicator()
	for _, want := range []string{"↑", "tok", "1K"} {
		if !strings.Contains(got, want) {
			t.Fatalf("indicator = %q, want it to contain %q", got, want)
		}
	}
}

// The estimate must keep climbing across the per-segment buffer clears (a tool
// call wipes streamingText/Reasoning) — turnStreamedRunes accumulates over the
// whole turn, so the counter never snaps back to zero mid-turn.
func TestWorkingTokenIndicatorAccumulatesAcrossSegmentClears(t *testing.T) {
	m := newModel(t.Context(), Options{ModelName: "gpt-4.1"})
	m = m.beginRun(nil)
	rid := m.activeRunID

	updated, _ := m.Update(agentReasoningMsg{runID: rid, delta: strings.Repeat("a", 40)})
	m = updated.(model)
	afterReasoning := m.turnStreamedRunes
	if afterReasoning == 0 {
		t.Fatal("reasoning deltas should accumulate streamed runes")
	}

	// Simulate the segment boundary that clears the live buffers, then stream
	// answer text in the next segment.
	m.streamingReasoning = ""
	m.streamingText = ""
	updated, _ = m.Update(agentTextMsg{runID: rid, delta: strings.Repeat("b", 40)})
	m = updated.(model)

	if m.turnStreamedRunes <= afterReasoning {
		t.Fatalf("token estimate must keep climbing across the buffer clear: before=%d after=%d", afterReasoning, m.turnStreamedRunes)
	}

	// The climbing figure must actually reach the rendered working line.
	if line := plainRender(t, m.workingStatusLine()); !strings.Contains(line, "tok") {
		t.Fatalf("working status line should carry the live token counter, got %q", line)
	}

	// A fresh turn resets the accumulator to zero.
	m = m.beginRun(nil)
	if m.turnStreamedRunes != 0 {
		t.Fatalf("beginRun should reset the per-turn token estimate, got %d", m.turnStreamedRunes)
	}
}

func TestPreviewTail(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"short", 20, "short"},
		{"exactlyten", 10, "exactlyten"},
		{"abcdefghijklmnop", 6, "…lmnop"}, // tail with leading ellipsis
		{"", 8, ""},
	}
	for _, c := range cases {
		if got := previewTail(c.in, c.width); got != c.want {
			t.Errorf("previewTail(%q,%d) = %q, want %q", c.in, c.width, got, c.want)
		}
	}
}

// The live preview shows only the newest natural sentence, not the full
// provider reasoning stream.
func TestInterimBlockShowsCompactReasoningTail(t *testing.T) {
	m := newModel(t.Context(), Options{ModelName: "gpt-4.1"})
	m.width = 100
	base := time.Date(2026, 6, 18, 23, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return base.Add(90 * time.Second) }
	m.turnStartedAt = base
	m.streamingReasoning = "analyzing the layout\nthe patch was corrupt so re-planning the css edits"
	m.streamingText = "" // thinking phase: no answer yet

	got := plainRender(t, m.interimBlock(96))
	if !strings.Contains(got, "re-planning the css edits") {
		t.Fatalf("latest natural reasoning tail missing from preview:\n%s", got)
	}
	if strings.Contains(got, "analyzing the layout") {
		t.Fatalf("preview should keep only the latest reasoning tail:\n%s", got)
	}
	if !strings.Contains(got, "> thinking ..") {
		t.Fatalf("expected a thinking marker:\n%s", got)
	}
	// The elapsed clock moved to the footer working status line.
	status := plainRender(t, m.workingStatusLine())
	if !strings.Contains(status, "1m30s") {
		t.Fatalf("expected the working-line elapsed clock:\n%s", status)
	}
}

func TestInterimBlockHidesPromptChecklistReasoning(t *testing.T) {
	m := newModel(t.Context(), Options{ModelName: "gpt-4.1"})
	m.width = 100
	m.streamingReasoning = "Let's check constraints: answer briefly\n" +
		"- Output matches response.\n" +
		"I am checking the existing file before the next edit."
	m.streamingText = ""
	got := plainRender(t, m.interimBlock(96))
	if strings.Contains(got, "Let's check constraints") || strings.Contains(got, "Output matches response") {
		t.Fatalf("prompt/checklist reasoning leaked into compact preview:\n%s", got)
	}
	if !strings.Contains(got, "checking the existing file") {
		t.Fatalf("safe natural reasoning should remain visible:\n%s", got)
	}
	if !strings.Contains(got, "> thinking ..") {
		t.Fatalf("thinking marker missing from compact preview:\n%s", got)
	}
}

func TestInterimBlockKeepsExpandedReasoningCompact(t *testing.T) {
	m := newModel(t.Context(), Options{ModelName: "gpt-4.1"})
	m.width = 100
	m.now = func() time.Time { return time.Date(2026, 6, 18, 23, 0, 0, 0, time.UTC) }
	m.streamingReasoningExpanded = true
	m.streamingReasoning = "only line of reasoning here"
	m.streamingText = ""
	got := plainRender(t, m.interimBlock(96))
	if !strings.Contains(got, "only line of reasoning here") {
		t.Fatalf("expanded reasoning preview missing:\n%s", got)
	}
	if !strings.Contains(got, "> thinking ..") {
		t.Fatalf("thinking marker missing:\n%s", got)
	}
}
