package sonutruntime

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A stream that never emits its FIRST event (dead pooled connection, hung
// prefill) must be abandoned as a stream-timeout stall — the exact "0 tokens,
// stuck before generating" hang — instead of blocking the turn forever. The
// error wording must match the agent loop's stream-timeout needles ("no
// output for") so the turn is reissued.
func TestCollectStreamAbandonsDeadAirBeforeFirstEvent(t *testing.T) {
	events := make(chan StreamEvent) // never written to, never closed
	done := make(chan CollectedStream, 1)
	go func() {
		done <- CollectStreamWithOptions(context.Background(), events, CollectOptions{
			MaxStreamSilence: 60 * time.Millisecond,
		})
	}()
	select {
	case collected := <-done:
		if !strings.Contains(collected.Error, "no output for") {
			t.Fatalf("error = %q, want a 'no output for' stream-timeout", collected.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("collect never returned: dead-air watchdog did not fire")
	}
}

// Any arriving event resets the silence window: a slow but progressing stream
// is never killed by the dead-air watchdog.
func TestCollectStreamSilenceWindowResetsOnEvents(t *testing.T) {
	events := make(chan StreamEvent)
	go func() {
		for i := 0; i < 6; i++ {
			time.Sleep(40 * time.Millisecond) // below the 80ms budget each time
			events <- StreamEvent{Type: StreamEventText, Content: "x"}
		}
		events <- StreamEvent{Type: StreamEventDone}
		close(events)
	}()
	collected := CollectStreamWithOptions(context.Background(), events, CollectOptions{
		MaxStreamSilence: 80 * time.Millisecond,
	})
	if collected.Error != "" {
		t.Fatalf("progressing stream was killed: %q", collected.Error)
	}
	if collected.Text != "xxxxxx" {
		t.Fatalf("text = %q, want xxxxxx", collected.Text)
	}
}
