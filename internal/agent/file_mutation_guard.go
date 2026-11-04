package agent

import (
	"path/filepath"
	"strings"

	"github.com/aistatine/sonut/internal/tools"
)

// referencesExistingWorkspaceFile lets the loop remove whole-file write
// schemas before generation begins for the usual "fix @button.html" shape.
// This is intentionally lexical and cheap: it only checks file-looking tokens
// already named by the user or planner, never scans the repository.
func referencesExistingWorkspaceFile(cwd, text string, tracker *tools.FileTracker) bool {
	for _, raw := range strings.Fields(text) {
		token := strings.Trim(raw, "`\"'()[]{}<>.,:;!?")
		token = strings.TrimPrefix(token, "@")
		if token == "" || filepath.Ext(token) == "" {
			continue
		}
		if workspaceFileExists(cwd, token) || workspaceFileExists(cwd, filepath.Base(token)) {
			return true
		}
	}
	if tracker != nil {
		lower := strings.ToLower(text)
		for _, path := range tracker.TrackedPaths() {
			base := strings.ToLower(filepath.Base(path))
			if base != "" && base != "." && strings.Contains(lower, base) {
				return true
			}
		}
	}
	return false
}
