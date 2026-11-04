package agent

import (
	"strings"
	"testing"
)

func TestCheckVerboseNarration(t *testing.T) {
	state := newGuardState()
	long := strings.Repeat("I should verify the current state of the file first. ", 20)

	if got := state.checkVerboseNarration(long, 0); got != "" {
		t.Fatalf("no tool calls should not fire, got %q", got)
	}
	if got := state.checkVerboseNarration("short preamble", 1); got != "" {
		t.Fatalf("short text should not fire, got %q", got)
	}
	if got := state.checkVerboseNarration(long, 1); !strings.Contains(got, verboseNarrationMarker) {
		t.Fatalf("first long turn should fire, got %q", got)
	}
	if got := state.checkVerboseNarration(long, 1); got == "" {
		t.Fatalf("second long turn should still fire (budget is %d)", maxVerboseNarrationNudges)
	}
	if got := state.checkVerboseNarration(long, 1); got != "" {
		t.Fatalf("clamp should be exhausted after %d nudges, got %q", maxVerboseNarrationNudges, got)
	}
}
