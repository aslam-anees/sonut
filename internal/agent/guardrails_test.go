package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

func TestExplicitlyRequiresToolUse(t *testing.T) {
	for _, prompt := range []string{
		"You must use the MCP search tool.",
		"Use the available MCP Firecrawl tool.",
		"You need to use the browser tool for this.",
	} {
		if !explicitlyRequiresToolUse(prompt) {
			t.Errorf("explicitlyRequiresToolUse(%q) = false", prompt)
		}
	}
	for _, prompt := range []string{
		"How do I use the MCP tool?",
		"Explain tool use.",
		"Search if useful.",
	} {
		if explicitlyRequiresToolUse(prompt) {
			t.Errorf("explicitlyRequiresToolUse(%q) = true", prompt)
		}
	}
}

// emptyTurn is a stream that produces no visible text and no tool calls.
func emptyTurn() []sonutruntime.StreamEvent {
	return []sonutruntime.StreamEvent{{Type: sonutruntime.StreamEventDone}}
}

func TestRepeatedReasoningGetsBoundedActNowNudge(t *testing.T) {
	state := newGuardState()
	thought := strings.Repeat("inspect the existing implementation and follow the current plan before making the next edit ", 3)
	if got := state.checkRepeatedReasoning(thought); got != "" {
		t.Fatalf("first thought should establish the baseline, got %q", got)
	}
	if got := state.checkRepeatedReasoning(thought); !strings.Contains(got, "tool call immediately") {
		t.Fatalf("repeated thought should trigger an act-now nudge, got %q", got)
	}
	if got := state.checkRepeatedReasoning(thought); got == "" {
		t.Fatalf("second repeated thought should get the final bounded nudge")
	}
	if got := state.checkRepeatedReasoning(thought); got != "" {
		t.Fatalf("reasoning nudge must be bounded, got %q", got)
	}
}

// textTurn produces a turn with visible assistant text.
func textTurn(content string) []sonutruntime.StreamEvent {
	return []sonutruntime.StreamEvent{
		{Type: sonutruntime.StreamEventText, Content: content},
		{Type: sonutruntime.StreamEventDone},
	}
}

// toolTurn produces a turn that calls a named tool with the given args JSON.
func toolTurn(callID string, toolName string, args string) []sonutruntime.StreamEvent {
	return []sonutruntime.StreamEvent{
		{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: callID, ToolName: toolName},
		{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: callID, ArgumentsFragment: args},
		{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: callID},
		{Type: sonutruntime.StreamEventDone},
	}
}

func countUserMessagesContaining(messages []sonutruntime.Message, needle string) int {
	count := 0
	for _, message := range messages {
		if message.Role == sonutruntime.MessageRoleUser && strings.Contains(message.Content, needle) {
			count++
		}
	}
	return count
}

func TestRunStopsAfterConsecutiveEmptyTurns(t *testing.T) {
	provider := &mockProvider{
		turns: [][]sonutruntime.StreamEvent{
			emptyTurn(),
			emptyTurn(),
			emptyTurn(),
			// A 4th turn exists but must never be requested.
			textTurn("should never reach here"),
		},
	}

	result, err := Run(context.Background(), "go", provider, Options{
		Registry: tools.NewRegistry(),
		MaxTurns: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != maxEmptyTurns {
		t.Fatalf("expected exactly %d turns before the no-output guard fires, got %d", maxEmptyTurns, len(provider.requests))
	}
	if result.Turns != maxEmptyTurns {
		t.Fatalf("expected %d turns recorded, got %d", maxEmptyTurns, result.Turns)
	}
	if !strings.Contains(result.FinalAnswer, "no output") {
		t.Fatalf("expected no-output stop message, got %q", result.FinalAnswer)
	}
	if result.FinalAnswer == maxTurnsAnswer {
		t.Fatalf("no-output guard must stop before reaching maxTurns, got max-turns answer")
	}
}

func TestRunResetsEmptyTurnCounterOnVisibleOutput(t *testing.T) {
	provider := &mockProvider{
		turns: [][]sonutruntime.StreamEvent{
			emptyTurn(),
			emptyTurn(),
			textTurn("here is real progress"), // resets the counter and is the final answer
			emptyTurn(),
		},
	}

	result, err := Run(context.Background(), "go", provider, Options{
		Registry: tools.NewRegistry(),
		MaxTurns: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The text turn ends the run as the final answer (no tool calls), so we
	// stop at turn 3 — the empty counter was reset and never reached the cap.
	if len(provider.requests) != 3 {
		t.Fatalf("expected the run to end on the text turn (3 requests), got %d", len(provider.requests))
	}
	if result.FinalAnswer != "here is real progress" {
		t.Fatalf("expected the visible text as final answer, got %q", result.FinalAnswer)
	}
}

func TestRunResetsEmptyTurnCounterOnToolCall(t *testing.T) {
	root := t.TempDir()
	readCount := staleToolCallThreshold + 2
	for i := 0; i < readCount; i++ {
		writeAgentTestFile(t, fmt.Sprintf("%s/notes%d.txt", root, i), "alpha")
	}
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(root))

	provider := &mockProvider{
		turns: [][]sonutruntime.StreamEvent{
			emptyTurn(),
			emptyTurn(),
			toolTurn("call-1", "read_file", `{"path":"notes.txt"}`), // resets counter
			emptyTurn(),
			emptyTurn(),
			textTurn("done"),
		},
	}

	result, err := Run(context.Background(), "go", provider, Options{
		Registry: registry,
		MaxTurns: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Without a reset, three empty turns would stop the run at turn 3. Because
	// the tool call at turn 3 resets the counter, the run survives the later
	// empty turns and ends with the text answer at turn 6.
	if result.FinalAnswer != "done" {
		t.Fatalf("expected the counter to reset on a tool call and the run to finish, got %q", result.FinalAnswer)
	}
	if len(provider.requests) != 6 {
		t.Fatalf("expected 6 turns, got %d", len(provider.requests))
	}
}

func TestGuardStateResetsToolOnlyStreakOnEmptyNonToolTurn(t *testing.T) {
	var state guardState
	toolOnly := sonutruntime.CollectedStream{
		ToolCalls: []sonutruntime.ToolCall{{ID: "call", Name: "read_file", Arguments: `{}`}},
	}

	for range toolOnlyProgressReminderAt - 1 {
		state.observeTurn(toolOnly)
	}
	state.observeTurn(sonutruntime.CollectedStream{})
	state.observeTurn(toolOnly)

	if reminder := state.progressReminder(); reminder != "" {
		t.Fatalf("expected empty non-tool turn to reset tool-only progress reminder, got %q", reminder)
	}
	if state.toolOnlyTurns != 1 {
		t.Fatalf("expected tool-only streak to restart at 1, got %d", state.toolOnlyTurns)
	}
}

func TestRunDoesNotCountDroppedToolCallTurnsAsEmpty(t *testing.T) {
	provider := &mockProvider{
		turns: [][]sonutruntime.StreamEvent{
			{
				{Type: sonutruntime.StreamEventToolCallDropped},
				{Type: sonutruntime.StreamEventDone},
			},
			{
				{Type: sonutruntime.StreamEventToolCallDropped},
				{Type: sonutruntime.StreamEventDone},
			},
			{
				{Type: sonutruntime.StreamEventToolCallDropped},
				{Type: sonutruntime.StreamEventDone},
			},
			textTurn("recovered"),
		},
	}

	result, err := Run(context.Background(), "go", provider, Options{
		Registry: tools.NewRegistry(),
		MaxTurns: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Dropped-call turns take the retry path and must NOT be counted by the
	// no-output guard; the run continues to the text turn.
	if result.FinalAnswer != "recovered" {
		t.Fatalf("expected dropped-call turns to be handled by the retry path, got %q", result.FinalAnswer)
	}
	if len(provider.requests) != 4 {
		t.Fatalf("expected 4 turns, got %d", len(provider.requests))
	}
}

func TestRunInjectsPlanNotCalledReminderForMultiStepTask(t *testing.T) {
	root := t.TempDir()
	writeAgentTestFile(t, root+"/notes.txt", "alpha")
	writeAgentTestFile(t, root+"/notes0.txt", "alpha")
	writeAgentTestFile(t, root+"/notes1.txt", "beta")
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(root))

	// Distinct paths each turn: byte-identical repeat calls are intercepted by
	// the dedup guard (blocked with a notice) before they reach the failure
	// tracker, which would arm a failure hint that outranks the plan reminder.
	provider := &mockProvider{
		turns: [][]sonutruntime.StreamEvent{
			toolTurn("call-1", "read_file", `{"path":"notes.txt"}`),  // turn 1: other tool call
			toolTurn("call-2", "read_file", `{"path":"notes0.txt"}`), // turn 2: still no update_plan
			toolTurn("call-3", "read_file", `{"path":"notes1.txt"}`), // turn 3: reminder fires here
			textTurn("done"),
		},
	}

	result, err := Run(context.Background(), "go", provider, Options{
		Registry: registry,
		MaxTurns: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("expected final answer, got %q", result.FinalAnswer)
	}
	count := countUserMessagesContaining(result.Messages, planNotCalledReminderMarker)
	if count != 1 {
		t.Fatalf("expected exactly one not-called plan reminder, got %d", count)
	}
}

func TestRunDoesNotInjectPlanReminderForTrivialTask(t *testing.T) {
	root := t.TempDir()
	writeAgentTestFile(t, root+"/notes.txt", "alpha")
	writeAgentTestFile(t, root+"/notes0.txt", "alpha")
	writeAgentTestFile(t, root+"/notes1.txt", "beta")
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(root))

	provider := &mockProvider{
		turns: [][]sonutruntime.StreamEvent{
			toolTurn("call-1", "read_file", `{"path":"notes.txt"}`), // single tool call
			textTurn("done"), // immediately answers
		},
	}

	result, err := Run(context.Background(), "go", provider, Options{
		Registry: registry,
		MaxTurns: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("expected final answer, got %q", result.FinalAnswer)
	}
	if count := countUserMessagesContaining(result.Messages, planNotCalledReminderMarker); count != 0 {
		t.Fatalf("expected no plan reminder for a trivial task, got %d", count)
	}
}

func TestRunDoesNotInjectNotCalledReminderWhenPlanUsed(t *testing.T) {
	root := t.TempDir()
	writeAgentTestFile(t, root+"/notes.txt", "alpha")
	writeAgentTestFile(t, root+"/notes0.txt", "alpha")
	writeAgentTestFile(t, root+"/notes1.txt", "beta")
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(root))
	registry.Register(tools.NewUpdatePlanTool())

	provider := &mockProvider{
		turns: [][]sonutruntime.StreamEvent{
			toolTurn("call-1", "update_plan", `{"plan":[{"content":"step one"}]}`),
			toolTurn("call-2", "read_file", `{"path":"notes.txt"}`),
			textTurn("done"),
		},
	}

	result, err := Run(context.Background(), "go", provider, Options{
		Registry: registry,
		MaxTurns: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("expected final answer, got %q", result.FinalAnswer)
	}
	if count := countUserMessagesContaining(result.Messages, planNotCalledReminderMarker); count != 0 {
		t.Fatalf("expected no not-called reminder when update_plan was used, got %d", count)
	}
}

func TestRunInjectsStalePlanReminderAfterManyToolCalls(t *testing.T) {
	root := t.TempDir()
	readCount := staleToolCallThreshold + 2
	for i := 0; i < readCount; i++ {
		writeAgentTestFile(t, fmt.Sprintf("%s/notes%d.txt", root, i), "alpha")
	}
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(root))
	registry.Register(tools.NewUpdatePlanTool())

	// Turn 1 calls update_plan (so the not-called reminder never triggers), then
	// many read_file turns accumulate without another plan update.
	turns := [][]sonutruntime.StreamEvent{
		toolTurn("plan-1", "update_plan", `{"plan":[{"content":"step one"}]}`),
	}
	// Use a fresh file per filler call so this test isolates stale-plan reminders
	// from the semantic same-file read guard.
	for i := 0; i < readCount; i++ {
		turns = append(turns, toolTurn("call", "read_file", fmt.Sprintf(`{"path":"notes%d.txt"}`, i)))
	}
	turns = append(turns, textTurn("done"))

	provider := &mockProvider{turns: turns}

	result, err := Run(context.Background(), "go", provider, Options{
		Registry: registry,
		MaxTurns: len(turns) + 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("expected final answer, got %q", result.FinalAnswer)
	}
	if count := countUserMessagesContaining(result.Messages, planStaleReminderMarker); count < 1 {
		t.Fatalf("expected at least one stale plan reminder, got %d", count)
	}
}

func TestRunStalePlanReminderIsOneShotPerInterval(t *testing.T) {
	root := t.TempDir()
	readCount := staleToolCallThreshold * 2
	for i := 0; i < readCount; i++ {
		writeAgentTestFile(t, fmt.Sprintf("%s/notes%d.txt", root, i), "alpha")
	}
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(root))
	registry.Register(tools.NewUpdatePlanTool())

	turns := [][]sonutruntime.StreamEvent{
		toolTurn("plan-1", "update_plan", `{"plan":[{"content":"step one"}]}`),
	}
	// Enough tool calls to exceed the threshold by a wide margin; the reminder
	// must fire once for the interval, not on every subsequent turn. Use fresh
	// paths so the duplicate-read circuit breaker is not the behavior under test.
	for i := 0; i < readCount; i++ {
		turns = append(turns, toolTurn("call", "read_file", fmt.Sprintf(`{"path":"notes%d.txt"}`, i)))
	}
	turns = append(turns, textTurn("done"))

	provider := &mockProvider{turns: turns}

	result, err := Run(context.Background(), "go", provider, Options{
		Registry: registry,
		MaxTurns: len(turns) + 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("expected final answer, got %q", result.FinalAnswer)
	}
	count := countUserMessagesContaining(result.Messages, planStaleReminderMarker)
	if count != 1 {
		t.Fatalf("expected the stale reminder to be one-shot per interval (exactly 1), got %d", count)
	}
}

func TestRunInjectsToolOnlyProgressReminder(t *testing.T) {
	root := t.TempDir()
	readCount := toolOnlyProgressReminderAt
	for i := 0; i < readCount; i++ {
		writeAgentTestFile(t, fmt.Sprintf("%s/notes%d.txt", root, i), "alpha")
	}
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(root))

	turns := make([][]sonutruntime.StreamEvent, 0, toolOnlyProgressReminderAt+1)
	for i := 0; i < readCount; i++ {
		turns = append(turns, toolTurn("call", "read_file", fmt.Sprintf(`{"path":"notes%d.txt"}`, i)))
	}
	turns = append(turns, textTurn("done"))

	provider := &mockProvider{turns: turns}
	result, err := Run(context.Background(), "go", provider, Options{
		Registry: registry,
		MaxTurns: len(turns) + 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("expected final answer, got %q", result.FinalAnswer)
	}
	if count := countUserMessagesContaining(result.Messages, toolOnlyProgressReminderMarker); count != 1 {
		t.Fatalf("expected one tool-only progress reminder, got %d", count)
	}
	found := false
	for _, message := range provider.requests[toolOnlyProgressReminderAt].Messages {
		if message.Role == sonutruntime.MessageRoleUser && strings.Contains(message.Content, toolOnlyProgressReminderMarker) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected reminder on request after tool-only streak, messages: %+v", provider.requests[toolOnlyProgressReminderAt].Messages)
	}
}

type alwaysFailingTool struct{}

func (alwaysFailingTool) Name() string        { return "flaky" }
func (alwaysFailingTool) Description() string { return "always fails for testing" }
func (alwaysFailingTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object", AdditionalProperties: false}
}
func (alwaysFailingTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectRead, Permission: tools.PermissionAllow}
}
func (alwaysFailingTool) Run(context.Context, map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusError, Output: "Error: Invalid arguments for flaky: thing is required"}
}

// repeatedFlakyTurns issues n flaky calls with DIFFERENT arguments each turn.
// Byte-identical repeats would be short-circuited by the consecutive-identical-
// call dedup guard before reaching the repeated-failure tracker; varying the
// args keeps every call executing so the failure streak actually accrues.
func repeatedFlakyTurns(n int) [][]sonutruntime.StreamEvent {
	turns := make([][]sonutruntime.StreamEvent, 0, n)
	for i := 0; i < n; i++ {
		turns = append(turns, toolTurn("c", "flaky", fmt.Sprintf(`{"attempt":%d}`, i)))
	}
	return turns
}

func TestRunStopsAfterRepeatedToolFailures(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(alwaysFailingTool{})
	provider := &mockProvider{turns: repeatedFlakyTurns(10)}

	result, err := Run(context.Background(), "go", provider, Options{Registry: registry, MaxTurns: 12})
	if err != nil {
		t.Fatal(err)
	}
	// Byte-identical repeats trip the consecutive-call guard at the same streak
	// the repeated-failure guard stops at; either stop answer is a valid halt.
	if !strings.Contains(result.FinalAnswer, "flaky") ||
		(!strings.Contains(result.FinalAnswer, "failed") && !strings.Contains(result.FinalAnswer, "in a row")) {
		t.Fatalf("expected a guard stop answer, got %q", result.FinalAnswer)
	}
	// Must halt at the failure cap, NOT loop to maxTurns.
	if len(provider.requests) != toolFailureStopAt {
		t.Fatalf("expected stop at %d failures, made %d requests", toolFailureStopAt, len(provider.requests))
	}
}

func TestRunInjectsToolFailureHintWithSchema(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(alwaysFailingTool{})
	provider := &mockProvider{turns: repeatedFlakyTurns(10)}

	if _, err := Run(context.Background(), "go", provider, Options{Registry: registry, MaxTurns: 12}); err != nil {
		t.Fatal(err)
	}
	// After the 2nd failure a one-shot hint is injected, so the 3rd turn's request
	// carries it (with the tool schema).
	found := false
	for _, m := range provider.requests[2].Messages {
		if m.Role == sonutruntime.MessageRoleUser && strings.Contains(m.Content, toolFailureHintMarker) {
			found = true
			if !strings.Contains(m.Content, "object") { // schema rendered
				t.Errorf("hint should include the tool schema, got %q", m.Content)
			}
		}
	}
	if !found {
		t.Fatalf("expected a tool-failure hint on the 3rd turn, messages: %+v", provider.requests[2].Messages)
	}
}

// (a) Plan-state feedback loop fix: the pending-plan nudge must be
// prescriptive (name the exact call to make), not merely descriptive
// ("items pending"), so the model acts instead of re-narrating the plan.
func TestPlanPendingNudgeIsPrescriptive(t *testing.T) {
	nudge := planPendingNudge()
	if !strings.Contains(nudge, planPendingNudgeMarker) {
		t.Fatalf("expected nudge to contain %q, got %q", planPendingNudgeMarker, nudge)
	}
	if !strings.Contains(nudge, "execute the next pending step with a tool call") {
		t.Fatalf("expected nudge to prescribe the next concrete action, got %q", nudge)
	}
	if strings.Contains(nudge, "do not restate the plan") == false &&
		!strings.Contains(strings.ToLower(nudge), "do not restate the plan") {
		t.Fatalf("expected nudge to forbid restating the plan, got %q", nudge)
	}
}

// (b) Auto-reconcile: a pending plan step that names a file which was actually
// written this run, combined with the model's own text signaling completion,
// must produce a step-specific reminder to call update_plan rather than a
// generic nudge.
func TestObviousStepCompletionReminderFiresWhenFileWritten(t *testing.T) {
	state := newGuardState()
	state.observePlanUpdate(`{"plan":[{"content":"Create style.css for the layout","status":"in_progress"}]}`)
	state.recordWrittenFile("style.css")

	reminder := state.obviousStepCompletionReminder("I created style.css with the layout rules, done.")
	if reminder == "" {
		t.Fatalf("expected a step-completion reminder, got empty string")
	}
	if !strings.Contains(reminder, "style.css") {
		t.Fatalf("expected reminder to name the file, got %q", reminder)
	}
	if !strings.Contains(reminder, obviousStepCompletionReminderMarker) {
		t.Fatalf("expected reminder to instruct calling update_plan, got %q", reminder)
	}
}

func TestCompleteWrittenFileStepsUpdatesOnlyMatchingFileStep(t *testing.T) {
	state := newGuardState()
	state.observePlanUpdate(`{"plan":[{"content":"Create mariogame.html with canvas rendering","status":"in_progress"},{"content":"Implement player movement","status":"pending"}]}`)
	state.recordWrittenFile("mariogame.html")

	items, changed := state.completeWrittenFileSteps()
	if !changed || len(items) != 2 {
		t.Fatalf("completion = (%v, %#v), want full changed roadmap", changed, items)
	}
	if items[0].Status != "completed" || items[1].Status != "in_progress" {
		t.Fatalf("unexpected reconciled statuses: %#v", items)
	}
}

func TestSeedPlannerPlanEnablesWriteDrivenProgress(t *testing.T) {
	state := newGuardState()
	state.seedPlannerPlan("1. Create mariogame.html with canvas rendering\n2. Implement player movement")
	state.recordWrittenFile("mariogame.html")

	items, changed := state.completeWrittenFileSteps()
	if !changed || len(items) != 2 || items[0].Status != "completed" || items[1].Status != "in_progress" {
		t.Fatalf("seeded plan did not reconcile write: changed=%v items=%#v", changed, items)
	}
}

func TestFirstPendingPlanFileSkipsWrittenPaths(t *testing.T) {
	state := newGuardState()
	state.seedPlannerPlan("1. Create index.html with canvas\n2. Create game.js with loop\n3. Verify in browser")
	if got := state.firstPendingPlanFile(); got != "index.html" {
		t.Fatalf("first pending file = %q, want index.html", got)
	}
	state.recordWrittenFile("index.html")
	_, _ = state.completeWrittenFileSteps()
	if got := state.firstPendingPlanFile(); got != "game.js" {
		t.Fatalf("after index.html write, next file = %q, want game.js", got)
	}
}

func TestNextFileProgressNudgeAllowsWriteFileForNewPath(t *testing.T) {
	nudge := nextFileProgressNudge("Create game.js with loop", "game.js", false)
	if !strings.Contains(nudge, "write_file") {
		t.Fatalf("nudge must allow write_file for new paths, got %q", nudge)
	}
	if !strings.Contains(nudge, "game.js") {
		t.Fatalf("nudge must name next file, got %q", nudge)
	}
	// Old failure mode banned write_file entirely ("edit_file or append_file only").
	if strings.Contains(nudge, "edit_file or append_file only") {
		t.Fatalf("nudge must not ban write_file for multi-file progress: %q", nudge)
	}
	finished := nextFileProgressNudge("", "", true)
	if !strings.Contains(finished, "final answer") {
		t.Fatalf("finished nudge should allow final answer, got %q", finished)
	}
}

func TestWrittenFilePathReconciliationIsExact(t *testing.T) {
	state := newGuardState()
	state.observePlanUpdate(`{"plan":[{"content":"Create workspace/mariogame.html","status":"in_progress"},{"content":"Create other/mariogame.html","status":"pending"}]}`)
	state.recordWrittenFile("/Users/aslam/Desktop/aistatine/sonut/workspace/mariogame.html")

	items, changed := state.completeWrittenFileSteps()
	if changed {
		t.Fatalf("absolute path must not satisfy an explicit relative directory without normalization, got %#v", items)
	}
	state.recordWrittenFile("workspace/mariogame.html")
	items, changed = state.completeWrittenFileSteps()
	if !changed || items[0].Status != "completed" || items[1].Status != "in_progress" {
		t.Fatalf("exact path reconciliation mismatch: changed=%v items=%#v", changed, items)
	}
}

// obviousStepCompletionReminder must NOT fire when the named file was never
// actually written (no evidence), even if the model's text sounds confident.
func TestObviousStepCompletionReminderSkipsWithoutWrittenFile(t *testing.T) {
	state := newGuardState()
	state.observePlanUpdate(`{"plan":[{"content":"Create style.css for the layout","status":"in_progress"}]}`)

	if reminder := state.obviousStepCompletionReminder("style.css is done."); reminder != "" {
		t.Fatalf("expected no reminder without evidence of a write, got %q", reminder)
	}
}

// A UI-touching step marked completed after only a NON-browser tool success
// (e.g. write_file) must be rejected: rendering/interaction was never verified.
func TestUIStepCompletionRequiresBrowserVerification(t *testing.T) {
	state := newGuardState()
	state.observePlanUpdate(`{"plan":[{"content":"Build index.html with a working click button","status":"in_progress"}]}`)
	// A file write succeeds, but that does not prove the page renders or works.
	state.observeToolResult("write_file", false, "")

	nudge := state.observePlanUpdate(`{"plan":[{"content":"Build index.html with a working click button","status":"completed"}]}`)
	if nudge == "" {
		t.Fatalf("expected UI step completion to be rejected without a browser verification")
	}
	if !strings.Contains(nudge, unverifiedUIStepCompletionMarker) {
		t.Fatalf("expected browser-specific rejection, got %q", nudge)
	}
}

// The same UI step is accepted once an actual browser verification has run.
func TestUIStepCompletionAcceptedAfterBrowserVerify(t *testing.T) {
	state := newGuardState()
	state.observePlanUpdate(`{"plan":[{"content":"Build index.html with a working click button","status":"in_progress"}]}`)
	state.observeToolResult("write_file", false, "")
	state.observeToolResult("mcp_playwright_browser_snapshot", false, "") // rendered page inspected

	if nudge := state.observePlanUpdate(`{"plan":[{"content":"Build index.html with a working click button","status":"completed"}]}`); nudge != "" {
		t.Fatalf("expected UI step to be accepted after a browser verification, got %q", nudge)
	}
}

func TestBrowserConsoleErrorSummaryDoesNotVerifyFrontend(t *testing.T) {
	state := newGuardState()
	state.recordWrittenFile("index.html")

	// Playwright can return StatusOK for the transport call while reporting
	// page-runtime failures in the result text.
	state.observeToolResult("mcp_playwright_browser_navigate", false, "Opened page\nConsole: 2 errors, 0 warnings")
	if !state.needsBrowserVerification() {
		t.Fatal("a browser result reporting console errors must not clear verification")
	}
	if state.successfulBrowserVerifySincePlanUpdate != 0 {
		t.Fatal("a browser result reporting console errors must not count as successful evidence")
	}

	state.observeToolResult("mcp_playwright_browser_console_messages", false, "No console messages")
	if state.needsBrowserVerification() {
		t.Fatal("a subsequent clean console check should satisfy the frontend gate")
	}
}

func TestBrowserConsoleErrorSummaryMatching(t *testing.T) {
	for _, tc := range []struct {
		output string
		error  bool
	}{
		{output: "Console: 2 errors, 0 warnings", error: true},
		{output: "Uncaught TypeError: x is not a function", error: true},
		{output: "Console: 0 errors, 0 warnings", error: false},
		{output: "No console messages", error: false},
	} {
		if got := browserOutputHasConsoleErrors(tc.output); got != tc.error {
			t.Errorf("browserOutputHasConsoleErrors(%q) = %v, want %v", tc.output, got, tc.error)
		}
	}
}

func TestFrontendWriteRequiresBrowserEvenWhenBrowserWasNeverOpened(t *testing.T) {
	state := newGuardState()
	state.recordWrittenFile("src/App.tsx")

	if !state.needsBrowserVerification() {
		t.Fatal("frontend write must require browser verification")
	}
	nudge := state.checkBrowserVerifyNudge()
	if !strings.Contains(nudge, "mcp_playwright_browser_navigate") {
		t.Fatalf("nudge must name the real Playwright tool, got %q", nudge)
	}

	// A failed attempt is not verification.
	state.observeToolResult("mcp_playwright_browser_snapshot", true, "browser unavailable")
	if !state.needsBrowserVerification() {
		t.Fatal("failed browser call must not clear frontend verification")
	}

	state.observeToolResult("mcp_playwright_browser_snapshot", false, "snapshot")
	if state.needsBrowserVerification() {
		t.Fatal("successful browser call after the write must clear verification")
	}

	// A later edit changes what is rendered and therefore re-arms the check.
	state.recordWrittenFile("src/App.tsx")
	if !state.needsBrowserVerification() || state.checkBrowserVerifyNudge() == "" {
		t.Fatal("a subsequent frontend write must re-arm the browser nudge")
	}
}

// A non-UI step keeps the original contract: ANY successful tool call verifies it.
func TestNonUIStepCompletionUnaffectedByBrowserGate(t *testing.T) {
	state := newGuardState()
	state.observePlanUpdate(`{"plan":[{"content":"Refactor the JSON parser in parser.go","status":"in_progress"}]}`)
	state.observeToolResult("run_shell", false, "")

	if nudge := state.observePlanUpdate(`{"plan":[{"content":"Refactor the JSON parser in parser.go","status":"completed"}]}`); nudge != "" {
		t.Fatalf("non-UI step verified by a shell run must not be gated on the browser, got %q", nudge)
	}
}

func TestLooksLikeUIStepAndBrowserVerifyPredicates(t *testing.T) {
	uiSteps := []string{"Create mario.html with canvas", "wire the click handler on the button", "polish the CSS layout"}
	for _, s := range uiSteps {
		if !looksLikeUIStep(s) {
			t.Fatalf("expected %q to be a UI step", s)
		}
	}
	nonUI := []string{"Refactor parser.go error handling", "add a unit test for the summer func"}
	for _, s := range nonUI {
		if looksLikeUIStep(s) {
			t.Fatalf("expected %q not to be a UI step", s)
		}
	}
	for _, name := range []string{"mcp_playwright_browser_click", "browser_snapshot", "capture_screenshot"} {
		if !isBrowserVerifyToolName(name) {
			t.Fatalf("expected %q to count as browser verification", name)
		}
	}
	for _, name := range []string{"read_file", "run_shell", "write_file"} {
		if isBrowserVerifyToolName(name) {
			t.Fatalf("expected %q not to count as browser verification", name)
		}
	}
}

// (c) Duplicate-narration detector: the same plan recap emitted twice in a row
// must be flagged; genuinely new content must not be.
func TestCheckDuplicateNarrationDetectsRecapButNotNewContent(t *testing.T) {
	state := newGuardState()
	recap := "Step 1 is done: I created style.css. Step 2 is not yet started: add the DuckDuckGo API integration."

	if nudge := state.checkDuplicateNarration(recap); nudge != "" {
		t.Fatalf("first occurrence should not be flagged as duplicate, got %q", nudge)
	}
	nudge := state.checkDuplicateNarration(recap)
	if nudge == "" {
		t.Fatalf("expected the repeated recap to be flagged as duplicate narration")
	}
	if !strings.Contains(strings.ToLower(nudge), duplicateNarrationMarker) {
		t.Fatalf("expected nudge to contain %q, got %q", duplicateNarrationMarker, nudge)
	}

	// Second consecutive trigger escalates to a tool-call-only demand.
	escalated := state.checkDuplicateNarration(recap)
	if !strings.Contains(escalated, "tool call only") {
		t.Fatalf("expected escalated nudge to demand a tool-call-only reply, got %q", escalated)
	}

	// Genuinely new content resets the streak and is not flagged.
	if nudge := state.checkDuplicateNarration("I finished implementing the DuckDuckGo API integration and verified it with a test call."); nudge != "" {
		t.Fatalf("new content must not be flagged as duplicate, got %q", nudge)
	}
}

// (d) A scripted session where the model marks steps done immediately (in the
// SAME turn as the verifying tool call, and in a strictly increasing manner)
// must never trigger a plan-pending nudge, a duplicate-narration nudge, or a
// stale-plan reminder.
func TestRunNoNudgesWhenPlanKeptCurrent(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewUpdatePlanTool())
	registry.Register(tools.NewWriteFileTool(t.TempDir()))

	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		toolTurn("plan1", "update_plan", `{"plan":[{"content":"Create style.css","status":"in_progress"},{"content":"Add DuckDuckGo API","status":"pending"}]}`),
		toolTurn("write1", "write_file", `{"path":"style.css","content":"body { color: red; }","overwrite":true}`),
		toolTurn("plan2", "update_plan", `{"plan":[{"content":"Create style.css","status":"completed"},{"content":"Add DuckDuckGo API","status":"in_progress"}]}`),
		textTurn("Both steps are complete."),
	}}

	result, err := Run(context.Background(), "build the page", provider, Options{
		Registry: registry,
		MaxTurns: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "Both steps are complete." {
		t.Fatalf("unexpected final answer: %q", result.FinalAnswer)
	}
	for _, marker := range []string{planPendingNudgeMarker, duplicateNarrationMarker, planStaleReminderMarker, "done via update_plan"} {
		if someRequestContains(provider.requests, marker) {
			t.Fatalf("expected no nudge containing %q when the plan is kept current", marker)
		}
	}
}
