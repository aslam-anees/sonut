package sonutruntime

import (
	"strings"
	"testing"
	"time"
)

func TestCollectorClampsExecCommandArgCaps(t *testing.T) {
	c := newToolCallCollector(64*1024, 5*time.Minute)
	c.start("1", "exec_command", "")
	c.delta("1", strings.Repeat("x", execCommandMaxArgBytes+1))
	call := c.calls["1"]
	if !call.Aborted || !strings.Contains(call.AbortReason, "write_file") {
		t.Fatalf("exec byte cap not applied: aborted=%v reason=%q", call.Aborted, call.AbortReason)
	}

	// A long-running call that is still PROGRESSING must never be aborted by
	// wall-clock time (the 86s-generation-cut-at-60s failure mode).
	c2 := newToolCallCollector(64*1024, 5*time.Minute)
	c2.start("2", "exec_command", "")
	c2.startedAt["2"] = time.Now().Add(-2 * time.Minute)
	c2.delta("2", "{")
	if c2.calls["2"].Aborted {
		t.Fatal("progressing exec call wrongly aborted by wall-clock age")
	}
	if c2.markStalled(defaultToolArgStallTimeout) {
		t.Fatal("fresh delta should not read as a stall")
	}

	// A call whose fragments stopped arriving IS aborted by the stall watchdog.
	c2.lastDeltaAt["2"] = time.Now().Add(-defaultToolArgStallTimeout - time.Second)
	if !c2.markStalled(defaultToolArgStallTimeout) {
		t.Fatal("stalled exec call not aborted")
	}
	if got := c2.calls["2"].AbortReason; !strings.Contains(got, "silent") {
		t.Fatalf("unexpected stall abort reason: %q", got)
	}

	// An ended call must not be stall-checked.
	c3 := newToolCallCollector(64*1024, 5*time.Minute)
	c3.start("3", "exec_command", "")
	c3.delta("3", `{"command":"ls"}`)
	c3.end("3")
	c3.lastDeltaAt["3"] = time.Now().Add(-time.Hour)
	if c3.markStalled(defaultToolArgStallTimeout) {
		t.Fatal("completed call wrongly flagged as stalled")
	}
}
