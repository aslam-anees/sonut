package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// "/index.html" must resolve to the workspace-root index.html instead of
// "nothing there" (models use URL-style leading-slash paths constantly).
func TestScopedPathRootSlashFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	abs, rel, err := resolveScopedReadPath(root, nil, "/index.html")
	if err != nil {
		t.Fatalf("read fallback failed: %v", err)
	}
	if rel != "index.html" || filepath.Base(abs) != "index.html" {
		t.Fatalf("unexpected resolution: abs=%q rel=%q", abs, rel)
	}

	if _, _, err := resolveScopedPath(root, nil, "/index.html"); err != nil {
		t.Fatalf("path fallback failed: %v", err)
	}

	// Write target that doesn't exist yet: "/style.css" lands at workspace root.
	absT, relT, err := resolveScopedTargetPath(root, nil, "/style.css")
	if err != nil {
		t.Fatalf("target fallback failed: %v", err)
	}
	if relT != "style.css" || filepath.Dir(absT) == "/" {
		t.Fatalf("unexpected target resolution: abs=%q rel=%q", absT, relT)
	}

	// Nested URL-style paths remain valid; only host-root prefixes such as
	// /Users and /var are rejected as accidental full-machine paths.
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	absNested, relNested, err := resolveScopedTargetPath(root, nil, "/src/app.js")
	if err != nil {
		t.Fatalf("nested target fallback failed: %v", err)
	}
	if relNested != filepath.ToSlash(filepath.Join("src", "app.js")) || filepath.Dir(absNested) == "/" {
		t.Fatalf("unexpected nested target resolution: abs=%q rel=%q", absNested, relNested)
	}

	// A real absolute path outside the workspace still fails for reads.
	if _, _, err := resolveScopedReadPath(root, nil, "/etc/hosts"); err == nil {
		t.Fatal("expected outside-workspace read to fail")
	}
}

// Live supermario thrash: cwd=.../sonut/test + path=sonut/test/supermario.html
// must write at cwd/supermario.html, not cwd/sonut/test/supermario.html.
func TestWriteFileStripsMultiSegmentAncestryAfterCD(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "sonut")
	cwd := filepath.Join(root, "test")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := NewWriteFileTool(root).(optionsAwareTool)
	result := tool.RunWithOptions(context.Background(), map[string]any{
		"path":    "sonut/test/supermario.html",
		"content": "<!doctype html>\n<html><body><canvas id=\"gc\"></canvas><script></script></body></html>\n",
	}, RunOptions{Cwd: cwd, PermissionGranted: true})
	if result.Status != StatusOK {
		t.Fatalf("write failed: %s", result.Output)
	}
	want := filepath.Join(cwd, "supermario.html")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected file at %s: %v", want, err)
	}
	shadow := filepath.Join(cwd, "sonut", "test", "supermario.html")
	if _, err := os.Stat(shadow); !os.IsNotExist(err) {
		t.Fatalf("shadow nested path must not be created: %s (stat err=%v)", shadow, err)
	}
}

// After /cd into a directory named "test", models list path="/test". That must
// resolve to the workspace root itself, not nest as test/test or die outside.
func TestScopedPathBareBasenameIsWorkspaceRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "test")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mario.html"), []byte("<html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	abs, rel, err := resolveScopedReadPath(root, nil, "/test")
	if err != nil {
		t.Fatalf("list /test failed: %v", err)
	}
	if rel != "." {
		t.Fatalf("rel = %q, want . (workspace root, not nested test/)", rel)
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	gotRoot, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != wantRoot {
		t.Fatalf("abs = %q, want workspace root %q", gotRoot, wantRoot)
	}

	// Nested file under bare basename.
	absFile, relFile, err := resolveScopedTargetPath(root, nil, "/test/mario.html")
	if err != nil {
		t.Fatalf("target /test/mario.html failed: %v", err)
	}
	if relFile != "mario.html" {
		t.Fatalf("relFile = %q, want mario.html", relFile)
	}
	if filepath.Base(absFile) != "mario.html" {
		t.Fatalf("absFile = %q", absFile)
	}
}
