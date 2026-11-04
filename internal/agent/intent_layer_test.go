package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// First touch of a sub-tree surfaces its AGENTS.md exactly once; the root
// chain (already injected at prompt build) is never re-surfaced; paths outside
// the workspace are ignored.
func TestIntentLayerSurfacesNestedGuidelinesOnce(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "internal", "auth")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("never log tokens in auth code"), 0o644); err != nil {
		t.Fatal(err)
	}

	state := newIntentLayerState(root)

	note := state.observeToolCall(map[string]any{"path": "internal/auth/session.go"})
	if !strings.Contains(note, "never log tokens in auth code") {
		t.Fatalf("expected nested guidelines surfaced, got %q", note)
	}
	if strings.Contains(note, "root rules") {
		t.Fatal("root guidelines are already in the system prompt and must not be re-surfaced")
	}

	// Second touch of the same tree: nothing new.
	if again := state.observeToolCall(map[string]any{"file_path": filepath.Join(sub, "token.go")}); again != "" {
		t.Fatalf("guidelines must be surfaced once per run, got %q", again)
	}

	// Outside the workspace: ignored.
	if out := state.observeToolCall(map[string]any{"path": "/etc/passwd"}); out != "" {
		t.Fatalf("paths outside the workspace must be ignored, got %q", out)
	}
}

// A run with no Cwd (headless tests, unit fixtures) disables the layer without
// changing behavior.
func TestIntentLayerDisabledWithoutCwd(t *testing.T) {
	state := newIntentLayerState("")
	if note := state.observeToolCall(map[string]any{"path": "a/b.go"}); note != "" {
		t.Fatalf("expected disabled layer, got %q", note)
	}
}
