package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditFilePreservesFailedReplacementForExactReuse(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "button.js")
	if err := os.WriteFile(path, []byte("const old = true;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditFileTool(root).(optionsAwareTool)

	failed := tool.RunWithOptions(context.Background(), map[string]any{
		"path":       "button.js",
		"old_string": "const missing = true;",
		"new_string": "const replacement = true;\n",
	}, RunOptions{ToolCallID: "edit-failed"})
	if failed.Status != StatusOK || !strings.Contains(failed.Output, "reuse_replacement") {
		t.Fatalf("failed edit did not advertise exact recovery: %#v", failed)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != "const old = true;\n" {
		t.Fatalf("failed edit changed target: %q", unchanged)
	}

	retried := tool.RunWithOptions(context.Background(), map[string]any{
		"path":              "button.js",
		"old_string":        "const old = true;",
		"reuse_replacement": true,
	}, RunOptions{ToolCallID: "edit-retry"})
	if retried.Status != StatusOK {
		t.Fatalf("reuse edit failed: %s", retried.Output)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != "const replacement = true;\n\n" {
		t.Fatalf("reused replacement was not applied exactly: %q", updated)
	}

	second := tool.RunWithOptions(context.Background(), map[string]any{
		"path":              "button.js",
		"old_string":        "const replacement = true;",
		"reuse_replacement": true,
	}, RunOptions{ToolCallID: "edit-retry-again"})
	if second.Status != StatusError || !strings.Contains(second.Output, "no saved edit replacement") {
		t.Fatalf("edit recovery was not consumed: %#v", second)
	}
}
