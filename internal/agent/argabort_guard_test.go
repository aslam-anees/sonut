package agent

import (
	"strings"
	"testing"
)

func TestArgAbortStreakNudge(t *testing.T) {
	var g guardState
	g.recordArgAbort()
	if !strings.Contains(g.checkArgAbortStreak(), "STOP") {
		t.Fatal("should fire on the first abort")
	}
	g.recordArgDispatch()
	if g.checkArgAbortStreak() != "" {
		t.Fatal("dispatch resets the streak; no abort means no nudge")
	}
	// Bounded: second nudge fires, third does not.
	g.recordArgAbort()
	if g.checkArgAbortStreak() == "" {
		t.Fatal("second nudge should still fire")
	}
	g.recordArgAbort()
	if g.checkArgAbortStreak() != "" {
		t.Fatal("nudges must be bounded")
	}
}

func TestDecodeToolArgumentsRepairsMalformedJSON(t *testing.T) {
	var v map[string]any
	// truncated object (missing closing brace) — the mid-stream abort shape
	if err := decodeToolArguments(`{"command": "ls -la"`, &v); err != nil {
		t.Fatalf("truncated object should be repaired: %v", err)
	}
	if v["command"] != "ls -la" {
		t.Fatalf("got %v", v)
	}
	// single quotes + trailing comma — common small-model near-JSON
	v = nil
	if err := decodeToolArguments(`{'path': 'a.go',}`, &v); err != nil {
		t.Fatalf("near-JSON should be repaired: %v", err)
	}
	if v["path"] != "a.go" {
		t.Fatalf("got %v", v)
	}
	// valid JSON must pass through untouched
	v = nil
	if err := decodeToolArguments(`{"x":1}`, &v); err != nil {
		t.Fatal(err)
	}
}
