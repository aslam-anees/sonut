package agent

import (
	"strings"
	"testing"
)

func TestSandboxBlockStreakNudge(t *testing.T) {
	var g guardState
	g.recordSandboxBlock("exec_command", "Sandbox block [network] for bash", true)
	if g.checkSandboxBlockStreak() != "" {
		t.Fatal("should not fire on first block")
	}
	g.recordSandboxBlock("exec_command", "network access requires approval", true)
	if !strings.Contains(g.checkSandboxBlockStreak(), "web_fetch") {
		t.Fatal("should fire on second consecutive block")
	}
	g.recordSandboxBlock("exec_command", "ok output", false)
	g.recordSandboxBlock("exec_command", "Sandbox block", true)
	if g.checkSandboxBlockStreak() != "" {
		t.Fatal("successful exec should reset streak")
	}
	// Non-network tool successes do not reset.
	g.recordSandboxBlock("read_file", "contents", false)
	g.recordSandboxBlock("exec_command", "Sandbox block", true)
	if g.checkSandboxBlockStreak() == "" {
		t.Fatal("read_file success must not reset streak")
	}
	g.recordSandboxBlock("exec_command", "Sandbox block", true)
	if g.checkSandboxBlockStreak() != "" {
		t.Fatal("nudges must be bounded")
	}
}
