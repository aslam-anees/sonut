package evolution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadContextUsesBudgetAndKeepsSourcesBounded(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SONUT_EVOLUTION_DIR", filepath.Join(root, "memory"))
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	paths, err := Ensure(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Memory, []byte("# Project memory\n\n## Rules\n\n"+strings.Repeat("rule ", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecordRun(project, "session-1", "implement durable memory", "checkpoint saved"); err != nil {
		t.Fatal(err)
	}

	ctx, err := LoadContext(project, "session-1", ContextOptions{BudgetTokens: 120, IncludeNotes: true})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Text == "" || ctx.Tokens > 150 {
		t.Fatalf("context = %#v, want non-empty bounded context", ctx)
	}
	if len(ctx.Sources) < 2 {
		t.Fatalf("sources = %#v, want project and session context", ctx.Sources)
	}
	if !ctx.Truncated {
		t.Fatal("expected oversized memory to be marked truncated")
	}
}

func TestRecordRunIsRedactedAndKeepsRecentHistory(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	secret := "apiKey=sk-test-1234567890"
	for i := 0; i < maxRecentWorkLines+3; i++ {
		if err := RecordRun(project, "session-1", "prompt "+secret, "outcome "+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	paths := ResolvePaths(project, "session-1")
	memory, err := os.ReadFile(paths.Memory)
	if err != nil {
		t.Fatal(err)
	}
	text := string(memory)
	if strings.Contains(text, secret) || !strings.Contains(text, "outcome ") {
		t.Fatalf("memory leaked or missed recent work: %s", text)
	}
	if strings.Contains(text, "outcome a") {
		t.Fatal("oldest history entry should have been evicted")
	}
	if !strings.Contains(text, "outcome o") {
		t.Fatal("newest history entry should be retained")
	}
	checkpoint, err := os.ReadFile(paths.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(checkpoint), secret) {
		t.Fatal("checkpoint leaked redacted input")
	}
}

func TestProjectIDIsStableAndPathTraversalDoesNotEscapeSessionRoot(t *testing.T) {
	project := t.TempDir()
	if ProjectID(project) != ProjectID(project) {
		t.Fatal("project id is not stable")
	}
	paths := ResolvePaths(project, "../escape")
	if paths.SessionDir != filepath.Join(paths.Root, "sessions") {
		t.Fatalf("unsafe session id changed session root: %q", paths.SessionDir)
	}
}
