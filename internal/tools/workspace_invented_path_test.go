package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sandbox"
)

// Regression: the model wrote "./sonut/mario.html" inside a workspace already
// named ".../sonut". The duplicated leading segment must be stripped so the
// write lands at <root>/mario.html instead of failing or creating a shadow
// sonut/ subtree.
func TestResolveScopedTargetPathStripsDuplicatedRootSegment(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "sonut")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	absolute, relative, err := resolveScopedTargetPath(workspace, nil, "./sonut/mario.html")
	if err != nil {
		t.Fatalf("resolveScopedTargetPath: %v", err)
	}
	if relative != "mario.html" {
		t.Fatalf("relative = %q, want %q (absolute=%q)", relative, "mario.html", absolute)
	}
}

// The same duplicated-root typo can arrive as a fully expanded absolute path
// after a model copies the workspace name into an already workspace-relative
// path. It must land at the workspace root without requiring a second model
// generation or exposing a scope error.
func TestResolveScopedTargetPathStripsAbsoluteDuplicatedRootSegment(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "sonut")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(workspace, "sonut", "supermario.html")
	absolute, relative, err := resolveScopedTargetPath(workspace, nil, fake)
	if err != nil {
		t.Fatalf("resolveScopedTargetPath(%q): %v", fake, err)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if relative != "supermario.html" || absolute != filepath.Join(canonicalWorkspace, "supermario.html") {
		t.Fatalf("resolved duplicate absolute path to %q, %q", absolute, relative)
	}
	if _, err := os.Stat(filepath.Join(workspace, "sonut")); !os.IsNotExist(err) {
		t.Fatalf("duplicate root directory should not be created, stat err=%v", err)
	}
}

// A caller that blindly joins the active cwd with an already absolute model
// path can hand the tool a relative-looking value such as
// "dummy//Users/.../dummy/game.html". The resolver must recover the embedded
// absolute operand and never create a Users subtree inside dummy.
func TestResolveScopedTargetPathRecoversEmbeddedAbsoluteCwd(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "dummy")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	requested := filepath.Base(workspace) + "//" + workspace + string(filepath.Separator) + "game.html"
	absolute, relative, err := resolveScopedTargetPath(workspace, nil, requested)
	if err != nil {
		t.Fatalf("resolveScopedTargetPath(%q): %v", requested, err)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonicalWorkspace, "game.html")
	if absolute != want || relative != "game.html" {
		t.Fatalf("resolved embedded absolute path to %q, %q; want %q, game.html", absolute, relative, want)
	}
	if _, err := os.Stat(filepath.Join(workspace, "Users")); !os.IsNotExist(err) {
		t.Fatalf("embedded absolute path created a Users subtree, stat err=%v", err)
	}
}

// The same mistake can lose the leading slash while retaining the complete
// workspace path. This is the path form produced by a few shell/model bridges.
func TestResolveScopedTargetPathRecoversAbsolutePathWithoutLeadingSlash(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "dummy")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	requested := strings.TrimPrefix(workspace, string(filepath.Separator)) + string(filepath.Separator) + "game.html"
	absolute, relative, err := resolveScopedTargetPath(workspace, nil, requested)
	if err != nil {
		t.Fatalf("resolveScopedTargetPath(%q): %v", requested, err)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonicalWorkspace, "game.html")
	if absolute != want || relative != "game.html" {
		t.Fatalf("resolved missing-slash absolute path to %q, %q; want %q, game.html", absolute, relative, want)
	}
	if _, err := os.Stat(filepath.Join(workspace, "tmp")); !os.IsNotExist(err) {
		t.Fatalf("missing-slash absolute path created a tmp subtree, stat err=%v", err)
	}
}

// A REAL subdirectory named after the root must be respected — no strip.
func TestResolveScopedTargetPathKeepsRealRootNamedSubdir(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "sonut")
	if err := os.MkdirAll(filepath.Join(workspace, "sonut"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, relative, err := resolveScopedTargetPath(workspace, nil, "sonut/mario.html")
	if err != nil {
		t.Fatalf("resolveScopedTargetPath: %v", err)
	}
	if relative != "sonut/mario.html" {
		t.Fatalf("relative = %q, want %q", relative, "sonut/mario.html")
	}
}

// Regression: the model read "/Users/x/asistatine/sonut/mario.html" (typo'd
// prefix) when the real root is ".../aistatine/sonut". The invented prefix is
// remapped into the root — same rule the sandbox engine allows — so the read
// succeeds instead of looping on a denial.
func TestResolveScopedReadPathRemapsInventedPrefix(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "sonut")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "mario.html"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	scope, err := sandbox.NewScope(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(string(filepath.Separator), "Users", "x", "asistatine", "sonut", "mario.html")
	absolute, _, err := resolveScopedReadPath(workspace, scope, fake)
	if err != nil {
		t.Fatalf("resolveScopedReadPath(%q): %v", fake, err)
	}
	if filepath.Base(absolute) != "mario.html" || !filepath.IsAbs(absolute) {
		t.Fatalf("absolute = %q", absolute)
	}
	if _, statErr := os.Stat(absolute); statErr != nil {
		t.Fatalf("remapped path must exist: %v", statErr)
	}
}

// Same remap for write targets: the target need not exist yet.
func TestResolveScopedTargetPathRemapsInventedPrefix(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "sonut")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	scope, err := sandbox.NewScope(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(string(filepath.Separator), "Users", "x", "asistatine", "sonut", "new.html")
	absolute, _, err := resolveScopedTargetPath(workspace, scope, fake)
	if err != nil {
		t.Fatalf("resolveScopedTargetPath(%q): %v", fake, err)
	}
	if filepath.Base(absolute) != "new.html" {
		t.Fatalf("absolute = %q", absolute)
	}
}

// macOS can expose the same user files through a synthetic Data/home prefix
// in subprocess output. The model must not turn that alias into a literal
// System/Volumes/Data subtree, and a generated file must resolve in one call.
func TestResolveScopedTargetPathRemapsMacOSDataHomePrefix(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "sonut")
	if err := os.MkdirAll(filepath.Join(workspace, "dummy"), 0o755); err != nil {
		t.Fatal(err)
	}
	scope, err := sandbox.NewScope(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(string(filepath.Separator), "System", "Volumes", "Data", "home", "aslam", "Desktop", "aistatine", "sonut", "dummy", "flappy_bird.html")
	absolute, relative, err := resolveScopedTargetPath(workspace, scope, fake)
	if err != nil {
		t.Fatalf("resolveScopedTargetPath(%q): %v", fake, err)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonicalWorkspace, "dummy", "flappy_bird.html")
	if absolute != want || relative != filepath.ToSlash(filepath.Join("dummy", "flappy_bird.html")) {
		t.Fatalf("resolved macOS Data/home path to %q, %q; want %q, dummy/flappy_bird.html", absolute, relative, want)
	}
	if err := recheckScopedWriteTargetAt(workspace, scope, fake, workspace); err != nil {
		t.Fatalf("recheckScopedWriteTargetAt(%q): %v", fake, err)
	}
}

// Exercise the complete write path, not only its resolver. This is the
// regression for the screenshot: resolution accepted the repaired target, but
// the pre-write recheck used to reject the original alias and force a retry.
func TestWriteFileCompletesAfterMacOSDataHomePathRepair(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "sonut")
	if err := os.MkdirAll(filepath.Join(workspace, "dummy"), 0o755); err != nil {
		t.Fatal(err)
	}
	scope, err := sandbox.NewScope(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(string(filepath.Separator), "System", "Volumes", "Data", "home", "aslam", "Desktop", "aistatine", "sonut", "dummy", "flappy_bird.html")
	tool, ok := NewScopedWriteFileTool(workspace, scope).(optionsAwareTool)
	if !ok {
		t.Fatal("write_file must support RunWithOptions")
	}
	content := "<!doctype html>\n<html><body><canvas id=\"game\"></canvas></body></html>\n"
	result := tool.RunWithOptions(context.Background(), map[string]any{
		"path": fake, "content": content, "overwrite": true,
	}, RunOptions{PermissionGranted: true, Cwd: workspace, FileTracker: NewFileTracker()})
	if result.Status != StatusOK {
		t.Fatalf("write_file failed after path repair: %q", result.Output)
	}
	got, err := os.ReadFile(filepath.Join(workspace, "dummy", "flappy_bird.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("written content changed during path repair: %q", got)
	}
}
