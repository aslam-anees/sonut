package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sessionsHub is the hub note every session note links to, so the graph shows
// all sessions clustered around one node.
const sessionsHub = "sessions"

// sessionNoteName returns the stable note slug for a session.
func sessionNoteName(sessionID string) string {
	id := Slugify(sessionID)
	if len(id) > 12 {
		id = id[:12]
	}
	if id == "" {
		id = "unknown"
	}
	return "session-" + id
}

// RecordSession appends one completed-run entry to the session's knowledge
// note, creating it (and the sessions hub) on first write. It is called
// automatically after every successful run so the knowledge graph grows
// without the user doing anything; /hide session disables it per session.
func RecordSession(sessionID, title, prompt, outcome string) error {
	dir, err := EnsureDir()
	if err != nil {
		return err
	}

	hub := filepath.Join(dir, sessionsHub+".md")
	if _, err := os.Stat(hub); os.IsNotExist(err) {
		hubBody := "# Sessions\n\nEvery @code session records what was asked and what was done here. #session\n"
		if err := os.WriteFile(hub, []byte(hubBody), 0o644); err != nil {
			return err
		}
	}

	name := sessionNoteName(sessionID)
	path := filepath.Join(dir, name+".md")
	entry := fmt.Sprintf("- **%s** — %s\n  ↳ %s\n",
		time.Now().Format("2006-01-02 15:04"),
		clampLine(prompt, 220),
		clampLine(outcome, 300))

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if strings.TrimSpace(title) == "" {
			title = "Session " + strings.TrimPrefix(name, "session-")
		}
		body := fmt.Sprintf("# %s\n\nPart of [[%s]]. #session\n\n%s", strings.TrimSpace(title), sessionsHub, entry)
		return os.WriteFile(path, []byte(body), 0o644)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(entry)
	return err
}

// RemoveSessionNote deletes a session's knowledge note (used by /hide session
// so already-captured entries disappear too).
func RemoveSessionNote(sessionID string) error {
	path := filepath.Join(Dir(), sessionNoteName(sessionID)+".md")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// clampLine flattens text to a single line and truncates it for note entries.
func clampLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	if s == "" {
		s = "(no text)"
	}
	return s
}
