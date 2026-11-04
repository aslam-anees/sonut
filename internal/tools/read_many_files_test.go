package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadManyFilesBatchesPaths(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("a.go", "package a\n// line2\n// line3\n")
	mustWrite("b.go", "package b\n// only\n")

	tool := NewReadManyFilesTool(root)
	result := tool.Run(context.Background(), map[string]any{
		"paths": []any{"a.go", "b.go"},
	})
	if result.Status != StatusOK {
		t.Fatalf("status=%s output=%q", result.Status, result.Output)
	}
	if !strings.Contains(result.Output, "a.go") || !strings.Contains(result.Output, "b.go") {
		t.Fatalf("missing file sections: %q", result.Output)
	}
	if !strings.Contains(result.Output, "package a") || !strings.Contains(result.Output, "package b") {
		t.Fatalf("missing content: %q", result.Output)
	}
	if result.Meta["files_ok"] != "2" {
		t.Fatalf("meta files_ok=%q", result.Meta["files_ok"])
	}
}

func TestReadManyFilesRejectsEmptyAndCaps(t *testing.T) {
	root := t.TempDir()
	tool := NewReadManyFilesTool(root)
	empty := tool.Run(context.Background(), map[string]any{"paths": []any{}})
	if empty.Status != StatusError {
		t.Fatalf("empty paths should error, got %s", empty.Status)
	}

	paths := make([]any, readManyMaxPaths+1)
	for i := range paths {
		paths[i] = "f.go"
	}
	tooMany := tool.Run(context.Background(), map[string]any{"paths": paths})
	if tooMany.Status != StatusError || !strings.Contains(tooMany.Output, "limited") {
		t.Fatalf("expected max path error, got %s %q", tooMany.Status, tooMany.Output)
	}
}

func TestReadManyFilesStringListArgTolerant(t *testing.T) {
	paths, err := stringListArg(map[string]any{"paths": "a.go, b.go\nc.go"}, "paths", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("got %v", paths)
	}
}
