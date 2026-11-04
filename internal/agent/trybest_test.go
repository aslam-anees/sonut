package agent

import (
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/tools"
)

func TestNormalizeDiffAndJaccard(t *testing.T) {
	a := normalizeDiff("+ foo bar\n- old line\n")
	b := normalizeDiff("+ foo bar\n- old line\n")
	if a == "" || b == "" {
		t.Fatal("expected normalized diffs")
	}
	sim := jaccard(shingleSet(a, 3), shingleSet(b, 3))
	if sim < 0.99 {
		t.Fatalf("identical diffs should have high similarity, got %v", sim)
	}
	c := normalizeDiff("+ completely different content here\n")
	sim2 := jaccard(shingleSet(a, 3), shingleSet(normalizeDiff(c), 3))
	if sim2 > 0.5 {
		t.Fatalf("different diffs should have low similarity, got %v", sim2)
	}
}

func TestTryBestEditRepeat(t *testing.T) {
	m := NewTryBestMonitor()
	diff := "@@ -1 +1 @@\n-old\n+new line alpha beta gamma\n+new line delta epsilon zeta\n"
	// Need enough tokens for 3-shingles
	diff = "@@ -1 +1 @@\n" + strings.Repeat("+token_one token_two token_three token_four\n", 3)

	for i := 0; i < tryBestEditMatches; i++ {
		inc := m.Observe("edit_file", nil, ToolResult{
			Status:       tools.StatusOK,
			ChangedFiles: []string{"pkg/foo.go"},
			Display:      tools.Display{Preview: diff},
		})
		if inc != nil {
			t.Fatalf("incident too early at edit %d: %+v", i+1, inc)
		}
	}
	// Next near-identical edit should trip.
	inc := m.Observe("edit_file", nil, ToolResult{
		Status:       tools.StatusOK,
		ChangedFiles: []string{"pkg/foo.go"},
		Display:      tools.Display{Preview: diff},
	})
	if inc == nil || inc.Reason != TryBestEditRepeat {
		t.Fatalf("expected edit_repeat, got %+v", inc)
	}
	if !strings.Contains(inc.Nudge(), "near-identical") {
		t.Fatalf("nudge should mention near-identical: %q", inc.Nudge())
	}
}

func TestTryBestBashRetry(t *testing.T) {
	m := NewTryBestMonitor()
	args := map[string]any{"command": "go test ./pkg -count=1"}
	fail := ToolResult{Status: tools.StatusError, Output: "FAIL: boom\n"}
	for i := 0; i < tryBestBashRetries-1; i++ {
		if inc := m.Observe("bash", args, fail); inc != nil {
			t.Fatalf("too early at fail %d: %+v", i+1, inc)
		}
	}
	inc := m.Observe("bash", args, fail)
	if inc == nil || inc.Reason != TryBestBashRetry {
		t.Fatalf("expected bash_retry, got %+v", inc)
	}
	// Success resets.
	m.Reset()
	if inc := m.Observe("bash", args, ToolResult{Status: tools.StatusOK, Output: "ok"}); inc != nil {
		t.Fatalf("success should not incident: %+v", inc)
	}
}

func TestTryBestActionStreak(t *testing.T) {
	m := NewTryBestMonitor()
	// Distinct diffs so edit_repeat does not fire first.
	for i := 0; i < tryBestActionStreak-1; i++ {
		diff := "@@ -1 +1 @@\n+unique tokens set number " + itoa(i) + " alpha beta gamma delta\n"
		inc := m.Observe("write_file", nil, ToolResult{
			Status:       tools.StatusOK,
			ChangedFiles: []string{"a.go"},
			Display:      tools.Display{Preview: diff},
		})
		if inc != nil {
			t.Fatalf("early incident at %d: %+v", i+1, inc)
		}
	}
	diff := "@@ -1 +1 @@\n+unique tokens set number final alpha beta gamma delta\n"
	inc := m.Observe("write_file", nil, ToolResult{
		Status:       tools.StatusOK,
		ChangedFiles: []string{"a.go"},
		Display:      tools.Display{Preview: diff},
	})
	if inc == nil || inc.Reason != TryBestActionStreak {
		t.Fatalf("expected action_streak, got %+v", inc)
	}
}

func TestTryBestDisabled(t *testing.T) {
	t.Setenv("ATCODE_TRY_BEST", "0")
	m := NewTryBestMonitor()
	if !m.Disabled {
		t.Fatal("expected disabled")
	}
	args := map[string]any{"command": "false"}
	for i := 0; i < 5; i++ {
		if inc := m.Observe("bash", args, ToolResult{Status: tools.StatusError, Output: "err"}); inc != nil {
			t.Fatalf("disabled monitor must not fire: %+v", inc)
		}
	}
}

func TestGuardObserveTryBestArmsNudge(t *testing.T) {
	g := newGuardState()
	diff := "@@ -1 +1 @@\n" + strings.Repeat("+token_one token_two token_three token_four\n", 3)
	for i := 0; i < tryBestEditMatches+1; i++ {
		g.observeTryBest("edit_file", nil, ToolResult{
			Status:       tools.StatusOK,
			ChangedFiles: []string{"x.go"},
			Display:      tools.Display{Preview: diff},
		})
	}
	nudge := g.takeRepeatNudge()
	if nudge == "" || !strings.Contains(nudge, "Try-best") {
		t.Fatalf("expected try-best nudge, got %q", nudge)
	}
}
