package tools

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func streamingTestOptions(root, id string, granted bool) RunOptions {
	return RunOptions{
		PermissionGranted: granted,
		PermissionMode:    "auto",
		ToolCallID:        id,
		Cwd:               root,
	}
}

func TestStreamingWritePersistsStageBeforeCommit(t *testing.T) {
	root := t.TempDir()
	tool := NewWriteFileTool(root).(StreamingTool)
	session := tool.StartStreaming(context.Background(), streamingTestOptions(root, "write-1", true))
	if session == nil {
		t.Fatal("expected a streaming session")
	}
	session.Feed(`{"path":"app.js","content":"one\n`)
	stageMatches, err := filepath.Glob(filepath.Join(root, "app.js.sonut.stream.*"))
	if err != nil || len(stageMatches) != 1 {
		t.Fatalf("stage matches = %v, err = %v", stageMatches, err)
	}
	if _, err := os.Stat(filepath.Join(root, "app.js")); !os.IsNotExist(err) {
		t.Fatalf("target should remain unpublished during generation, stat err = %v", err)
	}
	staged, err := os.ReadFile(stageMatches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(staged) != "one\n" {
		t.Fatalf("staged prefix = %q, want %q", staged, "one\n")
	}

	session.Feed(`two\n"}`)
	commit, ok := session.PrepareCommit(context.Background(), `{"path":"app.js","content":"one\ntwo\n"}`)
	if !ok || commit == nil {
		t.Fatal("expected a prepared streaming write commit")
	}
	session.Close()
	result := commit()
	if result.Status != StatusOK {
		t.Fatalf("commit status = %s, output = %s", result.Status, result.Output)
	}
	content, err := os.ReadFile(filepath.Join(root, "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "one\ntwo\n" {
		t.Fatalf("committed content = %q", content)
	}
}

func TestStreamingWriteRejectsHTMLAtExtensionlessPath(t *testing.T) {
	root := t.TempDir()
	tool := NewWriteFileTool(root).(StreamingTool)
	session := tool.StartStreaming(context.Background(), streamingTestOptions(root, "write-extensionless-html", true))
	if session == nil {
		t.Fatal("expected a streaming session")
	}
	args := `{"path":"index","content":"<!doctype html>\n<html><body><canvas></canvas><script>console.log('ok')</script></body></html>\n"}`
	session.Feed(args)
	commit, ok := session.PrepareCommit(context.Background(), args)
	if !ok || commit == nil {
		t.Fatal("expected a precomputed validation failure commit")
	}
	session.Close()
	result := commit()
	if result.Status != StatusError {
		t.Fatalf("extensionless HTML stream status = %q, output = %q", result.Status, result.Output)
	}
	if _, err := os.Stat(filepath.Join(root, "index")); !os.IsNotExist(err) {
		t.Fatalf("extensionless target should not be created, stat error = %v", err)
	}
	recovery, err := filepath.Glob(filepath.Join(root, ".atcode", "recovery", "*.json"))
	if err != nil || len(recovery) == 0 {
		t.Fatalf("expected rejected body recovery, files = %v, err = %v", recovery, err)
	}
	retried := tool.(optionsAwareTool).RunWithOptions(context.Background(), map[string]any{
		"path":          "index.html",
		"overwrite":     true,
		"reuse_content": true,
	}, streamingTestOptions(root, "write-extensionless-html-retry", true))
	if retried.Status != StatusOK {
		t.Fatalf("corrected reuse retry status = %q, output = %q", retried.Status, retried.Output)
	}
	if _, err := os.Stat(filepath.Join(root, "index.html")); err != nil {
		t.Fatalf("corrected HTML target missing: %v", err)
	}
}

func TestStreamingWriteWaitsForCompletePathBeforeStaging(t *testing.T) {
	root := t.TempDir()
	body := "<!doctype html>\n<html><body><canvas id=\"game\"></canvas></body></html>\n"
	tool := NewWriteFileTool(root).(StreamingTool)
	options := streamingTestOptions(root, "write-fragmented-path", true)
	session := tool.StartStreaming(context.Background(), options)
	if session == nil {
		t.Fatal("expected a streaming session")
	}

	// The provider can split the path inside the JSON string. The first fragment
	// must not lock the target to the transient prefix "super".
	session.Feed(`{"path":"super`)
	session.Feed(`mario.html","content":` + strconv.Quote(body) + `}`)
	args := `{"path":"supermario.html","content":` + strconv.Quote(body) + `}`
	commit, ok := session.PrepareCommit(context.Background(), args)
	if !ok || commit == nil {
		t.Fatal("expected a prepared streaming write commit")
	}
	result := commit()
	if result.Status != StatusOK {
		t.Fatalf("commit status=%s output=%s", result.Status, result.Output)
	}
	if _, err := os.Stat(filepath.Join(root, "super")); !os.IsNotExist(err) {
		t.Fatalf("transient path prefix was published, stat error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "supermario.html")); err != nil {
		t.Fatalf("complete path was not published: %v", err)
	}
}

func TestStreamingRecoveryReturnsSingleLineBytesAfterInterruptedStream(t *testing.T) {
	root := t.TempDir()
	tool := NewWriteFileTool(root).(StreamingTool)
	session := tool.StartStreaming(context.Background(), streamingTestOptions(root, "recover-1", true))
	if session == nil {
		t.Fatal("expected a streaming session")
	}
	// No closing JSON quote/brace arrives, and there is no newline to use as a
	// line checkpoint. The stage itself must still preserve the exact bytes.
	session.Feed(`{"path":"single.js","content":"console.log('saved')`)
	session.Close()
	recovery, ok := session.Recovery()
	if !ok {
		t.Fatal("expected durable streaming recovery")
	}
	if recovery.Path != "single.js" || recovery.Content != "console.log('saved')" {
		t.Fatalf("recovery = %#v", recovery)
	}
	if _, err := os.Stat(recovery.StagePath); err != nil {
		t.Fatalf("staging file was not preserved: %v", err)
	}
}

func TestStreamingAppendAndEditPersistRuntimeStages(t *testing.T) {
	root := t.TempDir()
	appendPath := filepath.Join(root, "append.txt")
	if err := os.WriteFile(appendPath, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	appendTool := NewAppendFileTool(root).(StreamingTool)
	appendSession := appendTool.StartStreaming(context.Background(), streamingTestOptions(root, "append-1", true))
	appendSession.Feed(`{"path":"append.txt","content":"two\n`)
	stageMatches, _ := filepath.Glob(filepath.Join(root, "append.txt.sonut.stream.*"))
	if len(stageMatches) != 1 {
		t.Fatalf("append stage matches = %v", stageMatches)
	}
	stage, err := os.ReadFile(stageMatches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(stage) != "one\ntwo\n" {
		t.Fatalf("append stage = %q", stage)
	}
	appendSession.Feed(`three"}`)
	appendCommit, ok := appendSession.PrepareCommit(context.Background(), `{"path":"append.txt","content":"two\nthree"}`)
	if !ok {
		t.Fatal("expected append commit")
	}
	appendSession.Close()
	if result := appendCommit(); result.Status != StatusOK {
		t.Fatalf("append result = %#v", result)
	}

	editPath := filepath.Join(root, "edit.txt")
	if err := os.WriteFile(editPath, []byte("one\ntail\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	editTool := NewEditFileTool(root).(StreamingTool)
	editSession := editTool.StartStreaming(context.Background(), streamingTestOptions(root, "edit-1", true))
	editSession.Feed(`{"path":"edit.txt","old_string":"one","new_string":"T`)
	stageMatches, _ = filepath.Glob(filepath.Join(root, "edit.txt.sonut.stream.*"))
	if len(stageMatches) != 1 {
		t.Fatalf("edit stage matches = %v", stageMatches)
	}
	stage, err = os.ReadFile(stageMatches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(stage), "T") {
		t.Fatalf("edit stage did not receive the streamed replacement: %q", stage)
	}
	editSession.Feed(`WO"}`)
	editCommit, ok := editSession.PrepareCommit(context.Background(), `{"path":"edit.txt","old_string":"one","new_string":"TWO"}`)
	if !ok {
		t.Fatal("expected edit commit")
	}
	editSession.Close()
	if result := editCommit(); result.Status != StatusOK {
		t.Fatalf("edit result = %#v", result)
	}
	content, err := os.ReadFile(editPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "TWO\ntail\n" {
		t.Fatalf("edited content = %q", content)
	}
}

func TestStreamingSessionDoesNotStageWithoutPermission(t *testing.T) {
	root := t.TempDir()
	tool := NewWriteFileTool(root).(StreamingTool)
	if session := tool.StartStreaming(context.Background(), streamingTestOptions(root, "denied-1", false)); session != nil {
		t.Fatal("permission-gated stream should not start")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("permission-gated stream created files: %v", entries)
	}
}

func TestStreamingCommitRefusesExternalChange(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "append.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewAppendFileTool(root).(StreamingTool)
	session := tool.StartStreaming(context.Background(), streamingTestOptions(root, "conflict-1", true))
	session.Feed(`{"path":"append.txt","content":"two"}`)
	commit, ok := session.PrepareCommit(context.Background(), `{"path":"append.txt","content":"two"}`)
	if !ok {
		t.Fatal("expected prepared append commit")
	}
	session.Close()
	if err := os.WriteFile(path, []byte("external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := commit()
	if result.Status != StatusError || !strings.Contains(result.Output, "changed on disk") {
		t.Fatalf("conflict result = %#v", result)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "external\n" {
		t.Fatalf("conflict changed target to %q", content)
	}
}

func TestRegistryRebasesStaleStreamingEditAgainstCurrentFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "edit.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditFileTool(root).(StreamingTool)
	options := streamingTestOptions(root, "edit-rebase-1", true)
	session := tool.StartStreaming(context.Background(), options)
	session.Feed(`{"path":"edit.txt","old_string":"one","new_string":"ONE"}`)
	commit, ok := session.PrepareCommit(context.Background(), `{"path":"edit.txt","old_string":"one","new_string":"ONE"}`)
	if !ok || commit == nil {
		t.Fatal("expected prepared edit commit")
	}
	// Another mutation in the same assistant batch landed before this prepared
	// edit executes. The registry must drop only the stale prepared closure and
	// run the normal edit against the latest bytes.
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	registry.Register(NewEditFileTool(root))
	result := registry.RunWithOptions(context.Background(), "edit_file", map[string]any{
		"path": "edit.txt", "old_string": "one", "new_string": "ONE",
	}, RunOptions{PermissionGranted: true, PrecomputedCommit: commit})
	session.Close()
	if result.Status != StatusOK {
		t.Fatalf("stale prepared edit failed: %s", result.Output)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != "ONE\ntwo\nthree\n" {
		t.Fatalf("rebased edit = %q", updated)
	}
}
