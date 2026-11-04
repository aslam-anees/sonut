package tui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aistatine/sonut/internal/agent"
)

func resetWarmupStateForTest(t *testing.T) {
	t.Helper()
	// Drain any leftover state from prior tests in this package.
	cancelActiveWarmups()
	waitForWarmupsToDrain(500 * time.Millisecond)
	userRunActive.Store(false)
	// Force counters clean in case a prior test timed out mid-flight.
	warmupCancelMu.Lock()
	warmupCancels = map[int]context.CancelFunc{}
	warmupCancelMu.Unlock()
	warmupInFlight.Store(0)
}

// A user run must cancel in-flight warmups and refuse new ones so the single
// llama slot is free for the real turn (the multi-minute 0-tok hang).
func TestUserRunPreemptsWarmup(t *testing.T) {
	resetWarmupStateForTest(t)
	t.Cleanup(func() { resetWarmupStateForTest(t) })

	// Start a long-lived "warmup" that holds the cancel registry.
	ctx, cancel, ok := warmupContext(30 * time.Second)
	if !ok {
		t.Fatal("warmupContext should succeed while idle")
	}
	defer cancel()
	if warmupInFlight.Load() != 1 {
		t.Fatalf("warmupInFlight = %d, want 1", warmupInFlight.Load())
	}

	// User starts a run: warmups cancel and new ones are refused.
	setUserRunActive(true)
	select {
	case <-ctx.Done():
		// expected
	case <-time.After(200 * time.Millisecond):
		t.Fatal("warmup context was not canceled by setUserRunActive(true)")
	}

	_, _, ok = warmupContext(5 * time.Second)
	if ok {
		t.Fatal("warmupContext must refuse while a user run is active")
	}

	setUserRunActive(false)
	_, cancel2, ok := warmupContext(5 * time.Second)
	if !ok {
		t.Fatal("warmupContext should succeed again after the run ends")
	}
	cancel2()
}

// cancelActiveWarmups must free every registered cancel and log a non-zero count.
func TestCancelActiveWarmupsCancelsAll(t *testing.T) {
	resetWarmupStateForTest(t)
	t.Cleanup(func() { resetWarmupStateForTest(t) })

	var canceled atomic.Int32
	for i := 0; i < 3; i++ {
		ctx, cancel, ok := warmupContext(30 * time.Second)
		if !ok {
			t.Fatal("warmupContext failed")
		}
		go func(c context.Context, done context.CancelFunc) {
			<-c.Done()
			canceled.Add(1)
			done()
		}(ctx, cancel)
	}

	n := cancelActiveWarmups()
	if n != 3 {
		t.Fatalf("cancelActiveWarmups = %d, want 3", n)
	}
	deadline := time.Now().Add(time.Second)
	for canceled.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if canceled.Load() != 3 {
		t.Fatalf("canceled count = %d, want 3", canceled.Load())
	}
	waitForWarmupsToDrain(time.Second)
	if warmupInFlight.Load() != 0 {
		t.Fatalf("warmupInFlight after drain = %d, want 0", warmupInFlight.Load())
	}
}

// postWarmup must abort quickly when the context is canceled mid-stream so the
// slot is free for the user — this is the whole point of stream:true warmups.
func TestPostWarmupHonorsCancel(t *testing.T) {
	resetWarmupStateForTest(t)
	t.Cleanup(func() { resetWarmupStateForTest(t) })

	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		// Stream forever until the client disconnects.
		for {
			if _, err := io.WriteString(w, "data: {\"choices\":[{\"delta\":{}}]}\n\n"); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- postWarmup(ctx, srv.URL, `{"stream":true,"max_tokens":1}`)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("server never saw the warmup request")
	}
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("postWarmup should return an error after cancel")
		}
		if !strings.Contains(err.Error(), "context canceled") && ctx.Err() == nil {
			// Accept any error as long as it returned promptly; some transports
			// surface "canceled" differently.
			t.Logf("postWarmup error after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("postWarmup did not return after context cancel — slot would stay busy")
	}
}

// effortWarmupPayload must request stream:true so cancel can free the slot,
// keep thinking ON (live agent always thinks), and enable cache_prompt so the
// loading-screen prefill is reused by the first homepage message.
func TestEffortWarmupPayloadIsStreaming(t *testing.T) {
	payload := effortWarmupPayload("system prompt", "none", agentOptionsEmpty())
	if !strings.Contains(payload, `"stream":true`) {
		t.Fatalf("warmup payload must use stream:true, got %s", payload)
	}
	if strings.Contains(payload, `"stream":false`) {
		t.Fatal("warmup payload must not set stream:false")
	}
	if !strings.Contains(payload, `"thinking":true`) || !strings.Contains(payload, `"enable_thinking":true`) {
		t.Fatalf("warmup payload must keep thinking ON, got %s", payload)
	}
	if strings.Contains(payload, `"thinking":false`) {
		t.Fatal("warmup payload must not disable thinking")
	}
	if !strings.Contains(payload, `"cache_prompt":true`) {
		t.Fatalf("warmup payload must set cache_prompt:true, got %s", payload)
	}
}

// agentOptionsEmpty is a tiny helper so the payload test does not need a full
// agent.Options with tools registered.
func agentOptionsEmpty() agent.Options {
	return agent.Options{}
}
