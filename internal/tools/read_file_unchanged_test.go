package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileUnchangedShortCircuit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "src.go")
	body := strings.Repeat("func x() {}\n", 50)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	registry.Register(NewReadFileTool(root))
	tracker := NewFileTracker()
	opts := RunOptions{FileTracker: tracker, Cwd: root}

	first := registry.RunWithOptions(context.Background(), "read_file", map[string]any{"path": "src.go"}, opts)
	if first.Status != StatusOK {
		t.Fatalf("first read: %s", first.Output)
	}
	if strings.Contains(first.Output, "file unchanged") {
		t.Fatal("first read must return content")
	}
	if !strings.Contains(first.Output, "func x()") {
		t.Fatalf("first read missing body: %q", first.Output[:minInt(120, len(first.Output))])
	}

	second := registry.RunWithOptions(context.Background(), "read_file", map[string]any{"path": "src.go"}, opts)
	if second.Status != StatusOK {
		t.Fatalf("second read: %s", second.Output)
	}
	if second.Meta["file_unchanged"] != "true" {
		t.Fatalf("expected file_unchanged meta, got %#v / %q", second.Meta, second.Output)
	}
	if !strings.Contains(second.Output, "file unchanged") {
		t.Fatalf("second should short-circuit, got %q", second.Output)
	}

	// Different range must re-read.
	third := registry.RunWithOptions(context.Background(), "read_file", map[string]any{
		"path": "src.go", "start_line": 2, "max_lines": 5,
	}, opts)
	if third.Meta["file_unchanged"] == "true" {
		t.Fatal("different range must not short-circuit")
	}
	if !strings.Contains(third.Output, "func x()") {
		t.Fatalf("range read should return content, got %q", third.Output)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
