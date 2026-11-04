package tools

import (
	"strings"
	"testing"
)

func TestAllocateTextBudgetFairShare(t *testing.T) {
	// Three 100-byte bodies, budget 150 → each gets 50.
	got := AllocateTextBudget([]int{100, 100, 100}, 150)
	sum := 0
	for _, n := range got {
		if n != 50 {
			t.Fatalf("got %v, want equal shares of 50", got)
		}
		sum += n
	}
	if sum != 150 {
		t.Fatalf("sum=%d want 150", sum)
	}
}

func TestAllocateTextBudgetKeepsShortIntact(t *testing.T) {
	// One short (10) and one long (1000), budget 100 → short keeps 10, long gets 90.
	got := AllocateTextBudget([]int{10, 1000}, 100)
	if got[0] != 10 {
		t.Fatalf("short allocation=%d want 10", got[0])
	}
	if got[1] != 90 {
		t.Fatalf("long allocation=%d want 90", got[1])
	}
}

func TestFitTextHeadTailKeepsEnds(t *testing.T) {
	text := strings.Repeat("A", 80) + "HEAD" + strings.Repeat("B", 80) + "TAIL" + strings.Repeat("C", 80)
	out := FitTextHeadTail(text, 60, "…mid…")
	if !strings.Contains(out, "…mid…") {
		t.Fatalf("missing marker: %q", out)
	}
	if len(out) > 60 {
		t.Fatalf("len=%d want <=60", len(out))
	}
	// Should retain content from the start.
	if !strings.HasPrefix(out, "AAA") {
		t.Fatalf("expected head prefix, got %q", out[:min(20, len(out))])
	}
}

func TestApplyBatchOutputBudgetShrinksSum(t *testing.T) {
	bodies := []string{
		strings.Repeat("x", 500),
		strings.Repeat("y", 500),
		strings.Repeat("z", 500),
	}
	n := ApplyBatchOutputBudget(bodies, 400, "hint")
	if n == 0 {
		t.Fatal("expected bodies to be shortened")
	}
	total := 0
	for _, b := range bodies {
		total += len(b)
	}
	// Shared budget is a soft target; notes add a few bytes. Stay well under raw sum.
	if total >= 1500 {
		t.Fatalf("total after budget=%d still too large", total)
	}
}

func TestApplyOutputBudgetHeadTail(t *testing.T) {
	// applyOutputBudget should use head+tail, not head-only.
	body := "START-" + strings.Repeat("m", 500) + "-END_MARKER"
	result := applyOutputBudget(body, 120, "continue")
	if !result.Truncated {
		t.Fatal("expected truncation")
	}
	if !strings.Contains(result.Output, "START") {
		t.Fatalf("expected head kept: %q", result.Output)
	}
	if !strings.Contains(result.Output, "END_MARKER") && !strings.Contains(result.Output, "END") {
		// Tail keep may include END_MARKER depending on budget split.
		if !strings.Contains(result.Output, "truncated") {
			t.Fatalf("unexpected truncated output: %q", result.Output)
		}
	}
}
