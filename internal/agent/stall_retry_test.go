package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

// stallProvider connects successfully (HTTP 200) but the stream emits a stall/idle
// timeout error for the first stallBefore calls, then succeeds with "done". The
// pre-stall emission simulates different partial-output shapes:
//   - partialText: forwarded PROSE (StreamEventText) — must NOT be retried
//     (re-issuing would duplicate visible answer text).
//   - reasoningText: transient reasoning preview — IS retried.
//   - partialToolCall: a tool call started (+ arg delta) but never ended, the
//     "froze mid-write_file" case — IS retried (the incomplete call is never
//     executed or committed).
//   - stallError: override the default no-output stall message (e.g. reasoning
//     exceeded) so recovery wording can be asserted.
type stallProvider struct {
	calls           int32
	stallBefore     int32
	partialText     string
	reasoningText   string
	partialToolCall string
	stallError      string
	requests        []sonutruntime.CompletionRequest
}

func (p *stallProvider) StreamCompletion(_ context.Context, req sonutruntime.CompletionRequest) (<-chan sonutruntime.StreamEvent, error) {
	p.requests = append(p.requests, req)
	n := atomic.AddInt32(&p.calls, 1)
	ch := make(chan sonutruntime.StreamEvent, 5)
	if n <= p.stallBefore {
		if p.partialText != "" {
			ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: p.partialText}
		}
		if p.reasoningText != "" {
			ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventReasoning, Content: p.reasoningText}
		}
		if p.partialToolCall != "" {
			// A tool call that starts and streams a partial argument fragment but
			// never gets StreamEventToolCallEnd, then the stream errors — exactly
			// the gpt-5.x "began the big write_file then froze" shape.
			ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "tc_1", ToolName: p.partialToolCall}
			ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "tc_1", ArgumentsFragment: `{"path":"x.html","content":"<!doctype`}
		}
		errMsg := p.stallError
		if errMsg == "" {
			errMsg = "provider stream error: no output for 6m (the model produced nothing)"
		}
		ch <- sonutruntime.StreamEvent{
			Type:  sonutruntime.StreamEventError,
			Error: errMsg,
		}
		close(ch)
		return ch, nil
	}
	ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: "done"}
	ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}
	close(ch)
	return ch, nil
}

// A no-output stall is re-issued on a fresh connection and recovers — this is the
// macOS stale-pooled-connection hang turned into an automatic recovery.
func TestRunRetriesStreamStallWithNoOutput(t *testing.T) {
	p := &stallProvider{stallBefore: 1}
	result, err := Run(context.Background(), "go", p, Options{Registry: tools.NewRegistry()})
	if err != nil {
		t.Fatalf("a no-output stall should retry to success, got %v", err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("final answer = %q, want %q", result.FinalAnswer, "done")
	}
	if got := atomic.LoadInt32(&p.calls); got != 2 {
		t.Fatalf("want 2 calls (1 stall + 1 retry), got %d", got)
	}
	// Recovery keeps thinking ON (quality + hybrid cache) and injects an
	// act-now nudge so the retry acts instead of silent re-overthink.
	if len(p.requests) < 2 {
		t.Fatalf("want at least 2 requests, got %d", len(p.requests))
	}
	retry := p.requests[1]
	if retry.ReasoningEffort != "low" {
		t.Fatalf("stall retry ReasoningEffort = %q, want low", retry.ReasoningEffort)
	}
	if retry.DisableThinking {
		t.Fatal("stall retry must keep DisableThinking=false (thinking on by default)")
	}
	foundNudge := false
	for _, m := range retry.Messages {
		if strings.Contains(m.Content, "produced no usable output") {
			foundNudge = true
			break
		}
	}
	if !foundNudge {
		t.Fatal("stall retry must carry the act-now nudge message")
	}
}

// A reasoning-budget stall recovers with thinking still ON and a specific
// "spent too long thinking" nudge (not the generic no-output wording).
func TestRunRetriesReasoningExceededStallKeepsThinking(t *testing.T) {
	p := &stallProvider{
		stallBefore: 1,
		stallError:  "stream stalled: model reasoning exceeded 1m0s without acting",
	}
	result, err := Run(context.Background(), "go", p, Options{Registry: tools.NewRegistry()})
	if err != nil {
		t.Fatalf("reasoning-exceeded stall should retry to success, got %v", err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("final answer = %q, want %q", result.FinalAnswer, "done")
	}
	if len(p.requests) < 2 {
		t.Fatalf("want at least 2 requests, got %d", len(p.requests))
	}
	retry := p.requests[1]
	if retry.DisableThinking || retry.ReasoningEffort != "low" {
		t.Fatalf("retry must keep thinking on (DisableThinking=%v effort=%q)", retry.DisableThinking, retry.ReasoningEffort)
	}
	found := false
	for _, m := range retry.Messages {
		if strings.Contains(m.Content, "spent too long thinking") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("reasoning-exceeded retry must use the thinking-specific nudge")
	}
}

// A stall AFTER partial output must NOT be retried — re-issuing would duplicate
// the already-streamed text. It surfaces the error instead.
func TestRunDoesNotRetryStallAfterPartialOutput(t *testing.T) {
	p := &stallProvider{stallBefore: 1, partialText: "partial"}
	_, err := Run(context.Background(), "go", p, Options{Registry: tools.NewRegistry()})
	if err == nil {
		t.Fatal("a stall after partial output must NOT be retried; want an error")
	}
	if got := atomic.LoadInt32(&p.calls); got != 1 {
		t.Fatalf("partial-then-stall must not retry, got %d calls", got)
	}
}

// A stall after only transient REASONING was forwarded (no prose text, no
// completed tool call) IS retried: reasoning is a "thinking" preview, not
// committed answer text, so re-streaming it on a clearly-signalled retry is
// acceptable and recovers the common "reasoned then froze" stall. Only
// forwarded final prose (OnText) blocks the retry (collected.Text is empty
// here, so this exercises the transient-vs-prose gate specifically).
func TestRunRetriesStallAfterOnlyReasoning(t *testing.T) {
	p := &stallProvider{stallBefore: 1, reasoningText: "thinking hard"}
	result, err := Run(context.Background(), "go", p, Options{
		Registry:    tools.NewRegistry(),
		OnReasoning: func(string) {},
	})
	if err != nil {
		t.Fatalf("a stall after only reasoning should retry to success, got %v", err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("final answer = %q, want %q", result.FinalAnswer, "done")
	}
	if got := atomic.LoadInt32(&p.calls); got != 2 {
		t.Fatalf("want 2 calls (1 stall + 1 retry), got %d", got)
	}
}

// A stall mid-way through an INCOMPLETE tool call (started + partial args, never
// ended) IS retried — the exact gpt-5.x / ollama "began the big write_file then
// froze" case. The incomplete call is never executed (a turn returns on the
// stall error before dispatch) and never committed to history, so re-issuing is
// safe and recovers to success. This is the primary scenario this change fixes.
func TestRunRetriesStallAfterIncompleteToolCall(t *testing.T) {
	starts := 0
	p := &stallProvider{stallBefore: 1, reasoningText: "planning", partialToolCall: "write_file"}
	result, err := Run(context.Background(), "go", p, Options{
		Registry:        tools.NewRegistry(),
		OnReasoning:     func(string) {},
		OnToolCallStart: func(string, string) { starts++ },
		OnToolCallDelta: func(string, string) {},
	})
	if err != nil {
		t.Fatalf("a stall mid-incomplete-tool-call should retry to success, got %v", err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("final answer = %q, want %q", result.FinalAnswer, "done")
	}
	if got := atomic.LoadInt32(&p.calls); got != 2 {
		t.Fatalf("want 2 calls (1 stall + 1 retry), got %d", got)
	}
	if starts != 1 {
		t.Fatalf("the incomplete tool call should have been forwarded once before the stall, got %d starts", starts)
	}
}

// A persistent stall surfaces an error after exhausting the capped retries.
func TestRunGivesUpAfterMaxStallRetries(t *testing.T) {
	p := &stallProvider{stallBefore: 99}
	_, err := Run(context.Background(), "go", p, Options{Registry: tools.NewRegistry()})
	if err == nil {
		t.Fatal("a persistent stall must surface an error after exhausting retries")
	}
	if got := atomic.LoadInt32(&p.calls); got != int32(1+maxStreamStallRetries) {
		t.Fatalf("want %d calls (1 + %d retries), got %d", 1+maxStreamStallRetries, maxStreamStallRetries, got)
	}
}

func TestIsStreamTimeoutError(t *testing.T) {
	timeouts := []string{
		"provider stream error: no output for 10m (the model produced nothing)",
		"provider stream error: idle timeout after 5m0s (upstream stopped sending data)",
		"stream stalled (upstream kept the connection alive but produced no output)",
	}
	for _, m := range timeouts {
		if !isStreamTimeoutError(m) {
			t.Fatalf("want timeout-classified: %q", m)
		}
	}
	notTimeouts := []string{"", "context length exceeded", "rate limit error: slow down", "model not found"}
	for _, m := range notTimeouts {
		if isStreamTimeoutError(m) {
			t.Fatalf("must NOT classify as timeout: %q", m)
		}
	}
}
