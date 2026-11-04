package llama

import (
	"context"
	"strings"
	"testing"

	sonutruntime "github.com/aistatine/sonut/internal/sonutruntime"
)

// fake SSE stream: reasoning_content-only deltas past the overthink limit must
// abort with a "stream stalled" error. Thinking stays ON on the agent retry
// (quality + hybrid cache) — the guard must NOT arm disableThinkingOnce.
func TestOverthinkGuardAbortsReasoningOnlyStream(t *testing.T) {
	var sse strings.Builder
	// overthinkReasoningLimit is 24 KB; emit ~30 KB of pure reasoning.
	chunk := `data: {"choices":[{"delta":{"reasoning_content":"` + strings.Repeat("think ", 100) + `"}}]}` + "\n\n"
	for i := 0; i < 50; i++ { // 50 * ~600 chars ≈ 30 KB > 24 KB limit
		sse.WriteString(chunk)
	}
	sse.WriteString("data: [DONE]\n\n")

	p := &Provider{}
	ch := make(chan sonutruntime.StreamEvent, 256)
	go func() {
		defer close(ch)
		p.readSSE(context.Background(), strings.NewReader(sse.String()), ch)
	}()

	sawStall := false
	for ev := range ch {
		if ev.Type == sonutruntime.StreamEventError && strings.Contains(ev.Error, "stream stalled") {
			sawStall = true
		}
		if ev.Type == sonutruntime.StreamEventDone {
			t.Fatalf("stream should have aborted before [DONE]")
		}
	}
	if !sawStall {
		t.Fatalf("expected a stream stalled error from the overthink guard")
	}
	if p.disableThinkingOnce.Load() {
		t.Fatalf("guard must NOT arm disableThinkingOnce (thinking stays on)")
	}
}

// A stream that produced real content must NOT trip the guard however long the
// reasoning is.
func TestOverthinkGuardIgnoresStreamsWithContent(t *testing.T) {
	var sse strings.Builder
	sse.WriteString(`data: {"choices":[{"delta":{"content":"answer"}}]}` + "\n\n")
	chunk := `data: {"choices":[{"delta":{"reasoning_content":"` + strings.Repeat("think ", 100) + `"}}]}` + "\n\n"
	for i := 0; i < 25; i++ {
		sse.WriteString(chunk)
	}
	sse.WriteString("data: [DONE]\n\n")

	p := &Provider{}
	ch := make(chan sonutruntime.StreamEvent, 256)
	go func() {
		defer close(ch)
		p.readSSE(context.Background(), strings.NewReader(sse.String()), ch)
	}()

	sawDone := false
	for ev := range ch {
		if ev.Type == sonutruntime.StreamEventError {
			t.Fatalf("unexpected error: %s", ev.Error)
		}
		if ev.Type == sonutruntime.StreamEventDone {
			sawDone = true
		}
	}
	if !sawDone || p.disableThinkingOnce.Load() {
		t.Fatalf("stream with content must complete normally (done=%v, armed=%v)", sawDone, p.disableThinkingOnce.Load())
	}
}
