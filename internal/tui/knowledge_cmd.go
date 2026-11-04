package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/aistatine/sonut/internal/evolution"
	"github.com/aistatine/sonut/internal/insights"
	"github.com/aistatine/sonut/internal/knowledge"
)

// maybeCaptureKnowledge returns a background command that records the finished
// run (prompt → outcome) into the session's knowledge note. Skipped when the
// user ran /hide session, when there is no active session yet, or for
// synthetic auto-continue nudges (nonstop mode) that aren't real user asks.
func (m model) maybeCaptureKnowledge(finalAnswer string) tea.Cmd {
	if m.knowledgeHidden {
		return nil
	}
	sessionID := strings.TrimSpace(m.activeSession.SessionID)
	prompt := strings.TrimSpace(m.lastUserPrompt)
	if sessionID == "" || prompt == "" {
		return nil
	}
	if strings.Contains(prompt, "Keep going — do the next open task now.") {
		return nil
	}
	title := m.activeSession.Title
	outcome := strings.TrimSpace(finalAnswer)
	return func() tea.Msg {
		// Best-effort: knowledge capture must never disturb the session on failure.
		_ = knowledge.RecordSession(sessionID, title, prompt, outcome)
		_ = evolution.RecordRun(m.cwd, sessionID, prompt, outcome)
		// Privacy-preserving product insights ride the same gate (/hide stops
		// both). Only derived signals are uploaded — never prompt or outcome
		// text; see internal/insights for the trust contract.
		insights.RecordRun(prompt, outcome)
		return nil
	}
}

// handleHideCommand implements /hide:
//
//	/hide session — stop capturing this session into the knowledge graph and
//	                delete what was already captured for it
//	/hide off     — resume capturing
func (m model) handleHideCommand(argText string) (model, string) {
	switch strings.TrimSpace(strings.ToLower(argText)) {
	case "session", "":
		m.knowledgeHidden = true
		if id := strings.TrimSpace(m.activeSession.SessionID); id != "" {
			if err := knowledge.RemoveSessionNote(id); err != nil {
				return m, "Session hidden from the knowledge graph, but removing the existing note failed: " + err.Error()
			}
		}
		return m, "This session is now hidden from the knowledge graph — nothing about it will be captured (existing entries removed). /hide off re-enables capture."
	case "off":
		m.knowledgeHidden = false
		return m, "Knowledge capture re-enabled for this session."
	default:
		return m, "Usage: /hide session  (exclude this session from the knowledge graph)  |  /hide off"
	}
}
