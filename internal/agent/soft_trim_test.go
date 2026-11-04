package agent

import (
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
)

func TestSoftTrimPreservesEndsAndReclaims(t *testing.T) {
	// Body large enough to soft-trim: ~20k tokens of 'x', bookended with markers.
	head := "HEAD_MARK"
	tail := "TAIL_MARK"
	middle := strings.Repeat("m", 80_000) // ~20k tokens
	body := head + middle + tail
	msgs := []sonutruntime.Message{
		toolCallMsg("old", "bash"),
		{Role: sonutruntime.MessageRoleTool, ToolCallID: "old", Content: body},
		// Push past recent protect window with another large recent result.
		toolCallMsg("mid", "read_file"),
		bigToolResult("mid", 45000),
		{Role: sonutruntime.MessageRoleUser, Content: "continue"},
	}

	out, reclaimed := softTrimStaleToolOutput(msgs, 2)
	if reclaimed <= 0 {
		t.Fatalf("expected soft-trim reclaim, got %d", reclaimed)
	}
	if !isSoftTrimmed(out[1].Content) {
		t.Fatalf("old body should be soft-trimmed, got prefix %q", out[1].Content[:min(80, len(out[1].Content))])
	}
	if !strings.Contains(out[1].Content, head) {
		t.Fatal("soft-trim must keep head")
	}
	if !strings.Contains(out[1].Content, tail) {
		t.Fatal("soft-trim must keep tail")
	}
	if len(out[1].Content) >= len(body)/4 {
		t.Fatalf("soft-trim should drop most of the middle bulk: got %d bytes from %d", len(out[1].Content), len(body))
	}
	// Recent result stays intact.
	if isSoftTrimmed(out[3].Content) {
		t.Fatal("recent protect window must not soft-trim")
	}
	// Idempotent.
	_, r2 := softTrimStaleToolOutput(out, 2)
	if r2 != 0 {
		t.Fatalf("second soft-trim should no-op, reclaimed=%d", r2)
	}
}

func TestSoftTrimDoesNotMutateInput(t *testing.T) {
	body := "HEAD" + strings.Repeat("x", 80_000) + "TAIL"
	msgs := []sonutruntime.Message{
		toolCallMsg("old", "grep"),
		{Role: sonutruntime.MessageRoleTool, ToolCallID: "old", Content: body},
		toolCallMsg("mid", "read_file"),
		bigToolResult("mid", 45000),
		{Role: sonutruntime.MessageRoleUser, Content: "go"},
	}
	original := msgs[1].Content
	_, n := softTrimStaleToolOutput(msgs, 1)
	if n == 0 {
		t.Fatal("expected soft-trim")
	}
	if msgs[1].Content != original {
		t.Fatal("softTrimStaleToolOutput must not mutate caller slice")
	}
}

func TestSoftTrimSkipsSkillAndToolSearch(t *testing.T) {
	body := strings.Repeat("s", 80_000)
	msgs := []sonutruntime.Message{
		toolCallMsg("s1", toolNameSkill),
		{Role: sonutruntime.MessageRoleTool, ToolCallID: "s1", Content: body},
		toolCallMsg("mid", "bash"),
		bigToolResult("mid", 45000),
		{Role: sonutruntime.MessageRoleUser, Content: "x"},
	}
	out, reclaimed := softTrimStaleToolOutput(msgs, 1)
	if reclaimed != 0 {
		t.Fatalf("skill results must not soft-trim, reclaimed=%d", reclaimed)
	}
	if out[1].Content != body {
		t.Fatal("skill body changed")
	}
}

func TestReclaimContextCheapLadder(t *testing.T) {
	// Soft-trim candidate + hard-prune candidate mass.
	msgs := []sonutruntime.Message{
		toolCallMsg("old", "bash"),
		{Role: sonutruntime.MessageRoleTool, ToolCallID: "old", Content: "HEAD" + strings.Repeat("x", 80_000) + "TAIL"},
		toolCallMsg("older", "grep"),
		bigToolResult("older", 60000),
		toolCallMsg("mid", "read_file"),
		bigToolResult("mid", 45000),
		{Role: sonutruntime.MessageRoleUser, Content: "next"},
	}
	out, n := reclaimContextCheap(msgs, 2)
	if n <= 0 {
		t.Fatalf("ladder should reclaim, got %d", n)
	}
	// At least one of soft-trim or hard-prune should have fired.
	if !isSoftTrimmed(out[1].Content) && !isPrunedPlaceholder(out[1].Content) &&
		!isPrunedPlaceholder(out[3].Content) && !isSoftTrimmed(out[3].Content) {
		t.Fatal("expected at least one reclaimed tool body")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
