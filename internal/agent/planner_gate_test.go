package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

// When Options.Evaluate is on and the request is non-trivial, the run must
// begin with a separate planner pass (fresh minimal context, its own strict
// system prompt) before the generator's first turn, and the resulting plan
// must land in the generator's conversation.
func TestPlannerRunsWhenEvaluateOnAndPlanLandsInConversation(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("1. Update the config file\n   - Do: edit_file config with the requested port value\n   - Done when: the new port is present\n2. Update dependent settings\n   - Do: edit_file related config references\n   - Done when: references use the same port\n3. Restart the service\n   - Do: exec_command the focused restart command\n   - Done when: the service reports healthy"), // planner pass
		textTurn("Done, the config is updated."), // generator's only turn
	}}

	result, err := Run(context.Background(), "please update the server config port and restart it", provider, Options{
		Registry: tools.NewRegistry(),
		Evaluate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "Done, the config is updated." {
		t.Fatalf("unexpected final answer: %q", result.FinalAnswer)
	}

	if len(provider.requests) != 2 {
		t.Fatalf("expected 2 provider requests (planner + generator), got %d", len(provider.requests))
	}

	plannerReq := provider.requests[0]
	if len(plannerReq.Messages) != 2 {
		t.Fatalf("expected a 2-message planner request (system + user), got %d messages", len(plannerReq.Messages))
	}
	// Planner should use the same system prompt as the main run (reuse KV cache)
	mainSystemPrompt := BuildSystemPrompt(Options{})
	if plannerReq.Messages[0].Content != mainSystemPrompt {
		t.Fatalf("planner message[0] should match main system prompt for KV cache reuse; got %q", plannerReq.Messages[0].Content)
	}
	// Planner instruction should be in the last user message, not as a separate system prompt
	if !strings.Contains(plannerReq.Messages[1].Content, "Before executing anything") {
		t.Fatalf("planner instruction not found in user message: %q", plannerReq.Messages[1].Content)
	}
	if len(plannerReq.Tools) != 0 {
		t.Fatal("planner pass must be tool-less")
	}
	if plannerReq.ReasoningEffort != "medium" {
		t.Fatalf("planner must run at medium thinking (harness: planner thinks deeply, executor thinks briefly), got %q", plannerReq.ReasoningEffort)
	}

	generatorReq := provider.requests[1]
	if !someMessageContains(generatorReq.Messages, "Update the config file") {
		t.Fatal("expected the planner's plan to be injected into the generator conversation")
	}
	// Plan-seeded executor uses concise private thinking on all providers so it
	// acts instead of re-planning. Effort is flash ("none") for the whole phase.
	if generatorReq.DisableThinking {
		t.Fatal("generator after a seeded plan must keep concise private thinking enabled")
	}
	if generatorReq.ReasoningEffort != "none" {
		t.Fatalf("first generator turn after a seeded plan ReasoningEffort = %q, want none", generatorReq.ReasoningEffort)
	}
}

func TestUnifiedWorkflowUsesOneContinuousModelLoop(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("Implemented and verified."),
	}}
	_, err := Run(context.Background(), "create a mario-like game", provider, Options{
		Registry:        tools.NewRegistry(),
		Evaluate:        true,
		RunMode:         RunModeBuilder,
		UnifiedWorkflow: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("unified workflow must not launch planner or validator requests, got %d", len(provider.requests))
	}
	if !someMessageContains(provider.requests[0].Messages, "one continuous coding agent") {
		t.Fatal("unified workflow contract was not injected into the model context")
	}
	if provider.requests[0].DisableThinking || provider.requests[0].ReasoningEffort != "none" {
		t.Fatalf("short unified build must fast-start with concise thinking, got disable=%v effort=%q", provider.requests[0].DisableThinking, provider.requests[0].ReasoningEffort)
	}
}

func TestUnifiedWorkflowPrioritizesFirstHTMLGameWriteAndBrowserProof(t *testing.T) {
	messages := []sonutruntime.Message{
		{Role: sonutruntime.MessageRoleSystem, Content: "system"},
		{Role: sonutruntime.MessageRoleUser, Content: "build a playable game"},
	}
	got := injectUnifiedWorkflow(messages, false)
	if len(got) != len(messages) {
		t.Fatalf("workflow injection changed message count: got %d, want %d", len(got), len(messages))
	}
	workflow := got[0].Content
	for _, want := range []string{
		"first action listing the empty directory",
		"<!DOCTYPE html>",
		"append_file",
		"actual entries",
		"press ArrowRight and Space",
		"exact absolute path",
		"never invent a /home/user path",
		"A write-only turn is not completion",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("unified workflow missing %q: %s", want, workflow)
		}
	}
}

// Explicit builder mode must not let a short implementation request bypass
// the planner. This is the contract that keeps the visible plan panel and the
// builder's plan-of-record synchronized from the very first action.
func TestBuilderModeAlwaysPlansShortBuildRequest(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("1. Fix the typo\n   - Do: edit_file the target file with the correct text\n   - Done when: the typo is gone\n2. Check nearby references\n   - Do: grep for the old text and update matches\n   - Done when: no stale match remains\n3. Verify the result\n   - Do: run the focused test\n   - Done when: the check passes"),
		textTurn("Fixed and verified."),
	}}
	var gotPlan string
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreTools("") {
		registry.Register(tool)
	}
	result, err := Run(context.Background(), "fix typo", provider, Options{
		Registry: registry,
		Evaluate: true,
		RunMode:  RunModeBuilder,
		OnPlan:   func(plan string) { gotPlan = plan },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "Fixed and verified." {
		t.Fatalf("unexpected final answer: %q", result.FinalAnswer)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("expected planner + builder requests, got %d", len(provider.requests))
	}
	if !strings.Contains(gotPlan, "Fix the typo") {
		t.Fatalf("short builder request did not surface its plan: %q", gotPlan)
	}
	if provider.requests[1].DisableThinking {
		t.Fatal("builder request after planning must keep concise private thinking enabled")
	}
	for _, tool := range provider.requests[1].Tools {
		if tool.Name == "update_plan" {
			t.Fatalf("executor request must not expose update_plan, got %#v", provider.requests[1].Tools)
		}
	}
	if len(provider.requests[1].Tools) == 0 {
		t.Fatal("executor must receive work tools immediately after the planner finishes")
	}
}

// Browser validation must still receive Playwright MCP tools when normal
// executor advertisement would defer or hide permission-prompted network
// tools. Otherwise the planner is required to validate the page but has no
// capability with which to do it.
func TestPlannerValidationExposesDeferredPromptedBrowserTools(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(plannerValidationBrowserTool{})
	registry.Register(plannerValidationDeniedBrowserTool{})
	registry.Register(tools.NewAskUserTool())

	definitions := plannerValidationToolDefinitions(registry, PermissionModeAuto, nil, nil)
	if !containsRuntimeTool(definitions, "mcp_playwright_browser_snapshot") {
		t.Fatalf("planner validation must expose a prompted Playwright browser tool: %#v", definitions)
	}
	if containsRuntimeTool(definitions, "mcp_playwright_browser_click_denied") {
		t.Fatalf("planner validation must not expose a denied browser tool: %#v", definitions)
	}
	if !containsRuntimeTool(definitions, "ask_user") {
		t.Fatalf("planner validation must expose ask_user for bounded browser handoff: %#v", definitions)
	}

	definitions = plannerValidationToolDefinitions(registry, PermissionModeAuto, nil, []string{"mcp_playwright_browser_snapshot"})
	if containsRuntimeTool(definitions, "mcp_playwright_browser_snapshot") {
		t.Fatal("explicitly disabled browser tool must stay hidden from planner validation")
	}
}

func TestPlannerBrowserEvidenceRequiresOpenInspectAndInteractionOrConsole(t *testing.T) {
	okResult := tools.Result{Status: tools.StatusOK, Output: "ok"}
	var evidence plannerBrowserEvidence
	// Open alone is not enough.
	evidence.observe("web_browser", map[string]any{"action": "navigate"}, okResult)
	if evidence.complete() {
		t.Fatal("open-only evidence must not pass")
	}
	// Open + inspect still needs interaction OR clean console.
	evidence.observe("web_browser", map[string]any{"action": "snapshot"}, okResult)
	if evidence.complete() {
		t.Fatal("open+inspect without interaction/console must not pass")
	}
	// Interaction after open+inspect is sufficient (post-interaction re-check optional).
	evidence.observe("web_browser", map[string]any{"action": "press"}, okResult)
	if !evidence.complete() {
		t.Fatalf("open+inspect+interaction must pass: %#v", evidence)
	}

	// Alternate path: open + inspect + clean console (static pages, no interaction).
	var static plannerBrowserEvidence
	static.observe("web_browser", map[string]any{"action": "navigate"}, okResult)
	static.observe("web_browser", map[string]any{"action": "screenshot"}, tools.Result{
		Status: tools.StatusOK,
		Images: []sonutruntime.ImageBlock{{MediaType: "image/png", Data: []byte{1}}},
	})
	static.observe("web_browser", map[string]any{"action": "console_logs"}, tools.Result{
		Status: tools.StatusOK,
		Output: "no console messages",
	})
	if !static.complete() {
		t.Fatalf("open+screenshot+clean console must pass: %#v", static)
	}
}

func TestPlannerBrowserEvidenceRejectsConsoleErrors(t *testing.T) {
	var evidence plannerBrowserEvidence
	evidence.observe("web_browser", map[string]any{"action": "console_logs"}, tools.Result{
		Status: tools.StatusOK,
		Output: "Uncaught TypeError: cannot read properties of undefined",
	})
	if evidence.consoleClean {
		t.Fatal("uncaught browser console error must block validation")
	}
}

func TestPlannerBrowserEvidenceRejectsConsoleErrorsReportedByNavigate(t *testing.T) {
	var evidence plannerBrowserEvidence
	evidence.observe("mcp_playwright_browser_navigate", map[string]any{"url": "file:///tmp/game.html"}, tools.Result{
		Status: tools.StatusOK,
		Output: "Opened local page\nConsole: 2 errors, 0 warnings",
	})
	evidence.observe("mcp_playwright_browser_snapshot", nil, tools.Result{
		Status: tools.StatusOK,
		Output: "<canvas>",
	})
	evidence.observe("mcp_playwright_browser_press_key", map[string]any{"key": "ArrowRight"}, tools.Result{
		Status: tools.StatusOK,
		Output: "pressed ArrowRight",
	})
	if evidence.complete() {
		t.Fatal("navigate-reported console errors must block planner validation even after interaction")
	}
}

func TestPlannerValidationLoopExhaustionFailsClosed(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(plannerValidationBrowserTool{})
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		toolTurn("b1", "mcp_playwright_browser_snapshot", `{}`),
		toolTurn("b2", "mcp_playwright_browser_snapshot", `{}`),
		toolTurn("b3", "mcp_playwright_browser_snapshot", `{}`),
		toolTurn("b4", "mcp_playwright_browser_snapshot", `{}`),
		toolTurn("b5", "mcp_playwright_browser_snapshot", `{}`),
		toolTurn("b6", "mcp_playwright_browser_snapshot", `{}`),
		toolTurn("b7", "mcp_playwright_browser_snapshot", `{}`),
		toolTurn("b8", "mcp_playwright_browser_snapshot", `{}`),
	}}
	passed, feedback, _, err := validateWithPlanner(
		context.Background(),
		provider,
		"system",
		"build a browser game",
		nil,
		"done",
		Options{Registry: registry},
	)
	if err != nil {
		t.Fatal(err)
	}
	if passed {
		t.Fatal("exhausted browser validation loop must fail closed")
	}
	if !strings.Contains(feedback, "bounded validation loop ended") {
		t.Fatalf("missing actionable exhaustion feedback: %q", feedback)
	}
}

func TestPlannerValidationPassesOnlyAfterHeadfulInteractionEvidence(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(plannerValidationWebBrowserTool{})
	// Sufficient chain: open → inspect → interact → PASS (post-interaction
	// re-check is optional under the loosened evidence gate).
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		toolTurn("v1", "web_browser", `{"action":"navigate","url":"http://127.0.0.1:3000"}`),
		toolTurn("v2", "web_browser", `{"action":"snapshot"}`),
		toolTurn("v3", "web_browser", `{"action":"press","key":"ArrowRight"}`),
		textTurn("VERDICT: PASS\nPLAN:\n1. [completed] Browser game validated"),
	}}
	passed, feedback, _, err := validateWithPlanner(
		context.Background(),
		provider,
		"system",
		"build a browser game",
		nil,
		"done",
		Options{Registry: registry},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !passed {
		t.Fatalf("complete headful evidence was rejected: %q", feedback)
	}
	if len(provider.requests) != 4 {
		t.Fatalf("validation requests = %d, want three browser actions plus verdict", len(provider.requests))
	}
}

func TestPlannerValidationMalformedVerdictFailsClosed(t *testing.T) {
	if passed, _, _, err := parsePlannerValidation("looks good to me"); err == nil || passed {
		t.Fatalf("malformed validation must fail closed, got passed=%v err=%v", passed, err)
	}
}

// Image understanding belongs to the planner pass. The builder must receive
// the resulting visual plan, not the raw image again on its first tool turn.
func TestImageIsAnalyzedOnceBeforeBuilder(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("1. Match the screenshot's dark layout and blue accent\n   - Do: edit_file the UI styles with the reference palette\n   - Done when: the layout and accent match\n2. Update the existing UI\n   - Do: edit_file the existing component, preserving its structure\n   - Done when: the requested controls render\n3. Verify the rendered page\n   - Do: open the page and inspect the result\n   - Done when: the UI renders without errors"),
		textTurn("Implemented the visual changes."),
	}}
	image := sonutruntime.ImageBlock{MediaType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}}
	_, err := Run(context.Background(), "make our code work like the attached image", provider, Options{
		Registry: tools.NewRegistry(),
		Evaluate: true,
		RunMode:  RunModeBuilder,
		Images:   []sonutruntime.ImageBlock{image},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("expected image planner + builder requests, got %d", len(provider.requests))
	}
	if len(provider.requests[0].Messages[1].Images) != 1 {
		t.Fatal("planner request must carry the attached image")
	}
	if !strings.Contains(provider.requests[0].Messages[0].Content, "visual intake and planning stage") {
		t.Fatal("image planner must use the compact visual-intake prompt")
	}
	for _, message := range provider.requests[1].Messages {
		if len(message.Images) != 0 {
			t.Fatal("builder request must use the analyzed plan instead of re-encoding the image")
		}
	}
	if !someMessageContains(provider.requests[1].Messages, "dark layout") {
		t.Fatal("builder request must contain the image-derived plan")
	}
}

// Quick mode is intentionally one request with no tools and no planner.
// Thinking stays ON, but factual Q&A uses low effort to avoid a long idle
// monologue before a direct answer.
func TestQuickModeIsToollessAndSkipsPlanner(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("The answer is 42."),
	}}
	registry := tools.NewRegistry()
	registry.Register(&stubReadTool{})
	_, err := Run(context.Background(), "what is the answer?", provider, Options{
		Registry: registry,
		Evaluate: true,
		RunMode:  RunModeQuick,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("quick mode must make one request, got %d", len(provider.requests))
	}
	if len(provider.requests[0].Tools) != 0 {
		t.Fatalf("quick mode must advertise no tools, got %d", len(provider.requests[0].Tools))
	}
	if provider.requests[0].DisableThinking {
		t.Fatal("quick mode must keep thinking enabled for casual Q&A")
	}
	if !strings.EqualFold(provider.requests[0].ReasoningEffort, "low") {
		t.Fatalf("quick mode effort = %q, want low", provider.requests[0].ReasoningEffort)
	}
}

// A planner error must never fail the run — it logs and the run continues
// plannerless.
func TestPlannerErrorIsNonFatal(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		{{Type: sonutruntime.StreamEventError, Content: "boom"}},
		textTurn("Done anyway."),
	}}

	result, err := Run(context.Background(), "please refactor the payment module and update all of its call sites", provider, Options{
		Registry: tools.NewRegistry(),
		Evaluate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "Done anyway." {
		t.Fatalf("expected the run to continue plannerless and finish, got %q", result.FinalAnswer)
	}
}

// When Options.Evaluate is off, the planner must never run.
func TestPlannerSkippedWhenEvaluateOff(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("The answer is 42."),
	}}
	_, err := Run(context.Background(), "please explain how the caching layer works in detail", provider, Options{
		Registry: tools.NewRegistry(),
		Evaluate: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("expected exactly 1 provider request (no planner pass), got %d", len(provider.requests))
	}
}

// Trivial single-step requests must skip the planner even with Evaluate on.
func TestPlannerSkippedForTrivialRequest(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("Hi there!"),
	}}
	_, err := Run(context.Background(), "hello", provider, Options{
		Registry: tools.NewRegistry(),
		Evaluate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("expected exactly 1 provider request (planner skipped for trivial request), got %d", len(provider.requests))
	}
}

// Questions get answers, not plans — the planner must skip interrogative
// prompts even when they are long enough to pass the word-count gate.
func TestPlannerSkippedForQuestions(t *testing.T) {
	for _, prompt := range []string{
		"how many occurrences of the letter r are in strawberry",
		"who are you and what can you do for me today",
		"the config lives in cmd/serve, right?",
	} {
		provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{textTurn("answer")}}
		_, err := Run(context.Background(), prompt, provider, Options{Registry: tools.NewRegistry(), Evaluate: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(provider.requests) != 1 {
			t.Fatalf("prompt %q: expected planner skip (1 request), got %d", prompt, len(provider.requests))
		}
	}
}

// The TUI wraps follow-up prompts in session context; PlannerPrompt carries the
// raw user ask so the triviality gate still sees "do it"-sized requests.
func TestPlannerGatesOnPlannerPromptNotWrappedPrompt(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{textTurn("done")}}
	wrapped := "Resumed session context: previously we discussed building a large multi-file game with several subsystems.\n\nUser request: do it"
	_, err := Run(context.Background(), wrapped, provider, Options{
		Registry:      tools.NewRegistry(),
		Evaluate:      true,
		PlannerPrompt: "do it",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("expected planner skip via PlannerPrompt gate, got %d requests", len(provider.requests))
	}
}

// The plan seed must be folded into the LAST USER message — never appended as a
// synthetic assistant turn, which small local models echo back verbatim.
func TestPlannerSeedFoldsIntoUserMessage(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("1. Step one\n   - Do: write_file the first deliverable\n   - Done when: it exists\n2. Step two\n   - Do: edit_file the second deliverable\n   - Done when: it is correct\n3. Verify\n   - Do: run the focused check\n   - Done when: it passes"),
		textTurn("done"),
	}}
	_, err := Run(context.Background(), "please update the server config port and restart it", provider, Options{
		Registry: tools.NewRegistry(),
		Evaluate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	generatorReq := provider.requests[1]
	last := generatorReq.Messages[len(generatorReq.Messages)-1]
	if last.Role != sonutruntime.MessageRoleUser {
		t.Fatalf("generator conversation must end with the user message, got role %q", last.Role)
	}
	if !strings.Contains(last.Content, "Step one") || !strings.Contains(last.Content, "restart it") {
		t.Fatalf("plan not folded into user message: %q", last.Content)
	}
}

// OnPlan must surface the planner's plan so the TUI can render it — the plan
// rides inside the user message, so without the callback it is invisible.
func TestPlannerOnPlanCallbackFires(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("1. Step one\n   - Do: write_file the first deliverable\n   - Done when: it exists\n2. Step two\n   - Do: edit_file the second deliverable\n   - Done when: it is correct\n3. Verify\n   - Do: run the focused check\n   - Done when: it passes"),
		textTurn("done"),
	}}
	var got string
	_, err := Run(context.Background(), "please update the server config port and restart it", provider, Options{
		Registry: tools.NewRegistry(),
		Evaluate: true,
		OnPlan:   func(plan string) { got = plan },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Step one") {
		t.Fatalf("OnPlan should receive the plan, got %q", got)
	}
}

// Planner messages[0] must match the main system prompt for KV cache reuse,
// and the planning instruction must be in the last user message.
func TestPlannerUsesMainSystemPromptForKVCacheReuse(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("1. First step\n   - Do: write_file the first deliverable\n   - Done when: it exists\n2. Second step\n   - Do: edit_file the second deliverable\n   - Done when: it is correct\n3. Verify\n   - Do: run the focused check\n   - Done when: it passes"),
		textTurn("Done"),
	}}

	// Use a longer prompt to ensure it passes the triviality check (> 8 words)
	longPrompt := "please refactor the payment module to support multiple currencies and add comprehensive error handling"
	_, err := Run(context.Background(), longPrompt, provider, Options{
		Registry: tools.NewRegistry(),
		Evaluate: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	plannerReq := provider.requests[0]
	if len(plannerReq.Messages) < 2 {
		t.Fatalf("expected at least 2 messages in planner request, got %d", len(plannerReq.Messages))
	}

	mainSystemPrompt := BuildSystemPrompt(Options{})
	if plannerReq.Messages[0].Content != mainSystemPrompt {
		t.Fatalf("planner message[0] should match main system prompt for KV cache reuse\nExpected: %q\nGot: %q",
			mainSystemPrompt, plannerReq.Messages[0].Content)
	}

	// Planner instruction should be in the last user message
	userMsg := plannerReq.Messages[len(plannerReq.Messages)-1]
	if userMsg.Role != sonutruntime.MessageRoleUser {
		t.Fatalf("last message should be user role, got %s", userMsg.Role)
	}
	if !strings.Contains(userMsg.Content, "Before executing anything") {
		t.Fatalf("planner instruction not found in user message.\nFull content: %q", userMsg.Content)
	}
	if !strings.Contains(userMsg.Content, "payment module") {
		t.Fatalf("original request not found in user message: %q", userMsg.Content)
	}
}

func someMessageContains(messages []sonutruntime.Message, needle string) bool {
	for _, m := range messages {
		if strings.Contains(m.Content, needle) {
			return true
		}
	}
	return false
}

type plannerValidationBrowserTool struct{}

func (plannerValidationBrowserTool) Name() string             { return "mcp_playwright_browser_snapshot" }
func (plannerValidationBrowserTool) Description() string      { return "Inspect the visible browser page." }
func (plannerValidationBrowserTool) Parameters() tools.Schema { return tools.Schema{Type: "object"} }
func (plannerValidationBrowserTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectNetwork, Permission: tools.PermissionPrompt}
}
func (plannerValidationBrowserTool) Run(context.Context, map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusOK, Output: "snapshot"}
}

type plannerValidationDeniedBrowserTool struct{}

func (plannerValidationDeniedBrowserTool) Name() string        { return "mcp_playwright_browser_click_denied" }
func (plannerValidationDeniedBrowserTool) Description() string { return "Denied browser action." }
func (plannerValidationDeniedBrowserTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object"}
}
func (plannerValidationDeniedBrowserTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectNetwork, Permission: tools.PermissionDeny}
}
func (plannerValidationDeniedBrowserTool) Run(context.Context, map[string]any) tools.Result {
	return tools.Result{Output: "denied"}
}

type plannerValidationWebBrowserTool struct{}

func (plannerValidationWebBrowserTool) Name() string        { return "web_browser" }
func (plannerValidationWebBrowserTool) Description() string { return "Control a visible browser." }
func (plannerValidationWebBrowserTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object", AdditionalProperties: true}
}
func (plannerValidationWebBrowserTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectLocalBrowser, Permission: tools.PermissionPrompt}
}
func (plannerValidationWebBrowserTool) Run(_ context.Context, args map[string]any) tools.Result {
	result := tools.Result{Status: tools.StatusOK, Output: "ok"}
	switch args["action"] {
	case "screenshot":
		result.Images = []sonutruntime.ImageBlock{{MediaType: "image/png", Data: []byte{1, 2, 3}}}
		result.Meta = map[string]string{"screenshot_attached": "true"}
	case "console_logs":
		result.Output = "no console messages"
	}
	return result
}

func containsRuntimeTool(definitions []sonutruntime.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}
