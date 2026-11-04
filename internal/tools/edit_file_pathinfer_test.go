package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A model that omits path after reading one file must still land the edit on
// that file (recovered via the session FileTracker), not fail the turn.
func TestEditFileInfersPathFromTrackedFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "page.html")
	writeTestFile(t, path, "<title>Old Title</title>\n")

	tracker := NewFileTracker()
	content, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	tracker.Record(path, content, info)

	tool := NewEditFileTool(root).(editFileTool)
	result := tool.RunWithOptions(context.Background(), map[string]any{
		"old_string": "Old Title",
		"new_string": "New Title",
	}, RunOptions{FileTracker: tracker})

	if result.Status != StatusOK {
		t.Fatalf("expected inferred-path edit ok, got %s: %s", result.Status, result.Output)
	}
	updated, _ := os.ReadFile(path)
	if string(updated) != "<title>New Title</title>\n" {
		t.Fatalf("unexpected content: %q", string(updated))
	}
}

// With several tracked files, the one containing old_string wins even when it
// is not the most recently recorded.
func TestEditFileInfersPathByContentMatch(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	other := filepath.Join(root, "b.txt")
	writeTestFile(t, target, "needle here\n")
	writeTestFile(t, other, "nothing relevant\n")

	tracker := NewFileTracker()
	for _, p := range []string{target, other} { // other recorded last
		content, _ := os.ReadFile(p)
		info, _ := os.Stat(p)
		tracker.Record(p, content, info)
	}

	tool := NewEditFileTool(root).(editFileTool)
	result := tool.RunWithOptions(context.Background(), map[string]any{
		"old_string": "needle here",
		"new_string": "replaced",
	}, RunOptions{FileTracker: tracker})

	if result.Status != StatusOK {
		t.Fatalf("expected content-matched edit ok, got %s: %s", result.Status, result.Output)
	}
	updated, _ := os.ReadFile(target)
	if string(updated) != "replaced\n" {
		t.Fatalf("expected edit on a.txt, got %q", string(updated))
	}
	untouched, _ := os.ReadFile(other)
	if string(untouched) != "nothing relevant\n" {
		t.Fatalf("b.txt must be untouched, got %q", string(untouched))
	}
}

// No path and nothing tracked: the error must be actionable, not a bare
// schema failure.
func TestEditFileMissingPathWithoutContextErrors(t *testing.T) {
	root := t.TempDir()
	tool := NewEditFileTool(root)
	result := tool.Run(context.Background(), map[string]any{
		"old_string": "a",
		"new_string": "b",
	})
	if result.Status != StatusError || !strings.Contains(result.Output, "path is required") {
		t.Fatalf("expected actionable path-required error, got %s: %s", result.Status, result.Output)
	}
}

func TestEditFileAcceptsReplacementContentAlias(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "page.html")
	writeTestFile(t, path, "<button>Before</button>\n")

	result := NewEditFileTool(root).Run(context.Background(), map[string]any{
		"path":       "page.html",
		"old_string": "Before",
		"content":    "After",
	})
	if result.Status != StatusOK {
		t.Fatalf("content alias edit failed: %s", result.Output)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != "<button>After</button>\n" {
		t.Fatalf("updated content = %q", updated)
	}
}

func TestEditFileMissingReplacementIsQuietAndDoesNotMutate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "page.html")
	writeTestFile(t, path, "<button>Before</button>\n")

	result := NewEditFileTool(root).Run(context.Background(), map[string]any{
		"path":       "page.html",
		"old_string": "Before",
	})
	if result.Status != StatusOK || !strings.Contains(result.Output, "No edit applied") {
		t.Fatalf("missing replacement = %#v, want quiet retry result", result)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != "<button>Before</button>\n" {
		t.Fatalf("missing replacement mutated file: %q", updated)
	}
}

func TestEditFileMissingAnchorIsQuietForLiveAgentCall(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "page.html")
	writeTestFile(t, path, "<button>Before</button>\n")

	result := NewEditFileTool(root).(editFileTool).RunWithOptions(context.Background(), map[string]any{
		"path":       "page.html",
		"new_string": "After",
	}, RunOptions{ToolCallID: "edit-live-missing-anchor"})
	if result.Status != StatusOK || result.Meta["retryable_arguments"] != "true" {
		t.Fatalf("missing live anchor = %#v, want quiet retry result", result)
	}
	if !strings.Contains(result.Output, "old_string was missing") {
		t.Fatalf("missing live anchor output = %q", result.Output)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != "<button>Before</button>\n" {
		t.Fatalf("missing anchor changed file: %q", updated)
	}
}

func TestEditFileStaleAnchorIsQuietForLiveAgentCall(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "page.html")
	writeTestFile(t, path, "<button>Current</button>\n")

	result := NewEditFileTool(root).(editFileTool).RunWithOptions(context.Background(), map[string]any{
		"path":       "page.html",
		"old_string": "stale text from an earlier read",
		"new_string": "Updated",
	}, RunOptions{ToolCallID: "edit-live-1"})
	if result.Status != StatusOK || result.Meta["edit_anchor_miss"] != "true" {
		t.Fatalf("live stale anchor = %#v, want quiet recovery result", result)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != "<button>Current</button>\n" {
		t.Fatalf("stale anchor changed file: %q", updated)
	}
}

// Live supermario thrash: model re-issued edit_file with old_string == new_string
// three times until hard-stop. Must return a strategy-change OK, not a silent
// one-liner that invites another identical call.
func TestEditFileIdenticalStringsSteersToAppendOrWrite(t *testing.T) {
	root := t.TempDir()
	// Truncated mid-generation (same shape as test/sonut/test/supermario.html).
	body := `<!DOCTYPE html>
<html lang="en">
<head><title>Super Mario</title></head>
<body>
<canvas id="gc"></canvas>
<script>
const TILE = 32;
const levelData = [
`
	path := filepath.Join(root, "supermario.html")
	writeTestFile(t, path, body)

	anchor := "const levelData = ["
	result := NewEditFileTool(root).Run(context.Background(), map[string]any{
		"path":       "supermario.html",
		"old_string": anchor,
		"new_string": anchor,
	})
	if result.Status != StatusOK {
		t.Fatalf("status = %s, want OK: %s", result.Status, result.Output)
	}
	if result.Meta["edit_noop"] != "true" {
		t.Fatalf("meta = %#v, want edit_noop", result.Meta)
	}
	for _, want := range []string{"identical", "Do NOT re-issue", "append_file", "TRUNCATED"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("output missing %q: %q", want, result.Output)
		}
	}
	// File must be unchanged.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("noop edit mutated file")
	}
}

func TestLooksLikeTruncatedGeneratedFile(t *testing.T) {
	if !looksLikeTruncatedGeneratedFile("supermario.html", "<!DOCTYPE html>\n<html>\n<script>\nconst x = [\n") {
		t.Fatal("open-ended HTML+script must count as truncated")
	}
	if looksLikeTruncatedGeneratedFile("page.html", "<!DOCTYPE html>\n<html><body>ok</body></html>\n") {
		t.Fatal("complete HTML must not count as truncated")
	}
}
