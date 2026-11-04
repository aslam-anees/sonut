package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

// recordingTool is a trivially-succeeding tool so the run counts real work
// (the evaluator gate only fires when totalToolCalls > 0).
type recordingTool struct{ calls int }

func (t *recordingTool) Name() string        { return "touch_tool" }
func (t *recordingTool) Description() string { return "test tool" }
func (t *recordingTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectNone, Permission: tools.PermissionAllow, Reason: "test"}
}
func (t *recordingTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object"}
}
func (t *recordingTool) Run(ctx context.Context, args map[string]any) tools.Result {
	t.calls++
	return tools.Result{Output: "ok"}
}

// Planner validation rejects once with concrete issues, the loop bounces them
// back to the worker, and the second verdict passes — the run must finalize
// with the post-fix answer and the rejection must appear in the conversation.
func TestEvaluatorGateBouncesRejectionThenAccepts(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(&recordingTool{})

	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		toolTurn("c1", "touch_tool", `{}`),   // worker does work
		textTurn("Done, the feature works."), // worker claims done
		textTurn("VERDICT: REPLAN\nISSUES:\n1. index.html was never verified — no read-back or build ran.\nPLAN:\n1. [completed] Implement the feature\n2. [pending] Verify index.html"),
		toolTurn("c2", "touch_tool", `{"phase":"verify"}`), // executor performs fresh rework; it may not bounce straight back
		textTurn("Fixed and verified now."),
		textTurn("VERDICT: PASS\nPLAN:\n1. [completed] Implement the feature\n2. [completed] Verify index.html"),
	}}

	var verdicts []bool
	var validatedPlan []tools.PlanItem
	result, err := Run(context.Background(), "add the feature", provider, Options{
		Registry: registry,
		MaxTurns: 10,
		Evaluate: true,
		OnPlanProgress: func(items []tools.PlanItem) {
			validatedPlan = append([]tools.PlanItem(nil), items...)
		},
		OnEvaluator: func(passed bool, feedback string) {
			verdicts = append(verdicts, passed)
			if !passed && !strings.Contains(feedback, "index.html") {
				t.Fatalf("rejection feedback lost the concrete issue: %q", feedback)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "Fixed and verified now." {
		t.Fatalf("expected the post-fix answer to be final, got %q", result.FinalAnswer)
	}
	if len(verdicts) != 2 || verdicts[0] || !verdicts[1] {
		t.Fatalf("expected verdicts [false true], got %v", verdicts)
	}
	if len(validatedPlan) != 2 || validatedPlan[0].Status != "completed" || validatedPlan[1].Status != "completed" {
		t.Fatalf("planner validation did not authoritatively complete the task roadmap: %#v", validatedPlan)
	}
	if !someRequestContains(provider.requests, "Planner validation found") {
		t.Fatal("expected planner validation rejection to be bounced into the worker conversation")
	}
	if !someRequestContains(provider.requests, "CORRECTED PLAN (planner-owned)") {
		t.Fatal("expected the planner's corrected roadmap to replace the stale executor seed")
	}
	for _, req := range provider.requests {
		if someMessageContains(req.Messages, "CORRECTED PLAN (planner-owned)") && req.DisableThinking {
			t.Fatal("executor rework after planner validation must keep concise private thinking")
		}
	}
	// The planner-validation request uses the main system prompt for cache reuse
	// and keeps thinking enabled for the planner role. This non-UI fixture has no
	// browser tools, so the planner capability set is empty.
	found := false
	for _, req := range provider.requests {
		if len(req.Messages) == 2 && len(req.Tools) == 0 && req.ReasoningEffort == "high" && !req.DisableThinking {
			if strings.Contains(req.Messages[1].Content, "planning agent returning to validate") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatal("no planner-validation request with instructions in user message was made")
	}
}

// A run with zero tool calls (plain Q&A) must never trigger planner validation.
func TestEvaluatorGateSkipsToollessRuns(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("The answer is 42."),
	}}
	_, err := Run(context.Background(), "what is the answer", provider, Options{
		Registry: tools.NewRegistry(),
		Evaluate: true,
		OnEvaluator: func(bool, string) {
			t.Fatal("planner validation must not run for a tool-less Q&A turn")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("expected exactly 1 provider request, got %d", len(provider.requests))
	}
}

// After bounded planner-validation rejections the run must still terminate (the planner
// can never trap the loop), and an unparseable verdict must fail open.
func TestEvaluatorVerdictParsing(t *testing.T) {
	if passed, _, _ := parseEvaluatorVerdict("VERDICT: PASS"); !passed {
		t.Fatal("PASS verdict must pass")
	}
	if passed, fb, _ := parseEvaluatorVerdict("VERDICT: FAIL\n1. broken"); passed || fb != "1. broken" {
		t.Fatalf("FAIL verdict must fail with issues, got passed=%v fb=%q", passed, fb)
	}
	if passed, _, _ := parseEvaluatorVerdict("hmm not sure"); !passed {
		t.Fatal("unparseable verdict must fail open")
	}
	if passed, _, _ := parseEvaluatorVerdict("VERDICT: FAIL"); !passed {
		t.Fatal("FAIL with no issues must fail open (nothing actionable)")
	}
}

// Planner turn runs at maximum thinking when effort came from the auto
// classifier; execution turns drop to low; explicit user efforts are honored.
func TestPlanningEffort(t *testing.T) {
	if got := planningEffort("low", true); got != "high" {
		t.Fatalf("auto low plan turn must upgrade to high, got %q", got)
	}
	if got := planningEffort("low", false); got != "low" {
		t.Fatalf("explicit low must be honored, got %q", got)
	}
	if got := planningEffort("none", true); got != "none" {
		t.Fatalf("trivial turns must not upgrade, got %q", got)
	}
	if got := planningEffort("high", true); got != "high" {
		t.Fatalf("high stays high, got %q", got)
	}
}
