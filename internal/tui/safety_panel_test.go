package tui

import (
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/agent"
)

func TestSafetyStateRecordsDecisions(t *testing.T) {
	var s safetyState
	req := agent.PermissionRequest{ToolName: "exec_command", Permission: "network"}

	s.recordPermissionDecision(req, permissionDecisionDeny)
	s.recordPermissionDecision(req, permissionDecisionAllowForSession)

	if len(s.events) != 2 {
		t.Fatalf("expected 2 events, got %v", s.events)
	}
	if !strings.Contains(s.events[0], "denied") || !strings.Contains(s.events[0], "exec_command") {
		t.Fatalf("denial event = %q", s.events[0])
	}
	if !strings.Contains(s.events[1], "approved (session)") {
		t.Fatalf("session approval event = %q", s.events[1])
	}
}

func TestRenderSafetyLinesShowsModeAndEvents(t *testing.T) {
	m := model{permissionMode: agent.PermissionMode("standard")}
	joined := strings.Join(m.renderSafetyLines(40, 6), "\n")
	if !strings.Contains(joined, "SAFETY") || !strings.Contains(joined, "mode: standard") {
		t.Fatalf("missing header/mode: %q", joined)
	}
	if !strings.Contains(joined, "no approvals or denials yet") {
		t.Fatalf("missing empty placeholder: %q", joined)
	}

	m.safetyState.addEvent("✗ exec_command · network denied")
	joined = strings.Join(m.renderSafetyLines(60, 6), "\n")
	if !strings.Contains(joined, "network denied") {
		t.Fatalf("missing event: %q", joined)
	}
}
