package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// safetyRegistry wires the read/write tools the conflict-detection feature spans,
// so tests exercise the real RunWithOptions dispatch (including optionsAwareTool
// routing) rather than calling tool methods directly.
func safetyRegistry(t *testing.T, dir string) *Registry {
	t.Helper()
	registry := NewRegistry()
	for _, tool := range []Tool{NewReadFileTool(dir), NewEditFileTool(dir), NewWriteFileTool(dir)} {
		registry.Register(tool)
	}
	return registry
}

func grantedOpts(tracker *FileTracker) RunOptions {
	return RunOptions{PermissionGranted: true, FileTracker: tracker}
}

func TestEditFileSafelyRebasesUniqueAnchorAfterFileChangedOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := safetyRegistry(t, dir)
	tracker := NewFileTracker()

	// Model reads the file → baseline recorded.
	if res := registry.RunWithOptions(context.Background(), "read_file", map[string]any{"path": "f.txt"}, grantedOpts(tracker)); res.Status != StatusOK {
		t.Fatalf("read_file failed: %s", res.Output)
	}
	// Something outside @code rewrites the file.
	if err := os.WriteFile(path, []byte("alpha changed externally\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The exact anchor still exists uniquely, so the surgical edit can rebase
	// safely while preserving the external suffix.
	res := registry.RunWithOptions(context.Background(), "edit_file", map[string]any{
		"path": "f.txt", "old_string": "alpha", "new_string": "beta",
	}, grantedOpts(tracker))
	if res.Status != StatusOK {
		t.Fatalf("expected a safe stale rebase, got status=%s output=%q", res.Status, res.Output)
	}
	if got, _ := os.ReadFile(path); string(got) != "beta changed externally\n" {
		t.Fatalf("safe rebase did not preserve external content, got %q", got)
	}
}

func TestEditFileStillRefusesStaleFileWithoutUniqueExactAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := safetyRegistry(t, dir)
	tracker := NewFileTracker()
	registry.RunWithOptions(context.Background(), "read_file", map[string]any{"path": "f.txt"}, grantedOpts(tracker))
	if err := os.WriteFile(path, []byte("completely replaced externally\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := registry.RunWithOptions(context.Background(), "edit_file", map[string]any{
		"path": "f.txt", "old_string": "alpha", "new_string": "beta",
	}, grantedOpts(tracker))
	if res.Status != StatusError || !strings.Contains(res.Output, "changed on disk") {
		t.Fatalf("expected incompatible stale edit refusal, got status=%s output=%q", res.Status, res.Output)
	}
	if got, _ := os.ReadFile(path); string(got) != "completely replaced externally\n" {
		t.Fatalf("incompatible stale edit changed the file: %q", got)
	}
}

func TestEditFileStaleBaselineIsNoOpWhenEditAlreadyApplied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := safetyRegistry(t, dir)
	tracker := NewFileTracker()
	registry.RunWithOptions(context.Background(), "read_file", map[string]any{"path": "f.txt"}, grantedOpts(tracker))
	if err := os.WriteFile(path, []byte("beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := registry.RunWithOptions(context.Background(), "edit_file", map[string]any{
		"path": "f.txt", "old_string": "alpha", "new_string": "beta",
	}, grantedOpts(tracker))
	if res.Status != StatusOK || !strings.Contains(res.Output, "already contains") {
		t.Fatalf("already-applied stale edit should be a no-op success, got status=%s output=%q", res.Status, res.Output)
	}
	current, err := os.ReadFile(path)
	if err != nil || tracker.CheckConflict(path, current) != nil {
		t.Fatalf("already-applied edit did not rebaseline current content: readErr=%v", err)
	}
}

func TestEditFileSucceedsWhenUnchangedAndRebaselines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := safetyRegistry(t, dir)
	tracker := NewFileTracker()

	registry.RunWithOptions(context.Background(), "read_file", map[string]any{"path": "f.txt"}, grantedOpts(tracker))
	if res := registry.RunWithOptions(context.Background(), "edit_file", map[string]any{
		"path": "f.txt", "old_string": "alpha", "new_string": "beta",
	}, grantedOpts(tracker)); res.Status != StatusOK {
		t.Fatalf("edit on unchanged file should succeed, got %q", res.Output)
	}
	// A second edit must not false-conflict: the write re-baselined the tracker.
	if res := registry.RunWithOptions(context.Background(), "edit_file", map[string]any{
		"path": "f.txt", "old_string": "beta", "new_string": "gamma",
	}, grantedOpts(tracker)); res.Status != StatusOK {
		t.Fatalf("second consecutive edit should succeed (re-baselined), got %q", res.Output)
	}
	if got, _ := os.ReadFile(path); string(got) != "gamma\n" {
		t.Fatalf("file = %q, want gamma", got)
	}
}

func TestEditFileStaleAnchorFallsBackToAtomicFullDocumentWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "button.html")
	if err := os.WriteFile(path, []byte("<html><body>old</body></html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := safetyRegistry(t, dir)
	content := "<!doctype html>\n<html>\n<head><title>Buttons</title></head>\n<body>\n" +
		strings.Repeat("<button class=\"cracker\">Blast</button>\n", 120) +
		"</body>\n</html>\n"
	res := registry.RunWithOptions(context.Background(), "edit_file", map[string]any{
		"path": "button.html", "old_string": "stale anchor from an earlier file", "new_string": content,
	}, grantedOpts(NewFileTracker()))
	if res.Status != StatusOK || res.Meta["edit_full_file_fallback"] != "true" {
		t.Fatalf("expected safe full-file fallback, got status=%s meta=%#v output=%q", res.Status, res.Meta, res.Output)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("fallback wrote unexpected content: got %d bytes, want %d", len(got), len(content))
	}
}

func TestEditFileNeverNestsCompleteHTMLDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "button.html")
	if err := os.WriteFile(path, []byte("<!doctype html>\n<html><body><button>old</button></body></html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	replacement := "<!doctype html>\n<html><body><button>new</button></body></html>\n"
	result := NewEditFileTool(dir).Run(context.Background(), map[string]any{
		"path": "button.html", "old_string": "old", "new_string": replacement,
	})
	if result.Status != StatusOK || result.Meta["edit_full_file_fallback"] != "true" {
		t.Fatalf("complete HTML edit = %#v, want full-file fallback", result)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != replacement {
		t.Fatalf("complete HTML was nested/replaced incorrectly: %q", updated)
	}
}

func TestWriteFileOverwriteRefusedWhenChangedOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := safetyRegistry(t, dir)
	tracker := NewFileTracker()

	registry.RunWithOptions(context.Background(), "read_file", map[string]any{"path": "f.txt"}, grantedOpts(tracker))
	if err := os.WriteFile(path, []byte("v2 external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := registry.RunWithOptions(context.Background(), "write_file", map[string]any{
		"path": "f.txt", "content": "v3 stale\n", "overwrite": true,
	}, grantedOpts(tracker))
	if res.Status != StatusError || !strings.Contains(res.Output, "changed on disk") {
		t.Fatalf("expected overwrite refusal, got status=%s output=%q", res.Status, res.Output)
	}
	if got, _ := os.ReadFile(path); string(got) != "v2 external\n" {
		t.Fatalf("file must be untouched after a refused overwrite, got %q", got)
	}
}

func TestWriteFileOutsidePathPreservesGeneratedBodyForReuse(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "button.txt")
	registry := safetyRegistry(t, workspace)
	tracker := NewFileTracker()

	refused := registry.RunWithOptions(context.Background(), "write_file", map[string]any{
		"path": outside, "content": "the complete generated body\n",
	}, grantedOpts(tracker))
	if refused.Status != StatusError || !strings.Contains(refused.Output, "generated content was SAVED") {
		t.Fatalf("outside-path failure must preserve the generated body, got status=%s output=%q", refused.Status, refused.Output)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside target was modified, stat err=%v", err)
	}

	retried := registry.RunWithOptions(context.Background(), "write_file", map[string]any{
		"path": "button.txt", "overwrite": true, "reuse_content": true,
	}, grantedOpts(tracker))
	if retried.Status != StatusOK {
		t.Fatalf("reuse retry failed: %q", retried.Output)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "button.txt")); err != nil || string(got) != "the complete generated body\n" {
		t.Fatalf("reused body = %q, err=%v", got, err)
	}
}

func TestWriteFileStaleBaselineIsNoOpWhenDiskAlreadyMatchesRequest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := safetyRegistry(t, dir)
	tracker := NewFileTracker()
	registry.RunWithOptions(context.Background(), "read_file", map[string]any{"path": "f.txt"}, grantedOpts(tracker))
	if err := os.WriteFile(path, []byte("v2 formatted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := registry.RunWithOptions(context.Background(), "write_file", map[string]any{
		"path": "f.txt", "content": "v2 formatted\n", "overwrite": true,
	}, grantedOpts(tracker))
	if res.Status != StatusOK || !strings.Contains(res.Output, "No changes needed") {
		t.Fatalf("identical stale write should be a no-op success, got status=%s output=%q", res.Status, res.Output)
	}
	if current, err := os.ReadFile(path); err != nil || tracker.CheckConflict(path, current) != nil {
		t.Fatalf("no-op success did not rebaseline current content: readErr=%v", err)
	}
}

type formatterShellTool struct{ path string }

func (tool formatterShellTool) Name() string        { return "format_shell" }
func (tool formatterShellTool) Description() string { return "format a tracked test file" }
func (tool formatterShellTool) Parameters() Schema {
	return Schema{Type: "object", AdditionalProperties: false}
}
func (tool formatterShellTool) Safety() Safety {
	return Safety{SideEffect: SideEffectShell, Permission: PermissionPrompt}
}
func (tool formatterShellTool) Run(context.Context, map[string]any) Result {
	if err := os.WriteFile(tool.path, []byte("formatted by command\n"), 0o644); err != nil {
		return errorResult(err.Error())
	}
	return okResult("formatted")
}

func TestSuccessfulShellMutationRefreshesTrackedBaseline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := safetyRegistry(t, dir)
	registry.Register(formatterShellTool{path: path})
	tracker := NewFileTracker()
	registry.RunWithOptions(context.Background(), "read_file", map[string]any{"path": "f.txt"}, grantedOpts(tracker))

	res := registry.RunWithOptions(context.Background(), "format_shell", map[string]any{}, grantedOpts(tracker))
	if res.Status != StatusOK {
		t.Fatalf("formatter shell failed: %q", res.Output)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := tracker.CheckConflict(path, current); err != nil {
		t.Fatalf("command-owned formatter change remained falsely stale: %v", err)
	}
}

func TestWriteFileOverwriteAllowedForUntrackedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := safetyRegistry(t, dir)
	tracker := NewFileTracker()

	// Never read through a tool → no baseline → overwrite is the model's call.
	res := registry.RunWithOptions(context.Background(), "write_file", map[string]any{
		"path": "f.txt", "content": "v3\n", "overwrite": true,
	}, grantedOpts(tracker))
	if res.Status != StatusOK {
		t.Fatalf("untracked overwrite should be allowed, got %q", res.Output)
	}
	if got, _ := os.ReadFile(path); string(got) != "v3\n" {
		t.Fatalf("file = %q, want v3", got)
	}
}

func TestWriteFileTrackedCleanOverwritesWithoutFlag(t *testing.T) {
	dir := t.TempDir()
	registry := safetyRegistry(t, dir)
	tracker := NewFileTracker()

	// The session creates the file, then rewrites it without overwrite: true.
	// Because the baseline matches disk, the rewrite proceeds instead of erroring.
	if res := registry.RunWithOptions(context.Background(), "write_file", map[string]any{
		"path": "f.txt", "content": "v1\n",
	}, grantedOpts(tracker)); res.Status != StatusOK {
		t.Fatalf("create failed: %s", res.Output)
	}
	res := registry.RunWithOptions(context.Background(), "write_file", map[string]any{
		"path": "f.txt", "content": "v2\n",
	}, grantedOpts(tracker))
	if res.Status != StatusOK {
		t.Fatalf("tracked-clean rewrite should be allowed without overwrite, got %q", res.Output)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "f.txt")); string(got) != "v2\n" {
		t.Fatalf("file = %q, want v2", got)
	}
}

func TestWriteFileRefusalStashesContentForReuse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := safetyRegistry(t, dir)
	tracker := NewFileTracker()

	// Untracked pre-existing file → refusal, but the refusal keeps the content
	// so the retry does not have to resend it.
	refused := registry.RunWithOptions(context.Background(), "write_file", map[string]any{
		"path": "f.txt", "content": "regenerated body\n",
	}, grantedOpts(tracker))
	if refused.Status != StatusError || !strings.Contains(refused.Output, "reuse_content") {
		t.Fatalf("expected refusal advertising reuse_content, got status=%s output=%q", refused.Status, refused.Output)
	}
	res := registry.RunWithOptions(context.Background(), "write_file", map[string]any{
		"path": "f.txt", "overwrite": true, "reuse_content": true,
	}, grantedOpts(tracker))
	if res.Status != StatusOK {
		t.Fatalf("reuse retry failed: %q", res.Output)
	}
	if got, _ := os.ReadFile(path); string(got) != "regenerated body\n" {
		t.Fatalf("file = %q, want stashed content", got)
	}

	// The stash is consumed: a second reuse attempt without content must error
	// clearly instead of writing an empty file.
	res = registry.RunWithOptions(context.Background(), "write_file", map[string]any{
		"path": "f.txt", "overwrite": true, "reuse_content": true,
	}, grantedOpts(tracker))
	if res.Status != StatusError || !strings.Contains(res.Output, "no stashed content") {
		t.Fatalf("expected no-stash error, got status=%s output=%q", res.Status, res.Output)
	}
}

func TestWriteFileMissingPathStashesContentForReuse(t *testing.T) {
	dir := t.TempDir()
	registry := safetyRegistry(t, dir)
	tracker := NewFileTracker()

	// Model generated the whole body but omitted path → the body must be saved
	// and the error must tell the model to retry with reuse_content.
	body := "<!DOCTYPE html>\n<html><body><canvas id=\"g\"></canvas><script>/* 700 lines of game */</script></body></html>\n"
	refused := registry.RunWithOptions(context.Background(), "write_file", map[string]any{
		"content": body,
	}, grantedOpts(tracker))
	if refused.Status != StatusError || !strings.Contains(refused.Output, "reuse_content") {
		t.Fatalf("expected refusal advertising reuse_content, got status=%s output=%q", refused.Status, refused.Output)
	}
	res := registry.RunWithOptions(context.Background(), "write_file", map[string]any{
		"path": "game.html", "overwrite": true, "reuse_content": true,
	}, grantedOpts(tracker))
	if res.Status != StatusOK {
		t.Fatalf("reuse retry failed: %q", res.Output)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "game.html")); string(got) != body {
		t.Fatalf("file = %q, want stashed content", got)
	}
}

func TestWriteAndEditUnaffectedWithoutTracker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := safetyRegistry(t, dir)

	// nil tracker (RunOptions without FileTracker) preserves the old behavior:
	// read, external change, then edit all proceed with no conflict gate.
	opts := RunOptions{PermissionGranted: true}
	registry.RunWithOptions(context.Background(), "read_file", map[string]any{"path": "f.txt"}, opts)
	if err := os.WriteFile(path, []byte("alpha changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := registry.RunWithOptions(context.Background(), "edit_file", map[string]any{
		"path": "f.txt", "old_string": "alpha changed", "new_string": "beta",
	}, opts); res.Status != StatusOK {
		t.Fatalf("without a tracker, edit should proceed, got %q", res.Output)
	}
}
