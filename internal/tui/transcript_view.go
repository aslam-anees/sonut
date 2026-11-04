package tui

import "strings"

// toggleReasoningOrTranscript toggles the last reasoning block when one exists
// (either the live streaming block or the most recent committed rowReasoning row).
// Falls back to the detailed transcript toggle when there is nothing to toggle.
func (m model) toggleReasoningOrTranscript() model {
	// Live streaming reasoning takes priority.
	if m.streamingReasoning != "" {
		m.streamingReasoningExpanded = !m.streamingReasoningExpanded
		return m
	}
	// Find the last committed reasoning row and toggle it.
	for i := len(m.transcript) - 1; i >= 0; i-- {
		if m.transcript[i].kind == rowReasoning {
			m.transcript[i].expanded = !m.transcript[i].expanded
			return m
		}
	}
	// No reasoning block — fall back to the detailed transcript view.
	return m.toggleDetailedTranscript()
}

func (m model) toggleDetailedTranscript() model {
	m.transcriptDetailed = !m.transcriptDetailed
	m.clearSuggestions()
	if m.picker != nil && m.picker.kind == pickerTheme {
		// Dropping a theme picker mid-preview must undo the live palette, or the
		// previewed theme would stick while m.themeMode still holds the old one.
		m.restoreCommittedTheme()
	}
	m.picker = nil
	return m
}

func (m model) detailedTranscriptView() string {
	width := chatWidth(m.width)
	rc := buildRowContext(m.transcript)
	renderer := m
	renderer.pending = false
	renderer.activeRunID = 0

	var builder strings.Builder
	builder.WriteString(detailedTranscriptHeader(width))
	builder.WriteString("\n")
	builder.WriteString(sonutTheme.line.Render(strings.Repeat("-", width)))
	builder.WriteString("\n")

	shownAny := false
	var previousKind rowKind
	for _, row := range m.transcript {
		if rc.skip(row) {
			continue
		}
		rendered := ""
		if row.kind == rowWelcome {
			rendered = fitStyledLine(sonutTheme.faint.Render(row.text), width)
		} else {
			rendered = renderer.renderRowDetailed(row, width, rc)
		}
		if rendered == "" {
			continue
		}
		if shownAny && startsTurn(row.kind) {
			builder.WriteString("\n")
		}
		if shownAny && previousKind == rowUser && row.kind == rowReasoning {
			builder.WriteString("\n")
		}
		builder.WriteString(rendered)
		builder.WriteString("\n")
		shownAny = true
		previousKind = row.kind
	}

	if !shownAny {
		builder.WriteString(sonutTheme.faint.Render("No transcript rows."))
		builder.WriteString("\n")
	}

	builder.WriteString(sonutTheme.line.Render(strings.Repeat("-", width)))
	builder.WriteString("\n")
	builder.WriteString(fitStyledLine(sonutTheme.faint.Render("Esc close | Ctrl+O toggle"), width))
	return builder.String()
}

func detailedTranscriptHeader(width int) string {
	title := sonutTheme.ink.Bold(true).Render("Transcript")
	hint := sonutTheme.faint.Render("detailed")
	return fitStyledLine(joinHeaderLine(title, hint, width), width)
}
