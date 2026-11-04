package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileRecoverySurvivesTrackerRebuild(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "game.html")
	if err := os.WriteFile(path, []byte("<html>old</html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "<!DOCTYPE html>\n<html><body><canvas></canvas><script>// complete generated game</script></body></html>\n"
	tool := NewWriteFileTool(root).(optionsAwareTool)
	first := tool.RunWithOptions(context.Background(), map[string]any{
		"path":    "game.html",
		"content": body,
	}, RunOptions{PermissionGranted: true})
	if first.Status != StatusError || !strings.Contains(first.Output, "reuse_content") {
		t.Fatalf("write refusal did not preserve a retryable body: %#v", first)
	}

	// Simulate a rebuilt tool/session with no in-memory FileTracker. The disk
	// journal must still provide the exact body, rather than forcing generation.
	second := tool.RunWithOptions(context.Background(), map[string]any{
		"path":          "game.html",
		"overwrite":     true,
		"reuse_content": true,
	}, RunOptions{PermissionGranted: true})
	if second.Status != StatusOK {
		t.Fatalf("disk recovery failed: %s", second.Output)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("recovered body = %q", got)
	}

	third := tool.RunWithOptions(context.Background(), map[string]any{
		"path":          "game.html",
		"overwrite":     true,
		"reuse_content": true,
	}, RunOptions{PermissionGranted: true})
	if third.Status != StatusError || !strings.Contains(third.Output, "no stashed content") {
		t.Fatalf("stale recovery artifact remained usable: %#v", third)
	}
}

func TestAppendFileRecoverySurvivesPathFailure(t *testing.T) {
	root := t.TempDir()
	tool := NewAppendFileTool(root).(optionsAwareTool)
	failed := tool.RunWithOptions(context.Background(), map[string]any{
		"path":    "../outside.txt",
		"content": "append body that must survive\n",
	}, RunOptions{PermissionGranted: true})
	if failed.Status != StatusError || !strings.Contains(failed.Output, "SAVED") {
		t.Fatalf("append path failure did not preserve body: %#v", failed)
	}

	retried := tool.RunWithOptions(context.Background(), map[string]any{
		"path":          "recovered.txt",
		"reuse_content": true,
	}, RunOptions{PermissionGranted: true})
	if retried.Status != StatusOK {
		t.Fatalf("append recovery failed: %s", retried.Output)
	}
	got, err := os.ReadFile(filepath.Join(root, "recovered.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "append body that must survive\n" {
		t.Fatalf("recovered append = %q", got)
	}
}
