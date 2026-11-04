package agent

import (
	"testing"
	"time"
)

// Flash/executor caps must sit ABOVE the server --reasoning-budget (512 tokens
// ≈ 2–2.5 KB). The old 512-byte / 8s client caps false-stalled healthy short
// thinks (live: reasoning_chars=605 → "stream stalled: … 512 bytes") and
// burned multi-minute idle on stall-retry.
func TestReasoningCapsNeverUseTinyFlashBudget(t *testing.T) {
	for _, effort := range []string{"none", "minimal", "low", "medium", "high", "auto", ""} {
		bytes, dur := reasoningCapsForEffort(effort)
		if bytes < 4096 {
			t.Errorf("reasoningCapsForEffort(%q) bytes=%d, want >= 4096 (above server budget)", effort, bytes)
		}
		if dur < 45*time.Second {
			t.Errorf("reasoningCapsForEffort(%q) duration=%s, want >= 45s", effort, dur)
		}
	}
}

// Executor steps always use flash effort — planner owns deliberation.
func TestExecutionEffortIsFlash(t *testing.T) {
	for _, base := range []string{"high", "medium", "low", "none", "auto", ""} {
		if got := executionEffort(base); got != "none" {
			t.Fatalf("executionEffort(%q)=%q, want none", base, got)
		}
	}
}
