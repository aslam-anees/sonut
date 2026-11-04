package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/aistatine/sonut/internal/sessions"
)

func (m model) queueMessage(text string) model {
	text = strings.TrimSpace(text)
	if text == "" {
		return m
	}
	if existing := strings.TrimSpace(m.queuedMessage); existing != "" {
		// Match Grok Build's queue promotion: consecutive plain prompts are
		// combined into one next-turn request instead of silently replacing the
		// earlier prompt.
		m.queuedMessage = existing + "\n\n" + text
	} else {
		m.queuedMessage = text
	}
	m.rememberInput(text)
	m.clearComposer()
	m.clearSuggestions()
	return m
}

func (m model) clearQueuedMessage() model {
	m.queuedMessage = ""
	return m
}

func (m model) hasQueuedMessage() bool {
	return strings.TrimSpace(m.queuedMessage) != ""
}

func queuedMessageCount(message string) int {
	message = strings.TrimSpace(message)
	if message == "" {
		return 0
	}
	return len(strings.Split(message, "\n\n"))
}

// steerQueuedMessage pushes the queued prompt INTO the live run: the agent
// loop drains the steer inbox at its next turn boundary and treats the text
// as the user's newest, highest-priority instruction — the model reacts
// mid-run instead of after the run ends. Triggered by pressing Enter on an
// empty composer while a prompt is queued.
func (m model) steerQueuedMessage() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.queuedMessage)
	if text == "" || m.steerInbox == nil || !m.pending {
		return m, nil
	}
	m.queuedMessage = ""
	m.steerInbox.Push(text)
	// Show the message as a normal user turn right away, and record it so a
	// resumed session replays the conversation in the order the model saw it.
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendUser, text: text})
	if m.activeSession.SessionID != "" {
		var err error
		m, err = m.appendSessionEvent(sessions.EventMessage, map[string]any{
			"role":    "user",
			"content": text,
		})
		if err != nil {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{
				kind: actionAppendError,
				text: "session record error: " + err.Error(),
			})
		}
	}
	m.transcript = reduceTranscript(m.transcript, transcriptAction{
		kind: actionAppendSystem,
		text: "↳ sent into the live run — @code will pick it up at the next step",
	})
	return m, nil
}

func renderQueuedMessagePreview(message string, width int) string {
	message = strings.Join(strings.Fields(message), " ")
	if message == "" {
		return ""
	}
	label := "[queued]"
	if count := queuedMessageCount(message); count > 1 {
		label = fmt.Sprintf("[queued %d]", count)
	}
	line := sonutTheme.accent.Render(label) + " " + sonutTheme.muted.Render(message) +
		" " + sonutTheme.faint.Render("· Enter queues · empty Enter sends now")
	return fitStyledLine(line, width)
}
