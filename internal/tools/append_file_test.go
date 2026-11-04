package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendFileRejectsStaleTrackedTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "app.js")
	const baseline = "const version = 1;\n"
	if err := os.WriteFile(target, []byte(baseline), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	trackedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	tracker := NewFileTracker()
	tracker.Record(trackedTarget, []byte(baseline), info)

	const external = "const version = 2;\n"
	if err := os.WriteFile(target, []byte(external), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	registry.Register(NewAppendFileTool(root))
	result := registry.RunWithOptions(context.Background(), "append_file", map[string]any{
		"path":    "app.js",
		"content": "start();\n",
	}, RunOptions{PermissionGranted: true, FileTracker: tracker, Cwd: root})
	if result.Status != StatusError {
		t.Fatalf("stale append status = %s, want error: %s", result.Status, result.Output)
	}
	if !strings.Contains(result.Output, ErrFileChangedOnDisk.Error()) {
		t.Fatalf("stale append output = %q", result.Output)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != external {
		t.Fatalf("stale append changed file: got %q, want %q", string(got), external)
	}
}

func TestAppendFileRetryIsIdempotent(t *testing.T) {
	root := t.TempDir()
	registry := NewRegistry()
	registry.Register(NewAppendFileTool(root))
	tracker := NewFileTracker()
	options := RunOptions{PermissionGranted: true, FileTracker: tracker, Cwd: root}
	args := map[string]any{"path": "styles.css", "content": ".result { color: blue; }\n"}

	first := registry.RunWithOptions(context.Background(), "append_file", args, options)
	if first.Status != StatusOK || len(first.ChangedFiles) != 1 {
		t.Fatalf("first append = %#v", first)
	}
	second := registry.RunWithOptions(context.Background(), "append_file", args, options)
	if second.Status != StatusOK || len(second.ChangedFiles) != 0 || !strings.Contains(second.Output, "already ends with") {
		t.Fatalf("replayed append was not a no-op: %#v", second)
	}
	got, err := os.ReadFile(filepath.Join(root, "styles.css"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != args["content"] {
		t.Fatalf("append chunk duplicated: %q", got)
	}
}
