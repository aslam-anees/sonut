package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/aistatine/sonut/internal/sessions"
	"github.com/aistatine/sonut/internal/tools"
)

func TestResetTouchedFileRestoresOnlyRequestedFile(t *testing.T) {
	workspace := t.TempDir()
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{Cwd: workspace, Title: "reset test"})
	if err != nil {
		t.Fatal(err)
	}
	aPath := filepath.Join(workspace, "a.txt")
	bPath := filepath.Join(workspace, "b.txt")
	if err := os.WriteFile(aPath, []byte("a before"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("b before"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CaptureToolCheckpoint(session.SessionID, workspace, "edit_file", []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aPath, []byte("a after"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("b after"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := sidebarTestModel()
	m.cwd = workspace
	m.sessionStore = store
	m.activeSession = session
	m.transcript = append(m.transcript,
		transcriptRow{kind: rowToolResult, tool: "edit_file", status: tools.StatusOK, changedFiles: []string{"a.txt"}},
		transcriptRow{kind: rowToolResult, tool: "edit_file", status: tools.StatusOK, changedFiles: []string{"b.txt"}},
	)
	m.selectedFile = "a.txt"

	next, notice := m.resetTouchedFile("a.txt")
	if got, err := os.ReadFile(aPath); err != nil || string(got) != "a before" {
		t.Fatalf("requested file = %q, err=%v; want pre-session content", got, err)
	}
	if got, err := os.ReadFile(bPath); err != nil || string(got) != "b after" {
		t.Fatalf("unrelated file = %q, err=%v; want unchanged content", got, err)
	}
	if !next.resetFiles["a.txt"] || next.selectedFile != "" {
		t.Fatalf("reset state = markers=%v selected=%q", next.resetFiles, next.selectedFile)
	}
	if len(next.touchedFiles()) != 1 || next.touchedFiles()[0].path != "b.txt" {
		t.Fatalf("FILES roster after reset = %+v, want only b.txt", next.touchedFiles())
	}
	if !strings.Contains(notice, "Reset a.txt") {
		t.Fatalf("reset notice = %q", notice)
	}

	events, err := store.ReadEvents(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[len(events)-1].Type != sessions.EventFileReset {
		t.Fatalf("last session event = %+v, want file_reset", events)
	}
}

func TestFileViewRKeyResetsFile(t *testing.T) {
	workspace := t.TempDir()
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{Cwd: workspace, Title: "reset key test"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "app.go")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CaptureToolCheckpoint(session.SessionID, workspace, "edit_file", []string{"app.go"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := sidebarTestModel()
	m.cwd = workspace
	m.sessionStore = store
	m.activeSession = session
	m.transcript = append(m.transcript, transcriptRow{
		kind: rowToolResult, tool: "edit_file", status: tools.StatusOK, changedFiles: []string{"app.go"},
	})
	m = m.openFileView("app.go")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	next := updated.(model)
	if got, err := os.ReadFile(path); err != nil || string(got) != "before\n" {
		t.Fatalf("R reset content = %q, err=%v", got, err)
	}
	if !next.resetFiles["app.go"] || next.fileView.mode != fileViewFull {
		t.Fatalf("R reset state = markers=%v fileView=%+v", next.resetFiles, next.fileView)
	}
}

func TestResetTouchedFileRemovesGitOnlyCreatedFile(t *testing.T) {
	workspace := t.TempDir()
	cmd := exec.Command("git", "init", "-q", workspace)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, output)
	}
	path := filepath.Join(workspace, "generated.txt")
	if err := os.WriteFile(path, []byte("generated"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := sidebarTestModel()
	m.ctx = context.Background()
	m.cwd = workspace
	m.sessionStore = nil
	m.gitTouched = []gitSweepFile{{path: "generated.txt", created: true}}
	next, notice := m.resetTouchedFile("generated.txt")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("git-only created file should be removed, stat error = %v", err)
	}
	if len(next.gitTouched) != 0 || !next.resetFiles["generated.txt"] {
		t.Fatalf("git-only reset state = touched=%v reset=%v", next.gitTouched, next.resetFiles)
	}
	if !strings.Contains(notice, "Reset generated.txt") {
		t.Fatalf("git-only reset notice = %q", notice)
	}
}
