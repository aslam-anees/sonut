package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aistatine/sonut/internal/tools"
)

func TestPlanPanelUpdateFromItems(t *testing.T) {
	var s planPanelState
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	s.updateFromItems([]tools.PlanItem{
		{Content: "Read file", Status: "in_progress"},
		{Content: "Edit file", Status: "pending"},
	}, now)

	if len(s.steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(s.steps))
	}
	if s.steps[0].status != "in_progress" {
		t.Errorf("step 0 status = %q, want in_progress", s.steps[0].status)
	}
	if s.steps[0].startedAt.IsZero() {
		t.Error("in_progress step should have startedAt set")
	}
	if !s.startedAt.IsZero() == false {
		t.Error("panel startedAt should be set on first update")
	}
	if s.isComplete() {
		t.Error("plan should not be complete with a pending step")
	}
}

func TestCompactMarkdownLineFormatsFixedHeightPlanRows(t *testing.T) {
	rendered := renderCompactMarkdownLine("# **Ship** with `care`", sonutTheme.accent, 80)
	if strings.Contains(rendered, "# ") || strings.Contains(rendered, "**Ship**") {
		t.Fatalf("compact Markdown leaked source markers: %q", rendered)
	}
	plain := ansiPattern.ReplaceAllString(rendered, "")
	if !strings.Contains(plain, "Ship") || !strings.Contains(plain, "care") {
		t.Fatalf("compact Markdown lost visible content: %q", plain)
	}
	if rendered == "# **Ship** with `care`" {
		t.Fatal("compact Markdown should not leave source formatting markers in the rendered row")
	}
}

func TestPlanPanelMergesPartialExecutorUpdateIntoPlannerRoadmap(t *testing.T) {
	var s planPanelState
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	roadmap := make([]tools.PlanItem, 8)
	for i := range roadmap {
		roadmap[i] = tools.PlanItem{Content: fmt.Sprintf("Step %d", i+1), Status: "pending"}
	}
	s.updateFromItems(roadmap, now)

	merged := s.mergePartialItems([]tools.PlanItem{{Content: "Step 1", Status: "completed"}})
	if len(merged) != 8 {
		t.Fatalf("partial executor update replaced the roadmap: got %d steps, want 8", len(merged))
	}
	if merged[0].Status != "completed" || merged[7].Content != "Step 8" {
		t.Fatalf("partial update was not merged into the planner roadmap: %#v", merged)
	}
}

func TestPlanPanelPreservesTimestamps(t *testing.T) {
	var s planPanelState
	t0 := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	s.updateFromItems([]tools.PlanItem{
		{Content: "Step A", Status: "in_progress"},
	}, t0)

	t1 := t0.Add(10 * time.Second)
	s.updateFromItems([]tools.PlanItem{
		{Content: "Step A", Status: "completed"},
	}, t1)

	if s.steps[0].startedAt != t0 {
		t.Errorf("startedAt not preserved: got %v, want %v", s.steps[0].startedAt, t0)
	}
	if s.steps[0].completedAt != t1 {
		t.Errorf("completedAt not set: got %v, want %v", s.steps[0].completedAt, t1)
	}
}

func TestPlanPanelIsComplete(t *testing.T) {
	var s planPanelState
	now := time.Now()

	s.updateFromItems([]tools.PlanItem{
		{Content: "A", Status: "completed"},
		{Content: "B", Status: "failed"},
	}, now)

	if !s.isComplete() {
		t.Error("plan with all completed/failed should be complete")
	}
	if s.completedAt.IsZero() {
		t.Error("completedAt should be set when plan is complete")
	}
}

func TestPlanPanelClear(t *testing.T) {
	var s planPanelState
	s.updateFromItems([]tools.PlanItem{{Content: "A", Status: "pending"}}, time.Now())
	s.clear()

	if !s.isEmpty() {
		t.Error("plan should be empty after clear")
	}
	if len(s.steps) != 0 {
		t.Errorf("expected 0 steps after clear, got %d", len(s.steps))
	}
}

func TestUpdateFromItemsDuplicateContentKeepsDistinctTimestamps(t *testing.T) {
	t1 := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	var s planPanelState
	// Two prior steps with IDENTICAL content but distinct start times.
	s.steps = []planStep{
		{content: "step", status: "completed", startedAt: t1, completedAt: t1.Add(time.Second)},
		{content: "step", status: "in_progress", startedAt: t2},
	}
	s.updateFromItems([]tools.PlanItem{
		{Content: "step", Status: "completed"},
		{Content: "step", Status: "in_progress"},
	}, t2.Add(time.Hour))
	if len(s.steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(s.steps))
	}
	// Each step must inherit a DISTINCT prior entry positionally, not both collapse
	// onto the first content match (L22).
	if s.steps[0].startedAt != t1 {
		t.Errorf("step[0] startedAt = %v, want %v", s.steps[0].startedAt, t1)
	}
	if s.steps[1].startedAt != t2 {
		t.Errorf("step[1] startedAt = %v, want %v (duplicate-content tie-break)", s.steps[1].startedAt, t2)
	}
}

func TestPlanPanelHeight(t *testing.T) {
	var s planPanelState
	now := time.Now()

	// Empty plan: height 0
	if h := s.height(80, now); h != 0 {
		t.Errorf("empty plan height = %d, want 0", h)
	}

	// Running plan with 3 steps: 2 (header+bar) + 3 (steps) = 5
	s.updateFromItems([]tools.PlanItem{
		{Content: "A", Status: "completed"},
		{Content: "B", Status: "in_progress"},
		{Content: "C", Status: "pending"},
	}, now)
	if h := s.height(80, now); h != 5 {
		t.Errorf("running plan height = %d, want 5", h)
	}

	// Completed plan collapsed: 2 (header+bar only)
	s.updateFromItems([]tools.PlanItem{
		{Content: "A", Status: "completed"},
		{Content: "B", Status: "completed"},
		{Content: "C", Status: "completed"},
	}, now)
	if h := s.height(80, now); h != 2 {
		t.Errorf("completed collapsed plan height = %d, want 2", h)
	}

	// Expanded: 2 + 3 = 5
	s.expanded = true
	if h := s.height(80, now); h != 5 {
		t.Errorf("expanded plan height = %d, want 5", h)
	}
}

func TestPlanPanelVisibleHidesAfterTimeout(t *testing.T) {
	var s planPanelState
	t0 := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	s.updateFromItems([]tools.PlanItem{
		{Content: "A", Status: "completed"},
	}, t0)

	// Visible right after completion
	if !s.visible(t0.Add(10 * time.Second)) {
		t.Error("plan should be visible within 30s of completion")
	}

	// Hidden after 30s
	if s.visible(t0.Add(31 * time.Second)) {
		t.Error("plan should hide after 30s of completion when not expanded")
	}

	// Expanded keeps it visible
	s.expanded = true
	if !s.visible(t0.Add(31 * time.Second)) {
		t.Error("expanded plan should stay visible")
	}
}

func TestPlanPanelRenderEmpty(t *testing.T) {
	m := newModel(t.Context(), Options{ModelName: "gpt-4"})
	if got := m.renderPlanPanel(80); got != "" {
		t.Errorf("empty plan should render empty string, got %q", got)
	}
}

func TestPlanPanelRenderRunning(t *testing.T) {
	m := newModel(t.Context(), Options{ModelName: "gpt-4"})
	m.width = 100
	base := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return base.Add(15 * time.Second) }

	m.plan.updateFromItems([]tools.PlanItem{
		{Content: "Read encrypt.go", Status: "completed"},
		{Content: "Fix retry loop", Status: "in_progress"},
		{Content: "Run tests", Status: "pending"},
	}, base)

	got := m.renderPlanPanel(80)
	if got == "" {
		t.Fatal("expected non-empty plan panel render")
	}
}

// TestPlanNowFreezeAndResume covers the idle-freeze fix: while the agent is idle
// (activeRunID == 0) and a freeze time is stamped, the plan clock is frozen so an
// in_progress step left mid-plan stops ticking; during a run it tracks live time.
func TestPlanNowFreezeAndResume(t *testing.T) {
	frozen := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	live := frozen.Add(2 * time.Minute)
	m := model{now: func() time.Time { return live }, plan: planPanelState{frozenAt: frozen}}

	m.activeRunID = 0 // idle -> frozen clock
	if got := m.planNow(); !got.Equal(frozen) {
		t.Fatalf("idle planNow = %v, want frozen %v", got, frozen)
	}
	m.activeRunID = 3 // running -> live clock
	if got := m.planNow(); !got.Equal(live) {
		t.Fatalf("running planNow = %v, want live %v", got, live)
	}
	m.activeRunID = 0
	m.plan.frozenAt = time.Time{} // idle but never stamped -> live fallback
	if got := m.planNow(); !got.Equal(live) {
		t.Fatalf("unstamped idle planNow = %v, want live fallback %v", got, live)
	}
}

func TestPlanPanelClearResetsFrozenAt(t *testing.T) {
	s := planPanelState{frozenAt: time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)}
	s.clear()
	if !s.frozenAt.IsZero() {
		t.Fatal("clear() must reset frozenAt so the next run starts with a live clock")
	}
}

// runningPlanModel builds a model with an n-step running plan for the pinned
// panel tests.
func runningPlanModel(t *testing.T, steps int) model {
	t.Helper()
	m := newModel(t.Context(), Options{ModelName: "gpt-4"})
	m.width = 100
	base := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return base.Add(15 * time.Second) }
	items := make([]tools.PlanItem, steps)
	for i := range items {
		status := "pending"
		if i == 0 {
			status = "in_progress"
		}
		items[i] = tools.PlanItem{Content: fmt.Sprintf("Step number %d here", i+1), Status: status}
	}
	m.plan.updateFromItems(items, base)
	return m
}

func TestPinnedPlanFullWhenItFits(t *testing.T) {
	m := runningPlanModel(t, 3)
	// 3 steps => header + 3 step lines = 4; budget 10 fits.
	got := m.renderPinnedPlanPanel(80, 10)
	if strings.Count(got, "\n")+1 < 4 {
		t.Fatalf("expected full multi-line panel within budget, got:\n%s", got)
	}
	if !strings.Contains(got, "Step number 1") || !strings.Contains(got, "Step number 3") {
		t.Fatalf("full panel should list every step, got:\n%s", got)
	}
}

func TestPinnedPlanCollapsesToSummaryWhenTooTall(t *testing.T) {
	m := runningPlanModel(t, 12)
	// 12 steps would be 14 lines; budget 5 forces the one-line summary.
	got := m.renderPinnedPlanPanel(80, 5)
	if strings.Contains(got, "\n") {
		t.Fatalf("over-budget plan should collapse to one line, got:\n%s", got)
	}
	if !strings.Contains(got, "PLAN") {
		t.Fatalf("summary line should still show PLAN, got:\n%s", got)
	}
}

func TestPinnedPlanHiddenWhenEmpty(t *testing.T) {
	m := newModel(t.Context(), Options{ModelName: "gpt-4"})
	m.width = 100
	if got := m.renderPinnedPlanPanel(80, 10); got != "" {
		t.Fatalf("no plan => empty pinned render, got %q", got)
	}
}

// TestPinnedPlanHiddenWhenSidebarToggledOff: Ctrl+S (sidebarHidden) on a wide
// alt-screen terminal hides the plan entirely — it must NOT fall back to the
// pinned panel above the composer, since the sidebar is the plan's home there.
func TestPinnedPlanHiddenWhenSidebarToggledOff(t *testing.T) {
	m := runningPlanModel(t, 3)
	m.altScreen = true
	m.height = 40
	m.headerPrinted = true
	// Real conversation keeps this test representative of an active chat.
	m.transcript = append(m.transcript, transcriptRow{kind: rowToolCall, tool: "read_file", detail: "main.go"})

	if !m.sidebarActive() {
		t.Fatal("precondition: sidebar should be active for a wide alt-screen model with a plan")
	}
	// Sidebar shown: the plan lives in the sidebar, no pinned panel.
	if got := m.renderPinnedPlanPanel(m.chatColumnWidth(), 10); got != "" {
		t.Fatalf("sidebar shown: pinned plan should be suppressed, got:\n%s", got)
	}

	// Ctrl+S collapses the sidebar -> the plan must be hidden entirely, not pinned.
	m.sidebarHidden = true
	if got := m.renderPinnedPlanPanel(m.chatColumnWidth(), 10); got != "" {
		t.Fatalf("Ctrl+S should hide the plan entirely, but the pinned panel showed:\n%s", got)
	}

	// Sidebar unavailable (no alt-screen): the pinned panel is the plan's only
	// home and must still show.
	if got := runningPlanModel(t, 3).renderPinnedPlanPanel(80, 10); got == "" {
		t.Fatal("sidebar unavailable: the pinned plan must still show")
	}
}

func TestFooterIncludesPinnedPlanAboveComposer(t *testing.T) {
	m := runningPlanModel(t, 3)
	m.height = 40
	footer := plainRender(t, m.footerView(m.chatColumnWidth()))
	planIdx := strings.Index(footer, "PLAN")
	composerIdx := strings.Index(footer, "describe a task")
	if planIdx < 0 {
		t.Fatalf("footer should contain the pinned plan, got:\n%s", footer)
	}
	if composerIdx >= 0 && planIdx > composerIdx {
		t.Fatalf("pinned plan should render ABOVE the composer, got:\n%s", footer)
	}
}

// TestParsePlannerOutputText_Simple verifies that numbered plan output is parsed correctly.
func TestParsePlannerOutputText_Simple(t *testing.T) {
	text := "Plan:\n1. Read file\n2. Edit file"
	items := parsePlannerOutputText(text, 12)

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Content != "Read file" {
		t.Errorf("item[0].Content = %q, want %q", items[0].Content, "Read file")
	}
	if items[0].Status != "in_progress" {
		t.Errorf("item[0].Status = %q, want in_progress", items[0].Status)
	}
	if items[1].Content != "Edit file" {
		t.Errorf("item[1].Content = %q, want %q", items[1].Content, "Edit file")
	}
	if items[1].Status != "pending" {
		t.Errorf("item[1].Status = %q, want pending", items[1].Status)
	}
}

// TestParsePlannerOutputText_VariousFormats tests different numbering/bullet formats.
func TestParsePlannerOutputText_VariousFormats(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantText []string
	}{
		{
			name:     "dot-numbered",
			text:     "Plan:\n1. First\n2. Second",
			wantText: []string{"First", "Second"},
		},
		{
			name:     "paren-numbered",
			text:     "Plan:\n1) First\n2) Second",
			wantText: []string{"First", "Second"},
		},
		{
			name:     "dashes",
			text:     "Plan:\n- First\n- Second",
			wantText: []string{"First", "Second"},
		},
		{
			name:     "asterisks",
			text:     "Plan:\n* First\n* Second",
			wantText: []string{"First", "Second"},
		},
		{
			name:     "mixed empty lines",
			text:     "Plan:\n1. First\n\n2. Second\n",
			wantText: []string{"First", "Second"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := parsePlannerOutputText(tt.text, 12)
			if len(items) != len(tt.wantText) {
				t.Fatalf("len(items) = %d, want %d", len(items), len(tt.wantText))
			}
			for i, want := range tt.wantText {
				if items[i].Content != want {
					t.Errorf("items[%d].Content = %q, want %q", i, items[i].Content, want)
				}
			}
		})
	}
}

// TestParsePlannerOutputText_MaxSteps verifies the step limit.
func TestParsePlannerOutputText_MaxSteps(t *testing.T) {
	text := "Plan:\n1. Step 1\n2. Step 2\n3. Step 3\n4. Step 4"
	items := parsePlannerOutputText(text, 2)

	if len(items) != 2 {
		t.Fatalf("expected 2 items (maxSteps=2), got %d", len(items))
	}
}

// TestParsePlannerOutputText_FirstInProgress verifies the first step is marked in_progress.
func TestParsePlannerOutputText_FirstInProgress(t *testing.T) {
	text := "Plan:\n1. First\n2. Second\n3. Third"
	items := parsePlannerOutputText(text, 12)

	if items[0].Status != "in_progress" {
		t.Errorf("first item status = %q, want in_progress", items[0].Status)
	}
	for i := 1; i < len(items); i++ {
		if items[i].Status != "pending" {
			t.Errorf("items[%d].Status = %q, want pending", i, items[i].Status)
		}
	}
}

// TestParsePlannerOutputText_InvalidPrefix verifies non-Plan text is ignored.
func TestParsePlannerOutputText_InvalidPrefix(t *testing.T) {
	text := "Not a plan:\n1. First\n2. Second"
	items := parsePlannerOutputText(text, 12)

	if len(items) != 0 {
		t.Fatalf("expected 0 items for invalid prefix, got %d", len(items))
	}
}

// TestPlanSeedingFromPlannerRow verifies that a planner row seeds the plan if empty.
func TestPlanSeedingFromPlannerRow(t *testing.T) {
	m := newModel(t.Context(), Options{ModelName: "gpt-4"})
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.activeRunID = 42

	// Simulate receiving a planner row
	planText := "Plan:\n1. Read the file\n2. Modify the content"
	row := transcriptRow{
		kind:  rowSystem,
		id:    "planner_42",
		text:  planText,
		runID: 42,
	}

	msg := agentRowMsg{runID: 42, row: row}
	updated, _ := m.Update(msg)
	m = updated.(model)

	// Verify the plan was seeded
	if m.plan.isEmpty() {
		t.Fatal("plan should not be empty after planner row")
	}
	if len(m.plan.steps) != 2 {
		t.Fatalf("expected 2 plan steps, got %d", len(m.plan.steps))
	}
	if m.plan.steps[0].content != "Read the file" {
		t.Errorf("step[0].content = %q, want %q", m.plan.steps[0].content, "Read the file")
	}
	if m.plan.steps[0].status != "in_progress" {
		t.Errorf("step[0].status = %q, want in_progress", m.plan.steps[0].status)
	}
}

// TestPlanSeedingIgnoresExistingPlan verifies that planner rows don't overwrite existing plans.
func TestPlanSeedingIgnoresExistingPlan(t *testing.T) {
	m := newModel(t.Context(), Options{ModelName: "gpt-4"})
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.activeRunID = 42

	// Seed with an existing plan via update_plan tool
	existingItems := []tools.PlanItem{
		{Content: "Existing step 1", Status: "in_progress"},
		{Content: "Existing step 2", Status: "pending"},
	}
	m.plan.updateFromItems(existingItems, now)

	// Simulate receiving a planner row with different content
	planText := "Plan:\n1. New step from planner\n2. Another new step"
	row := transcriptRow{
		kind:  rowSystem,
		id:    "planner_42",
		text:  planText,
		runID: 42,
	}

	msg := agentRowMsg{runID: 42, row: row}
	updated, _ := m.Update(msg)
	m = updated.(model)

	// Verify the plan was NOT overwritten
	if len(m.plan.steps) != 2 {
		t.Fatalf("expected 2 plan steps (unchanged), got %d", len(m.plan.steps))
	}
	if m.plan.steps[0].content != "Existing step 1" {
		t.Errorf("step[0].content = %q, want unchanged", m.plan.steps[0].content)
	}
}

// TestPlannerStartRowIgnored verifies that planner_start rows don't seed the plan.
func TestPlannerStartRowIgnored(t *testing.T) {
	m := newModel(t.Context(), Options{ModelName: "gpt-4"})
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.activeRunID = 42

	// Simulate receiving a planner_start row
	row := transcriptRow{
		kind:  rowSystem,
		id:    "planner_start_42",
		text:  "Planning the approach…",
		runID: 42,
	}

	msg := agentRowMsg{runID: 42, row: row}
	updated, _ := m.Update(msg)
	m = updated.(model)

	// Verify the plan was NOT seeded
	if !m.plan.isEmpty() {
		t.Fatalf("planner_start row should not seed the plan")
	}
}
