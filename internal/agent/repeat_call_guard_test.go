package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

type countingExecCommandTool struct {
	calls int
}

func (tool *countingExecCommandTool) Name() string        { return "exec_command" }
func (tool *countingExecCommandTool) Description() string { return "test command tool" }
func (tool *countingExecCommandTool) Parameters() tools.Schema {
	return tools.Schema{
		Type:                 "object",
		Properties:           map[string]tools.PropertySchema{"command": {Type: "string"}},
		Required:             []string{"command"},
		AdditionalProperties: false,
	}
}
func (tool *countingExecCommandTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectShell, Permission: tools.PermissionAllow}
}
func (tool *countingExecCommandTool) Run(context.Context, map[string]any) tools.Result {
	tool.calls++
	return tools.Result{Status: tools.StatusOK, Output: "command completed"}
}

func TestRepeatedIdenticalCallNudge(t *testing.T) {
	var g guardState
	args := `{"path":"stock.py"}`
	g.recordCallSignature("read_file", args)
	g.recordCallSignature("read_file", args)
	if g.takeRepeatNudge() != "" {
		t.Fatal("should not fire before third repeat")
	}
	g.recordCallSignature("read_file", args)
	if !strings.Contains(g.takeRepeatNudge(), "ALREADY") {
		t.Fatal("should fire on third identical call")
	}
	if g.takeRepeatNudge() != "" {
		t.Fatal("nudge must be consumed once")
	}
}

// Consecutive byte-identical calls of ANY tool (mutating included) are a model
// loop: the second is blocked instead of dispatched, and the streak hard-stops
// the run at consecutiveCallStopAt.
func TestConsecutiveIdenticalCallBlockAndStop(t *testing.T) {
	var g guardState
	args := `{"command":"mkdir -p src/game src/levels"}`

	// First call dispatches normally (streak = 1, threshold is ≥ 2).
	if out := g.recordCallSignature("bash", args); out.Blocked || out.Stop {
		t.Fatalf("call 1 must dispatch normally, got %+v", out)
	}

	// Second consecutive identical call is blocked (streak = 2 ≥ threshold).
	out := g.recordCallSignature("bash", args)
	if !out.Blocked || out.Stop || !strings.Contains(out.BlockNotice, "already ran") {
		t.Fatalf("second consecutive identical call must be blocked with 'already ran', got %+v", out)
	}

	// Third: streak hits consecutiveCallStopAt → hard-stop the run.
	out = g.recordCallSignature("bash", args)
	if !out.Stop {
		t.Fatalf("streak of %d must halt the run, got %+v", consecutiveCallStopAt, out)
	}

	// A different call resets the streak — legitimate re-runs separated by
	// other work are never blocked.
	if out := g.recordCallSignature("bash", `{"command":"go test ./..."}`); out.Blocked || out.Stop {
		t.Fatalf("new call must reset the streak, got %+v", out)
	}
	if out := g.recordCallSignature("bash", args); out.Blocked || out.Stop {
		t.Fatalf("non-consecutive repeat must dispatch, got %+v", out)
	}
}

// TestDeduplicateCallReturnsCachedOutput verifies that after the agent loop
// caches a tool result, the second identical call is blocked and the BlockNotice
// contains the cached output rather than a generic error.
func TestDeduplicateCallReturnsCachedOutput(t *testing.T) {
	var g guardState
	name := "bash"
	args := `{"command":"ls -la /tmp/build"}`
	cachedOutput := "total 8\ndrwxr-xr-x  2 user user 4096 Jul 18 10:00 .\n"

	// First call — dispatches normally; loop caches the result.
	if out := g.recordCallSignature(name, args); out.Blocked {
		t.Fatal("first call must dispatch normally")
	}
	g.cacheToolCallResult(name, args, cachedOutput)

	// Second call — must be blocked and BlockNotice must embed the cached output.
	out := g.recordCallSignature(name, args)
	if !out.Blocked {
		t.Fatal("second consecutive identical call must be blocked")
	}
	if !strings.Contains(out.BlockNotice, cachedOutput) {
		t.Fatalf("BlockNotice must contain cached output; got:\n%s", out.BlockNotice)
	}
}

func TestCallGuardCanonicalizesJSONArgumentOrder(t *testing.T) {
	var g guardState
	first := `{"path":"index.html","start_line":1,"end_line":80}`
	second := `{"end_line":80,"path":"index.html","start_line":1}`
	if out := g.recordCallSignature("read_file", first); out.Blocked || out.Stop {
		t.Fatalf("first call must dispatch, got %+v", out)
	}
	g.cacheToolCallResult("read_file", first, "file contents")
	out := g.recordCallSignature("read_file", second)
	if !out.Blocked || out.Stop {
		t.Fatalf("same JSON call with reordered keys must be blocked, got %+v", out)
	}
	if !strings.Contains(out.BlockNotice, "file contents") {
		t.Fatalf("canonicalized repeat did not receive cached result: %q", out.BlockNotice)
	}
}

// Live supermario: model re-issued edit_file with identical old/new three times.
// Cached no-op result must redirect to append_file/write_file, not a bare replay.
func TestIdenticalEditFileNoopRedirectsToAppendOrWrite(t *testing.T) {
	var g guardState
	name := "edit_file"
	args := `{"path":"supermario.html","old_string":"const levelData = [","new_string":"const levelData = ["}`
	cached := "No changes: new_string is identical to old_string — this edit does nothing. Do NOT re-issue the same edit_file call. The file looks TRUNCATED mid-generation."

	if out := g.recordCallSignature(name, args); out.Blocked || out.Stop {
		t.Fatalf("first edit must dispatch, got %+v", out)
	}
	g.cacheToolCallResult(name, args, cached)

	out := g.recordCallSignature(name, args)
	if !out.Blocked {
		t.Fatal("second identical edit_file must be blocked")
	}
	for _, want := range []string{"append_file", "write_file", "BLOCKED", "no-op"} {
		if !strings.Contains(out.BlockNotice, want) {
			t.Fatalf("BlockNotice missing %q:\n%s", want, out.BlockNotice)
		}
	}
}

// TestFiveIdenticalCallsOneRealExecution is the regression guard: when a model
// emits the same tool call five times in a row, only the first one must pass
// through recordCallSignature unblocked (simulating 1 real execution), and the
// run must halt before the fifth dispatch.
func TestFiveIdenticalCallsOneRealExecution(t *testing.T) {
	var g guardState
	name := "bash"
	args := `{"command":"ls -la /workspace"}`

	dispatched := 0
	for i := 1; i <= 5; i++ {
		out := g.recordCallSignature(name, args)
		if out.Stop {
			// Run halted — verify we never executed more than once.
			break
		}
		if !out.Blocked {
			dispatched++
			// Simulate the loop caching the tool result after real execution.
			g.cacheToolCallResult(name, args, "some output")
		}
	}

	if dispatched != 1 {
		t.Fatalf("expected exactly 1 real execution, got %d", dispatched)
	}
}

func TestIdenticalCallsInOneAssistantBatchAreSuppressedBeforeHardStop(t *testing.T) {
	var g guardState
	seen := make(map[string]int)
	name := "exec_command"
	args := `{"command":"printf loop-proof"}`

	for i := 0; i < 3; i++ {
		out := g.recordCallSignatureInBatch(name, args, seen)
		if out.Stop {
			t.Fatalf("same-response duplicate %d must not hard-stop before feedback is delivered: %+v", i+1, out)
		}
		if i == 0 && out.Blocked {
			t.Fatalf("first call in the response must execute normally: %+v", out)
		}
		if i > 0 && !out.Blocked {
			t.Fatalf("duplicate %d must be suppressed: %+v", i+1, out)
		}
	}

	if got := seen[toolCallKey(name, args)]; got != 3 {
		t.Fatalf("same-response call count = %d, want 3", got)
	}
	if !strings.Contains(g.repeatedCallBlockNotice(name, args, 3), "Do NOT issue it again") {
		t.Fatal("duplicate notice must tell the model to choose another action")
	}
}

func TestRunGivesSameResponseDuplicatesAChanceToRecover(t *testing.T) {
	tool := &countingExecCommandTool{}
	registry := tools.NewRegistry()
	registry.Register(tool)
	args := `{"command":"printf loop-proof"}`
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		{
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "call-1", ToolName: "exec_command"},
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "call-1", ArgumentsFragment: args},
			{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "call-1"},
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "call-2", ToolName: "exec_command"},
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "call-2", ArgumentsFragment: args},
			{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "call-2"},
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "call-3", ToolName: "exec_command"},
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "call-3", ArgumentsFragment: args},
			{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "call-3"},
			{Type: sonutruntime.StreamEventDone},
		},
		textTurn("done after one command"),
	}}

	result, err := Run(context.Background(), "open the page", provider, Options{
		Registry:     registry,
		MaxTurns:     2,
		RunMode:      RunModeBuilder,
		ProviderName: "openai",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done after one command" {
		t.Fatalf("final answer = %q, want recovery turn", result.FinalAnswer)
	}
	if tool.calls != 1 {
		t.Fatalf("duplicate same-response calls executed %d times, want exactly once", tool.calls)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want initial batch plus recovery turn", len(provider.requests))
	}
	var resultIDs = map[string]bool{}
	var sawBlockNotice bool
	for _, message := range provider.requests[1].Messages {
		if message.Role == sonutruntime.MessageRoleTool {
			resultIDs[message.ToolCallID] = true
			if strings.Contains(message.Content, "Do NOT issue it again") || strings.Contains(message.Content, "Do not repeat") {
				sawBlockNotice = true
			}
		}
		if message.Role == sonutruntime.MessageRoleUser && strings.Contains(message.Content, "STOP LOOPING") {
			sawBlockNotice = true
		}
	}
	for _, id := range []string{"call-1", "call-2", "call-3"} {
		if !resultIDs[id] {
			t.Fatalf("missing paired tool result for %s", id)
		}
	}
	if !sawBlockNotice {
		t.Fatal("recovery turn did not contain duplicate-call feedback")
	}
}
