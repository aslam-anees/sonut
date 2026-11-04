package sonutruntime

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A model that deliberates past the reasoning byte budget without producing
// text or a tool call must be abandoned as a stall, not drained forever.
func TestCollectAbandonsRunawayReasoning(t *testing.T) {
	events := make(chan StreamEvent)
	go func() {
		defer close(events)
		for i := 0; i < 100; i++ {
			events <- StreamEvent{Type: StreamEventReasoning, Content: strings.Repeat("x", 1024)}
		}
	}()
	collected := CollectStreamWithOptions(context.Background(), events, CollectOptions{
		MaxReasoningBytes: 8 * 1024,
	})
	if !strings.Contains(collected.Error, "reasoning exceeded") {
		t.Fatalf("expected reasoning budget error, got %q", collected.Error)
	}
}

// Once real output has started, long reasoning must NOT abort the turn.
func TestCollectReasoningBudgetIgnoredAfterOutputStarts(t *testing.T) {
	events := make(chan StreamEvent)
	go func() {
		defer close(events)
		events <- StreamEvent{Type: StreamEventText, Content: "answer"}
		for i := 0; i < 20; i++ {
			events <- StreamEvent{Type: StreamEventReasoning, Content: strings.Repeat("x", 1024)}
		}
		events <- StreamEvent{Type: StreamEventDone}
	}()
	collected := CollectStreamWithOptions(context.Background(), events, CollectOptions{
		MaxReasoningBytes:    4 * 1024,
		MaxReasoningDuration: time.Hour,
	})
	if collected.Error != "" {
		t.Fatalf("expected no error after output started, got %q", collected.Error)
	}
	if collected.Text != "answer" {
		t.Fatalf("expected text preserved, got %q", collected.Text)
	}
}

// Regression for the "plan never set" bug: a planning-scale think (~4 KB,
// which the old 2560-byte / 20 s default would have abandoned as a stall)
// followed by the actual plan text must complete cleanly under the DEFAULT
// budget — the planner runs with bare defaults, so this is what governs it.
func TestCollectDefaultBudgetToleratesPlanningThink(t *testing.T) {
	events := make(chan StreamEvent)
	go func() {
		defer close(events)
		// ~4 KB of legitimate deliberation before acting.
		for i := 0; i < 4; i++ {
			events <- StreamEvent{Type: StreamEventReasoning, Content: strings.Repeat("x", 1024)}
		}
		events <- StreamEvent{Type: StreamEventText, Content: "1. do the thing"}
		events <- StreamEvent{Type: StreamEventDone}
	}()
	collected := CollectStreamWithOptions(context.Background(), events, CollectOptions{})
	if collected.Error != "" {
		t.Fatalf("planning-scale think must not stall under the default budget, got %q", collected.Error)
	}
	if collected.Text != "1. do the thing" {
		t.Fatalf("plan text = %q, want it preserved", collected.Text)
	}
}

// A think that runs past the default byte budget with no output is still a
// spiral and must be abandoned.
func TestCollectDefaultBudgetStillCatchesRunaway(t *testing.T) {
	events := make(chan StreamEvent)
	go func() {
		defer close(events)
		for i := 0; i < 100; i++ {
			events <- StreamEvent{Type: StreamEventReasoning, Content: strings.Repeat("x", 1024)}
		}
	}()
	collected := CollectStreamWithOptions(context.Background(), events, CollectOptions{})
	if !strings.Contains(collected.Error, "reasoning exceeded") {
		t.Fatalf("runaway think must still stall under the default budget, got %q", collected.Error)
	}
}

// Active thinking that keeps streaming tokens must NEVER be killed by the
// wall-clock duration budget — only true idle (no new tokens) may stall.
// Regression for: "stream stalled: model reasoning exceeded 8s without acting"
// while the model was still emitting reasoning_content.
func TestCollectActiveReasoningSurvivesDurationBudget(t *testing.T) {
	events := make(chan StreamEvent)
	go func() {
		defer close(events)
		// Stream small think chunks across > duration budget wall-clock.
		// If duration were measured from first think token, this would stall.
		for i := 0; i < 12; i++ {
			events <- StreamEvent{Type: StreamEventReasoning, Content: "think "}
			time.Sleep(25 * time.Millisecond)
		}
		events <- StreamEvent{Type: StreamEventText, Content: "ok"}
		events <- StreamEvent{Type: StreamEventDone}
	}()
	collected := CollectStreamWithOptions(context.Background(), events, CollectOptions{
		MaxReasoningBytes:    64 * 1024,
		MaxReasoningDuration: 80 * time.Millisecond,
		MaxStreamSilence:     time.Hour,
	})
	if collected.Error != "" {
		t.Fatalf("active thinking must not stall on duration, got %q", collected.Error)
	}
	if collected.Text != "ok" {
		t.Fatalf("text = %q, want ok", collected.Text)
	}
}

// Pure-reasoning that goes IDLE (no new tokens) past the duration budget
// must still abort — idle without tokens is the real failure mode.
func TestCollectIdleReasoningStallsOnDuration(t *testing.T) {
	events := make(chan StreamEvent)
	go func() {
		// Emit one think chunk then hang (no close, no more events).
		events <- StreamEvent{Type: StreamEventReasoning, Content: "planning…"}
	}()
	collected := CollectStreamWithOptions(context.Background(), events, CollectOptions{
		MaxReasoningBytes:    64 * 1024,
		MaxReasoningDuration: 80 * time.Millisecond,
		MaxStreamSilence:     time.Hour, // silence watchdog must not steal this case
	})
	if !strings.Contains(collected.Error, "reasoning exceeded") {
		t.Fatalf("idle pure-reasoning must stall on duration, got %q", collected.Error)
	}
}

// The old 512-byte flash cap false-stalled healthy short thinks (~605 chars
// measured live). Defaults and explicit none-effort-scale budgets must
// tolerate a multi-sentence think that then acts.
func TestCollectToleratesShortHealthyThink(t *testing.T) {
	events := make(chan StreamEvent)
	go func() {
		defer close(events)
		// 605 chars of pure reasoning then an answer — the live failure shape.
		events <- StreamEvent{Type: StreamEventReasoning, Content: strings.Repeat("a", 605)}
		events <- StreamEvent{Type: StreamEventText, Content: "hello"}
		events <- StreamEvent{Type: StreamEventDone}
	}()
	collected := CollectStreamWithOptions(context.Background(), events, CollectOptions{
		// Flash-scale cap from reasoningCapsForEffort("none") after the fix.
		MaxReasoningBytes:    4096,
		MaxReasoningDuration: 45 * time.Second,
	})
	if collected.Error != "" {
		t.Fatalf("605-char think must not stall, got %q", collected.Error)
	}
	if collected.Text != "hello" {
		t.Fatalf("text = %q, want hello", collected.Text)
	}
}

// The SONUT_MAX_REASONING_BYTES env override raises the default budget so a
// slower/faster rig can tune the stall threshold without recompiling.
func TestReasoningBytesEnvOverride(t *testing.T) {
	t.Setenv("SONUT_MAX_REASONING_BYTES", "65536")
	if got := resolveMaxReasoningBytes(0); got != 65536 {
		t.Fatalf("env override bytes = %d, want 65536", got)
	}
	// An explicit option still wins over the env override.
	if got := resolveMaxReasoningBytes(1024); got != 1024 {
		t.Fatalf("explicit option bytes = %d, want 1024", got)
	}
}

// A tool call that trips the argument byte cap must stop the collect
// immediately, carrying the abort reason on the call.
func TestCollectStopsDrainingOnRunawayToolArgs(t *testing.T) {
	events := make(chan StreamEvent, 8)
	events <- StreamEvent{Type: StreamEventToolCallStart, ToolCallID: "t1", ToolName: "write_file"}
	events <- StreamEvent{Type: StreamEventToolCallDelta, ToolCallID: "t1", ArgumentsFragment: strings.Repeat("a", 2048)}
	// No Done event and channel left open: if the collector fails to stop on
	// abort, the test hangs (caught by the timeout below).
	done := make(chan CollectedStream, 1)
	go func() {
		done <- CollectStreamWithOptions(context.Background(), events, CollectOptions{MaxToolArgBytes: 1024})
	}()
	select {
	case collected := <-done:
		if len(collected.ToolCalls) != 1 || !collected.ToolCalls[0].Aborted {
			t.Fatalf("expected one aborted tool call, got %+v", collected.ToolCalls)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("collector kept draining after tool-arg cap was exceeded")
	}
}
