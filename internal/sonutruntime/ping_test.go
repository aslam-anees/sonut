package sonutruntime

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A stream of prefill-progress pings must keep the whole-stream silence
// tracker alive: a long healthy prefill emits pings but no content events.
func TestCollectStreamPingResetsSilenceClock(t *testing.T) {
	events := make(chan StreamEvent)
	go func() {
		defer close(events)
		// Silence budget is 300ms; ping every 100ms for ~1s, then answer.
		for i := 0; i < 10; i++ {
			time.Sleep(100 * time.Millisecond)
			events <- StreamEvent{Type: StreamEventPing}
		}
		events <- StreamEvent{Type: StreamEventText, Content: "hi"}
		events <- StreamEvent{Type: StreamEventDone}
	}()

	pings := 0
	collected := CollectStreamWithOptions(context.Background(), events, CollectOptions{
		MaxStreamSilence: 300 * time.Millisecond,
		OnPing:           func(processed, total int) { pings++ },
	})

	if collected.Error != "" {
		t.Fatalf("pinged stream must not stall, got error: %s", collected.Error)
	}
	if collected.Text != "hi" {
		t.Fatalf("text = %q, want %q", collected.Text, "hi")
	}
	if pings != 10 {
		t.Fatalf("OnPing fired %d times, want 10", pings)
	}
}

// Without pings the same silent stream still trips the silence tracker —
// the ping path must not have weakened the dead-air guard.
func TestCollectStreamSilenceStillFiresWithoutPings(t *testing.T) {
	events := make(chan StreamEvent) // never receives anything
	collected := CollectStreamWithOptions(context.Background(), events, CollectOptions{
		MaxStreamSilence: 200 * time.Millisecond,
	})
	if !strings.Contains(collected.Error, "no output for") {
		t.Fatalf("expected silence stall error, got: %q", collected.Error)
	}
}

// Heartbeats must not turn a stalled prefill into an unbounded request. The
// total prefill ceiling is separate from the silence budget because a server
// can keep the connection alive without making meaningful progress.
func TestCollectStreamPrefillDeadlineFiresDespiteHeartbeats(t *testing.T) {
	events := make(chan StreamEvent)
	stop := make(chan struct{})
	defer close(stop)
	done := make(chan CollectedStream, 1)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			select {
			case <-stop:
				return
			case events <- StreamEvent{Type: StreamEventPing, PrefillProcessed: 1, PrefillTotal: 1000}:
			}
		}
	}()
	go func() {
		done <- CollectStreamWithOptions(context.Background(), events, CollectOptions{
			MaxStreamSilence:   time.Second,
			MaxPrefillDuration: 60 * time.Millisecond,
		})
	}()

	select {
	case collected := <-done:
		if !strings.Contains(collected.Error, "prompt prefill exceeded") {
			t.Fatalf("error = %q, want prompt-prefill deadline", collected.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prefill deadline did not fire while heartbeats continued")
	}
}
