package agent

import (
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
)

func TestExtractNumberedPlanKeepsDoDoneDetail(t *testing.T) {
	raw := `Some preamble I should ignore.

1. Create mariogame.html scaffold with canvas and game loop
   - Do: single-file HTML, CSS layout, requestAnimationFrame, draw ground+player
   - Done when: file opens and the canvas paints a player and ground
2. Implement player physics (gravity, jump, left/right)
   - Do: vy gravity, space jump when onGround, arrow/WASD movement, platform collision
   - Done when: player can jump and land on platforms without falling through
3. Verify in headful browser
   - Do: open the HTML and confirm keys move the player
   - Done when: screenshot or console shows no errors

Trailing prose must be dropped.`

	plan := extractNumberedPlan(raw)
	if plan == "" {
		t.Fatal("expected multi-line numbered plan")
	}
	if !strings.Contains(plan, "Do: single-file HTML") {
		t.Fatalf("Do detail dropped: %q", plan)
	}
	if !strings.Contains(plan, "Done when: player can jump") {
		t.Fatalf("Done when detail dropped: %q", plan)
	}
	if strings.Contains(plan, "Trailing prose") {
		t.Fatalf("trailing prose leaked into plan: %q", plan)
	}
	if strings.Contains(plan, "Some preamble") {
		t.Fatalf("preamble leaked into plan: %q", plan)
	}
}

func TestFirstPlanStepTitle(t *testing.T) {
	plan := "1. Create mariogame.html scaffold\n   - Do: canvas\n2. Add physics"
	got := firstPlanStepTitle(plan)
	if got != "Create mariogame.html scaffold" {
		t.Fatalf("firstPlanStepTitle = %q", got)
	}
}

func TestInjectPlanSeedRequiresRealSourceAndExecuteNow(t *testing.T) {
	msgs := []Message{
		{Role: sonutruntime.MessageRoleSystem, Content: "sys"},
		{Role: sonutruntime.MessageRoleUser, Content: "build a mario game"},
	}
	plan := "1. Create mariogame.html with canvas\n   - Do: game loop\n   - Done when: paints\n2. Add physics\n   - Do: gravity\n   - Done when: jumps"
	out := injectPlanSeed(msgs, plan)
	body := out[1].Content
	if !strings.Contains(body, "EXECUTE NOW") {
		t.Fatalf("missing EXECUTE NOW directive:\n%s", body)
	}
	if !strings.Contains(body, "Keep private reasoning private") {
		t.Fatalf("executor seed must keep reasoning private:\n%s", body)
	}
	if !strings.Contains(body, "Create mariogame.html") {
		t.Fatalf("missing first step in execute-now:\n%s", body)
	}
	if !strings.Contains(body, "real arguments") {
		t.Fatalf("missing real-arguments guard:\n%s", body)
	}
	if !strings.Contains(body, "Read different files") && !strings.Contains(body, "distinct paths") &&
		!strings.Contains(body, "never re-read the same path") {
		t.Fatalf("executor seed must allow reading different files (not same-path thrash):\n%s", body)
	}
	if !strings.Contains(body, "Do: game loop") {
		t.Fatalf("full plan Do detail missing from seed:\n%s", body)
	}
	// Debloat: seed must stay short so local models do not echo a wall of rules.
	if len(body) > 2500 {
		t.Fatalf("seed too long (%d bytes) — keep builder_workflow tight", len(body))
	}
}

func TestDedupePlanStepsDropsRepeatedTitles(t *testing.T) {
	dup := `1. Implement Title Screen
   - Do: write title
   - Done when: title shows
2. Implement Player Character
   - Do: draw player
   - Done when: player moves
3. Implement Title Screen
   - Do: write title again
   - Done when: title shows
4. Implement Player Character
   - Do: draw player again
   - Done when: player moves`
	got := sanitizePlan(dup)
	if strings.Count(got, "Implement Title Screen") != 1 {
		t.Fatalf("title screen must appear once:\n%s", got)
	}
	if strings.Count(got, "Implement Player Character") != 1 {
		t.Fatalf("player character must appear once:\n%s", got)
	}
	if !strings.Contains(got, "1. Implement Title Screen") || !strings.Contains(got, "2. Implement Player Character") {
		t.Fatalf("expected renumbered unique steps:\n%s", got)
	}
	if strings.Contains(got, "3.") {
		t.Fatalf("duplicate steps must be dropped, got:\n%s", got)
	}
}

func TestDedupePlanStepsPreservesStatusAnnotations(t *testing.T) {
	// Validation PASS roadmaps must keep [completed] so the sticky plan does
	// not re-open step 1 as in_progress after sanitize/dedupe.
	in := "1. [completed] Implement the feature\n2. [completed] Verify index.html"
	got := sanitizePlan(in)
	if !strings.Contains(got, "[completed] Implement the feature") {
		t.Fatalf("status stripped from step 1:\n%s", got)
	}
	if !strings.Contains(got, "[completed] Verify index.html") {
		t.Fatalf("status stripped from step 2:\n%s", got)
	}
}

func TestFallbackPlanHasDoDone(t *testing.T) {
	plan := fallbackPlan("build something", false)
	if !strings.Contains(plan, "Do:") || !strings.Contains(plan, "Done when:") {
		t.Fatalf("fallback plan missing Do/Done detail:\n%s", plan)
	}
	withImg := fallbackPlan("build from screenshot", true)
	if !strings.Contains(withImg, "visual") && !strings.Contains(withImg, "image") {
		t.Fatalf("image fallback should mention visual/image grounding:\n%s", withImg)
	}
}

func TestPlannerInstructionsAskForDeepSteps(t *testing.T) {
	if !strings.Contains(plannerInstructions, "Do:") || !strings.Contains(plannerInstructions, "Done when:") {
		t.Fatal("plannerInstructions must require Do/Done detail lines")
	}
	if strings.Contains(plannerInstructions, "under ~25 words") {
		t.Fatal("shallow one-line-only instruction must be removed")
	}
	if !strings.Contains(plannerInstructions, "web_search") {
		t.Fatal("planner must instruct web_search steps when current facts matter")
	}
	if !strings.Contains(plannerInstructions, "zero thinking") {
		t.Fatal("planner must tell the model the executor has zero thinking")
	}
	if !strings.Contains(plannerInstructions, "write_file") {
		t.Fatal("planner should name concrete tools in Do guidance")
	}
}

func TestPlanLooksExecutorReady(t *testing.T) {
	ready := `1. Read game.js
   - Do: read_file game.js for current systems
   - Done when: player/physics entry points known
2. Restyle title
   - Do: edit_file game.js — title font Orbitron-like, color #7CFF6B
   - Done when: title matches reference palette
3. Verify
   - Do: open in browser
   - Done when: no console errors`
	if !planLooksExecutorReady(ready) {
		t.Fatal("expected multi-step Do/Done plan to look executor-ready")
	}
	if planLooksExecutorReady("1. Do stuff\n2. More stuff") {
		t.Fatal("title-only plan must not look executor-ready")
	}
}

func TestEmptyAppendFileNotice(t *testing.T) {
	empty := emptyAppendFileNotice(ToolCall{Name: "append_file", Arguments: `{"path":"x.html","content":""}`})
	if empty == "" {
		t.Fatal("expected empty append notice")
	}
	full := emptyAppendFileNotice(ToolCall{Name: "append_file", Arguments: `{"path":"x.html","content":"function foo(){}"}`})
	if full != "" {
		t.Fatalf("non-empty append should pass: %q", full)
	}
}

func TestFirstPendingPlanStep(t *testing.T) {
	g := newGuardState()
	g.seedPlannerPlan("1. Create file\n2. Add physics\n3. Verify")
	// seed marks first as in_progress
	got := g.firstPendingPlanStep()
	if got != "Create file" {
		t.Fatalf("firstPending = %q, want Create file", got)
	}
}
