package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A /cd override must move where RELATIVE tool paths resolve (write_file,
// exec cwd default), while absolute paths keep validating against the roots.
func TestSessionWorkingDirMovesRelativeResolution(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	defer SetSessionWorkingDir("")

	abs, _, err := resolveScopedTargetPath(root, nil, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(abs) != mustEval(t, root) {
		t.Fatalf("expected %s, got %s", root, abs)
	}

	SetSessionWorkingDir(sub)
	abs, _, err = resolveScopedTargetPath(root, nil, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(abs) != mustEval(t, sub) {
		t.Fatalf("expected file to land in /cd dir %s, got %s", sub, abs)
	}

	// Empty path (exec default cwd) resolves to the /cd dir too.
	abs, _, err = resolveScopedPath(root, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if abs != mustEval(t, sub) {
		t.Fatalf("expected exec default cwd %s, got %s", sub, abs)
	}
}

// Regression for the production failure:
//
//	registry launch root: .../sonut
//	resumed/current cwd:   .../sonut/dummy
//	model path:            dummy/search.html
//
// RunOptions.Cwd must win over the stale process-global cwd. The duplicated
// "dummy/" prefix is then normalized against the current cwd, so every tool
// touches .../dummy/search.html and never creates .../dummy/dummy/search.html.
func TestRegistryRunCwdPreventsDuplicatedSessionDirectory(t *testing.T) {
	launchRoot := t.TempDir()
	sessionCwd := filepath.Join(launchRoot, "dummy")
	staleOtherSession := filepath.Join(launchRoot, "other")
	if err := os.MkdirAll(sessionCwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(staleOtherSession, 0o755); err != nil {
		t.Fatal(err)
	}

	SetSessionWorkingDir(staleOtherSession)
	defer SetSessionWorkingDir("")

	registry := NewRegistry()
	registry.Register(NewWriteFileTool(launchRoot))
	registry.Register(NewAppendFileTool(launchRoot))
	registry.Register(NewReadFileTool(launchRoot))
	registry.Register(NewListDirectoryTool(launchRoot))
	registry.Register(NewGlobTool(launchRoot))
	registry.Register(NewGrepTool(launchRoot))
	options := RunOptions{
		PermissionGranted: true,
		Cwd:               sessionCwd,
		FileTracker:       NewFileTracker(),
	}

	write := registry.RunWithOptions(context.Background(), "write_file", map[string]any{
		"path":    "dummy/search.html",
		"content": "<main>search</main>\n",
	}, options)
	if write.Status != StatusOK {
		t.Fatalf("write_file failed: %s", write.Output)
	}
	if !strings.Contains(write.Output, "search.html") ||
		strings.Contains(write.Output, "dummy/dummy") {
		t.Fatalf("write_file reported wrong target: %s", write.Output)
	}

	appendResult := registry.RunWithOptions(context.Background(), "append_file", map[string]any{
		"path":    "dummy/search.html",
		"content": "<!-- ready -->\n",
	}, options)
	if appendResult.Status != StatusOK {
		t.Fatalf("append_file failed: %s", appendResult.Output)
	}

	read := registry.RunWithOptions(context.Background(), "read_file", map[string]any{
		"path": "dummy/search.html",
	}, options)
	if read.Status != StatusOK || !strings.Contains(read.Output, "<!-- ready -->") {
		t.Fatalf("read_file did not read current-cwd target: %s", read.Output)
	}

	list := registry.RunWithOptions(context.Background(), "list_directory", map[string]any{
		"path": "dummy",
	}, options)
	if list.Status != StatusOK || !strings.Contains(list.Output, "search.html") {
		t.Fatalf("list_directory did not normalize duplicate cwd: %s", list.Output)
	}

	glob := registry.RunWithOptions(context.Background(), "glob", map[string]any{
		"pattern": "*.html",
	}, options)
	if glob.Status != StatusOK || strings.TrimSpace(glob.Output) != "search.html" {
		t.Fatalf("glob ignored per-run cwd: %s", glob.Output)
	}

	grep := registry.RunWithOptions(context.Background(), "grep", map[string]any{
		"pattern": "ready",
	}, options)
	if grep.Status != StatusOK || !strings.Contains(grep.Output, "search.html:") {
		t.Fatalf("grep ignored per-run cwd: %s", grep.Output)
	}

	if _, err := os.Stat(filepath.Join(sessionCwd, "search.html")); err != nil {
		t.Fatalf("expected current-cwd file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessionCwd, "dummy")); !os.IsNotExist(err) {
		t.Fatalf("shadow dummy directory must not exist, stat err=%v", err)
	}
	if entries, err := os.ReadDir(staleOtherSession); err != nil || len(entries) != 0 {
		t.Fatalf("stale global cwd was used: entries=%v err=%v", entries, err)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
