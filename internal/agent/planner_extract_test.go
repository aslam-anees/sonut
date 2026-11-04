package agent

import (
	"strings"
	"testing"
)

func TestExtractNumberedPlan(t *testing.T) {
	// Reasoning dump with a plan buried after prose.
	reasoning := `I should break this into steps.
1. Create index.html with canvas
2. Add keyboard input handlers
3. Implement collision detection
Then I'll execute.`
	got := extractNumberedPlan(reasoning)
	want := "1. Create index.html with canvas\n2. Add keyboard input handlers\n3. Implement collision detection"
	if got != want {
		t.Fatalf("extractNumberedPlan =\n%q\nwant\n%q", got, want)
	}
}

func TestExtractNumberedPlanRejectsSingleLine(t *testing.T) {
	if got := extractNumberedPlan("1. only one step is not a plan"); got != "" {
		t.Fatalf("single numbered line should be rejected, got %q", got)
	}
}

func TestResolvePlanTextPrefersContentThenReasoning(t *testing.T) {
	content := "1. Read config\n2. Update port"
	if got := resolvePlanText(content, "ignored"); got != content {
		t.Fatalf("content plan should win: %q", got)
	}
	// Content empty — harvest from reasoning (the Bonsai failure mode).
	reasoning := "Let me plan carefully.\n1. Scaffold the app\n2. Wire events\n3. Verify in browser"
	got := resolvePlanText("", reasoning)
	for _, want := range []string{"Scaffold the app", "Wire events", "Verify in browser"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected numbered plan from reasoning to contain %q, got %q", want, got)
		}
	}
}
