package sonutruntime

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCollectorClampsEditFileArgCaps(t *testing.T) {
	c := newToolCallCollector(64*1024, 5*time.Minute)
	c.start("1", "edit_file", "")
	c.startedAt["1"] = time.Now()
	c.delta("1", strings.Repeat("x", editFileMaxArgBytes+1))
	call := c.calls["1"]
	if !call.Aborted || !strings.Contains(call.AbortReason, "write_file") {
		t.Fatalf("edit_file byte cap not applied: aborted=%v reason=%q", call.Aborted, call.AbortReason)
	}

	// Wall-clock age alone must NOT abort a call that is still progressing;
	// only a fragment stall does (see markStalled).
	c2 := newToolCallCollector(64*1024, 5*time.Minute)
	c2.start("2", "edit_file", "")
	c2.startedAt["2"] = time.Now().Add(-2 * time.Minute)
	c2.delta("2", "{")
	if c2.calls["2"].Aborted {
		t.Fatal("progressing edit_file call wrongly aborted by wall-clock age")
	}
	c2.lastDeltaAt["2"] = time.Now().Add(-defaultToolArgStallTimeout - time.Second)
	if !c2.markStalled(defaultToolArgStallTimeout) {
		t.Fatal("stalled edit_file call not aborted")
	}

	// write_file also uses the shared file ceiling, so a payload just over the
	// old 4 KiB edit guard is still accepted as a valid local file stream.
	c3 := newToolCallCollector(64*1024, 5*time.Minute)
	c3.start("3", "write_file", "")
	c3.startedAt["3"] = time.Now().Add(-2 * time.Minute)
	c3.delta("3", strings.Repeat("y", defaultMaxToolArgBytes+1))
	if c3.calls["3"].Aborted {
		t.Fatalf("write_file wrongly clamped: %q", c3.calls["3"].AbortReason)
	}
}

func TestCollectorDefaultToolArgumentDeadlineStopsActiveRunaway(t *testing.T) {
	c := newToolCallCollector(defaultMaxToolArgBytes, defaultMaxToolArgDuration)
	c.start("write-1", "write_file", "")
	c.startedAt["write-1"] = time.Now().Add(-defaultMaxToolArgDuration - time.Millisecond)
	c.delta("write-1", `{"path":"index.html","content":"still streaming"`)

	call := c.calls["write-1"]
	if !call.Aborted {
		t.Fatal("a continuously streaming tool call must stop at the default deadline")
	}
	if !strings.Contains(call.AbortReason, "took longer") {
		t.Fatalf("abort reason = %q, want duration limit", call.AbortReason)
	}
}

func TestResolveMaxToolArgDurationHonorsOverride(t *testing.T) {
	t.Setenv("SONUT_TOOL_ARG_TIMEOUT", "17s")
	if got := resolveMaxToolArgDuration(0); got != 17*time.Second {
		t.Fatalf("environment override = %s, want 17s", got)
	}
	if got := resolveMaxToolArgDuration(3 * time.Second); got != 3*time.Second {
		t.Fatalf("explicit option = %s, want 3s", got)
	}
}

func TestFileToolArgumentDeadlineAllowsHealthyLongGeneration(t *testing.T) {
	c := newToolCallCollector(defaultMaxToolArgBytes, defaultMaxToolArgDuration)
	c.maxFileArgDuration = defaultMaxFileToolArgDuration
	c.start("write-1", "write_file", "")
	c.startedAt["write-1"] = time.Now().Add(-2 * time.Minute)
	c.delta("write-1", `{"path":"index.html","content":"still progressing"`)
	if c.calls["write-1"].Aborted {
		t.Fatalf("healthy file generation was cut at the ordinary deadline: %q", c.calls["write-1"].AbortReason)
	}

	// Past the absolute deadline with no meaningful line progress → abort.
	c.startedAt["write-1"] = time.Now().Add(-defaultMaxFileToolArgDuration - time.Millisecond)
	c.delta("write-1", "x")
	if !c.calls["write-1"].Aborted {
		t.Fatal("file generation with no line progress must stop at its bounded deadline")
	}
}

func TestFileToolArgumentDeadlineSkipsHealthyLineProgress(t *testing.T) {
	c := newToolCallCollector(defaultMaxToolArgBytes, defaultMaxToolArgDuration)
	c.maxFileArgDuration = defaultMaxFileToolArgDuration
	c.fileLineStall = 45 * time.Second
	c.start("write-1", "write_file", "")
	// Past the absolute deadline, but lines are still completing — must keep going.
	c.startedAt["write-1"] = time.Now().Add(-defaultMaxFileToolArgDuration - time.Minute)
	c.delta("write-1", `{"path":"game.js","content":"one\ntwo\nthree\nfour\nfive\nsix\nseven`)
	if c.calls["write-1"].Aborted {
		t.Fatalf("progressing multi-line file generation must not be killed by wall-clock alone: %q", c.calls["write-1"].AbortReason)
	}
	// Fresh line keeps progress clock warm.
	c.delta("write-1", `\neight`)
	if c.calls["write-1"].Aborted {
		t.Fatalf("new source line must keep healthy generation alive: %q", c.calls["write-1"].AbortReason)
	}
}

func TestResolveMaxFileToolArgDurationHonorsOverrides(t *testing.T) {
	t.Setenv("SONUT_FILE_TOOL_ARG_TIMEOUT", "4m")
	if got := resolveMaxFileToolArgDuration(0); got != 4*time.Minute {
		t.Fatalf("file environment override = %s, want 4m", got)
	}
	if got := resolveMaxFileToolArgDuration(3 * time.Second); got != 3*time.Second {
		t.Fatalf("explicit file option = %s, want 3s", got)
	}
}

func TestStreamingFileContentProgressHandlesIncompleteJSON(t *testing.T) {
	arguments := `{"path":"game.js","content":"line one\nline two\nline three`
	contentLen, lines, ok := streamingFileContentProgress(arguments)
	if !ok {
		t.Fatal("expected an incomplete streaming content string to be recognized")
	}
	if lines != 3 {
		t.Fatalf("lines = %d, want 3", lines)
	}
	if contentLen == 0 {
		t.Fatal("expected decoded content progress")
	}
}

func TestFileLineStallAbortsDespiteRawArgumentProgress(t *testing.T) {
	collector := newToolCallCollector(defaultMaxToolArgBytes, defaultMaxToolArgDuration)
	collector.fileLineStall = 40 * time.Millisecond
	collector.start("write-1", "write_file", "")
	collector.delta("write-1", `{"path":"game.js","content":"one\ntwo\nthree\nfour\nfive\nsix`)
	collector.lastFileLineAt["write-1"] = time.Now().Add(-collector.fileLineStall - time.Millisecond)

	// More raw bytes keep lastDeltaAt fresh, reproducing the live bug: tokens and
	// argument size advance while the source line count remains frozen.
	collector.delta("write-1", strings.Repeat("x", 128))
	if !collector.markStalled(defaultToolArgStallTimeout) {
		t.Fatal("expected semantic file-line stall to abort the call")
	}
	call := collector.calls["write-1"]
	if !call.Aborted || !strings.Contains(call.AbortReason, "stopped completing new lines") {
		t.Fatalf("unexpected abort: aborted=%v reason=%q", call.Aborted, call.AbortReason)
	}
}

func TestFileLineProgressResetsSemanticStallClock(t *testing.T) {
	collector := newToolCallCollector(defaultMaxToolArgBytes, defaultMaxToolArgDuration)
	collector.fileLineStall = 40 * time.Millisecond
	collector.start("write-1", "write_file", "")
	collector.delta("write-1", `{"path":"game.js","content":"one\ntwo\nthree\nfour\nfive`)
	collector.lastFileLineAt["write-1"] = time.Now().Add(-collector.fileLineStall - time.Millisecond)
	collector.delta("write-1", `\nsix`)
	if collector.markStalled(defaultToolArgStallTimeout) {
		t.Fatal("a newly completed source line must count as healthy progress")
	}
}

func TestCollectStreamStopsTokenGrowthWhenFileLinesFreeze(t *testing.T) {
	t.Setenv("SONUT_FILE_LINE_STALL_TIMEOUT", "20ms")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	events := make(chan StreamEvent, 8)
	events <- StreamEvent{Type: StreamEventToolCallStart, ToolCallID: "write-1", ToolName: "write_file"}
	events <- StreamEvent{
		Type:              StreamEventToolCallDelta,
		ToolCallID:        "write-1",
		ArgumentsFragment: `{"path":"game.js","content":"one\ntwo\nthree\nfour\nfive\nsix`,
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case events <- StreamEvent{Type: StreamEventToolCallDelta, ToolCallID: "write-1", ArgumentsFragment: "xxxxxxxx"}:
				case <-stop:
					return
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	collected := CollectStreamWithOptions(ctx, events, CollectOptions{})
	if collected.Error != "" {
		t.Fatalf("semantic stall should return a recoverable aborted call, got error %q", collected.Error)
	}
	if len(collected.ToolCalls) != 1 || !collected.ToolCalls[0].Aborted {
		t.Fatalf("expected one aborted partial write, got %#v", collected.ToolCalls)
	}
	if !strings.Contains(collected.ToolCalls[0].AbortReason, "stopped completing new lines") {
		t.Fatalf("unexpected abort reason: %q", collected.ToolCalls[0].AbortReason)
	}
}
