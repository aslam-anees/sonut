package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aistatine/sonut/internal/sessions"
)

const fileResetTimeout = 8 * time.Second

// resetTouchedFile restores one FILES entry to the state it had before the
// session first changed it. File-tool edits use durable checkpoint blobs;
// shell/subagent edits use the git working tree because those tools cannot
// declare their mutation targets ahead of time.
func (m model) resetTouchedFile(path string) (model, string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return m, "Reset file\nno file selected."
	}
	if m.pending {
		return m, "Reset file\ncannot reset while a run is in progress."
	}
	if m.resetFiles[path] {
		return m, "Reset file\n" + path + " is already reset."
	}

	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, fileResetTimeout)
	defer cancel()

	report := sessions.RestoreReport{TargetSequence: -1}
	var err error
	if m.sessionStore != nil && m.activeSession.SessionID != "" {
		report, err = m.sessionStore.RestoreFilesToInitialState(m.activeSession.SessionID, m.cwd, []string{path})
	}
	if errors.Is(err, sessions.ErrNoCheckpoint) || m.sessionStore == nil || m.activeSession.SessionID == "" {
		if !m.gitTouchedPath(path) {
			return m, "Reset file\nno reversible checkpoint for " + path + "."
		}
		err = resetGitTouchedFile(ctx, m.cwd, path)
		report = sessions.RestoreReport{TargetSequence: -1}
	}
	if err != nil {
		return m, "Reset file\n" + path + ": " + err.Error()
	}
	if len(report.Skipped) > 0 {
		return m, "Reset file\n" + path + " could not be fully restored: " + strings.Join(report.Skipped, ", ")
	}

	if m.resetFiles == nil {
		m.resetFiles = map[string]bool{}
	}
	m.resetFiles[path] = true
	m = m.removeGitTouchedPath(path)
	if m.selectedFile == path {
		m.selectedFile = ""
	}
	if m.fileView.active && m.fileView.path == path {
		// Keep the inspector open so the user can immediately verify the restored
		// file. The diff mode is now intentionally empty, so show the file itself.
		m.fileView.mode = fileViewFull
		m.chatScrollOffset = 0
		m = m.clearHover()
	}

	marker := sessions.FileResetMarker{Path: path, Report: report}
	var markerErr error
	if m.activeSession.SessionID != "" && m.sessionStore != nil {
		m, markerErr = m.appendSessionEvent(sessions.EventFileReset, marker)
	}
	summary := fmt.Sprintf("Reset %s\n%d file restored, %d file deleted.", path, report.FilesRestored, report.FilesDeleted)
	if markerErr != nil {
		summary += "\nWarning: the file was restored, but the reset marker could not be saved: " + markerErr.Error()
	}
	return m, summary
}

func (m model) gitTouchedPath(path string) bool {
	for _, file := range m.gitTouched {
		if file.path == path {
			return true
		}
	}
	return false
}

func (m model) removeGitTouchedPath(path string) model {
	if len(m.gitTouched) == 0 {
		return m
	}
	kept := m.gitTouched[:0]
	for _, file := range m.gitTouched {
		if file.path != path {
			kept = append(kept, file)
		}
	}
	m.gitTouched = kept
	return m
}

// resetFilesFromSessionEvents reconstructs the current reset markers during
// resume. A later tool result reactivates that file, so a new edit is visible
// again without requiring a new session.
func resetFilesFromSessionEvents(events []sessions.Event) map[string]bool {
	reset := map[string]bool{}
	for _, event := range events {
		switch event.Type {
		case sessions.EventFileReset:
			path := payloadString(sessionPayload(event), "path")
			if path != "" {
				reset[path] = true
			}
		case sessions.EventToolResult:
			for _, path := range payloadStringSlice(sessionPayload(event), "changedFiles") {
				delete(reset, path)
			}
		}
	}
	return reset
}

// clearFileResetForRow reactivates a file when a later live mutation lands.
func (m model) clearFileResetForRow(row transcriptRow) model {
	if row.kind != rowToolResult || len(row.changedFiles) == 0 || len(m.resetFiles) == 0 {
		return m
	}
	for _, path := range row.changedFiles {
		delete(m.resetFiles, path)
	}
	return m
}

// resetGitTouchedFile restores a tracked path from HEAD or removes a newly
// created untracked path. The path comes from git status and is passed as a
// separate argument, never interpolated into a shell command.
func resetGitTouchedFile(ctx context.Context, cwd, path string) error {
	status, err := defaultPRCommandRunner(ctx, cwd, "git", "status", "--porcelain", "--untracked-files=all", "--", path)
	if err != nil {
		return fmt.Errorf("inspect git status: %w", err)
	}
	if strings.TrimSpace(status) == "" {
		return nil // another sweep or action already made it clean
	}
	created := false
	for _, file := range parseGitPorcelain(status) {
		if file.path == path {
			created = file.created
			break
		}
	}
	if created {
		// A shell can stage a newly created path. `git clean` only removes
		// untracked paths, so first drop any index entry; --ignore-unmatch keeps
		// the ordinary ?? case harmless.
		if _, err := defaultPRCommandRunner(ctx, cwd, "git", "rm", "--cached", "--ignore-unmatch", "-r", "--", path); err != nil {
			return fmt.Errorf("untrack created file: %w", err)
		}
		if _, err := defaultPRCommandRunner(ctx, cwd, "git", "clean", "-f", "--", path); err != nil {
			return fmt.Errorf("remove created file: %w", err)
		}
		return nil
	}
	if _, err := defaultPRCommandRunner(ctx, cwd, "git", "restore", "--source=HEAD", "--staged", "--worktree", "--", path); err != nil {
		return fmt.Errorf("restore from HEAD: %w", err)
	}
	return nil
}
