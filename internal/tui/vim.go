package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// vimModeState tracks vim editing mode state for the composer.
type vimModeState struct {
	enabled bool    // true when vim mode is active
	mode    vimMode // NORMAL or INSERT
	// pendingKey buffers the first key of a two-key NORMAL-mode sequence
	// ('d' for dd, 'g' for gg). Cleared by any key that doesn't complete it.
	pendingKey byte
}

// vimMode represents the current vim editing mode.
type vimMode int

const (
	vimModeINSERT vimMode = iota
	vimModeNORMAL
)

// String returns a human-readable mode name.
func (m vimMode) String() string {
	switch m {
	case vimModeNORMAL:
		return "NORMAL"
	case vimModeINSERT:
		return "INSERT"
	default:
		return "UNKNOWN"
	}
}

// handleVimCommand toggles vim mode on/off and displays status.
func (m model) handleVimCommand(args string) model {
	args = strings.TrimSpace(strings.ToLower(args))
	switch args {
	case "on", "1", "true":
		m.vimMode.enabled = true
		m.vimMode.mode = vimModeINSERT
	case "off", "0", "false":
		m.vimMode.enabled = false
		m.vimMode.mode = vimModeINSERT
	case "", "toggle":
		// bare /vim toggles
		m.vimMode.enabled = !m.vimMode.enabled
		m.vimMode.mode = vimModeINSERT
	default:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendSystem,
			text: "Vim\nUsage: /vim [on|off] — toggle vim-style editing mode",
		})
		return m
	}

	status := "off"
	if m.vimMode.enabled {
		status = "on"
	}
	text := strings.Join([]string{
		"Vim mode",
		"status: " + status,
		"When enabled: press Esc to enter NORMAL mode, i/a/A/I/o to enter INSERT mode.",
		"Normal-mode motions: hjkl (move), 0 (line start), $ (line end), w/b (word),",
		"x (delete char), dd (delete line), D (delete to end), gg/G (start/end of buffer).",
		"Enter still submits in NORMAL mode.",
	}, "\n")
	m.transcript = reduceTranscript(m.transcript, transcriptAction{
		kind: actionAppendSystem,
		text: text,
	})
	return m
}

// applyVimKey processes a key press in vim mode, returning whether the key was
// handled by vim (true) or should fall through to normal composer handling (false).
// If handled, it mutates the composer state and/or mode as needed.
func (m *model) applyVimKey(msg tea.KeyMsg) bool {
	if !m.vimMode.enabled {
		return false
	}

	// Esc always switches to NORMAL mode (or stays in NORMAL).
	if keyIs(msg, tea.KeyEsc) {
		m.vimMode.mode = vimModeNORMAL
		m.vimMode.pendingKey = 0
		return true
	}

	// In INSERT mode, most keys just type normally (fall through).
	// Only Esc switches mode.
	if m.vimMode.mode == vimModeINSERT {
		// i, a, A, I, o are only special when pressed to ENTER insert mode;
		// once in INSERT mode, they type as normal characters.
		// (The entry keys are handled below in NORMAL mode.)
		return false // let the key fall through to normal composition
	}

	// NORMAL mode: Enter and modifier chords fall through so submit and global
	// shortcuts keep working; everything else is vim's to handle or swallow.
	if keyIs(msg, tea.KeyEnter) || keyHasMod(msg, tea.ModCtrl) || keyAlt(msg) {
		m.vimMode.pendingKey = 0
		return false
	}

	key := keyText(msg)

	// Two-key sequences (dd, gg): the first press buffered the operator; this
	// press either completes it or cancels it (and is then handled normally).
	if pending := m.vimMode.pendingKey; pending != 0 {
		m.vimMode.pendingKey = 0
		switch {
		case pending == 'd' && key == "d":
			// dd: delete the current line including its trailing newline.
			state := m.currentComposerState()
			runes := []rune(state.text)
			start := state.cursor
			for start > 0 && runes[start-1] != '\n' {
				start--
			}
			end := state.cursor
			for end < len(runes) && runes[end] != '\n' {
				end++
			}
			if end < len(runes) {
				end++ // take the newline with the line
			} else if start > 0 {
				start-- // last line: take the preceding newline instead
			}
			m.setComposerState(deleteComposerRange(state, start, end))
			return true
		case pending == 'g' && key == "g":
			// gg: go to start of buffer.
			state := m.currentComposerState()
			state.cursor = 0
			m.setComposerState(state)
			return true
		}
		// Sequence cancelled: fall through and treat this key normally.
	}

	switch key {
	case "i":
		m.vimMode.mode = vimModeINSERT
		return true
	case "a":
		// a: append (move cursor right, then insert)
		state := m.currentComposerState()
		runes := []rune(state.text)
		if state.cursor < len(runes) {
			state.cursor++
		}
		m.setComposerState(state)
		m.vimMode.mode = vimModeINSERT
		return true
	case "A":
		// A: append at end of line
		state := m.currentComposerState()
		runes := []rune(state.text)
		// Find end of current line
		pos := state.cursor
		for pos < len(runes) && runes[pos] != '\n' {
			pos++
		}
		state.cursor = pos
		m.setComposerState(state)
		m.vimMode.mode = vimModeINSERT
		return true
	case "I":
		// I: insert at beginning of line
		state := m.currentComposerState()
		runes := []rune(state.text)
		// Find start of current line
		pos := state.cursor
		for pos > 0 && runes[pos-1] != '\n' {
			pos--
		}
		state.cursor = pos
		m.setComposerState(state)
		m.vimMode.mode = vimModeINSERT
		return true
	case "o":
		// o: open new line below and enter insert
		state := m.currentComposerState()
		runes := []rune(state.text)
		// Find end of current line
		pos := state.cursor
		for pos < len(runes) && runes[pos] != '\n' {
			pos++
		}
		// Move cursor to end of line before inserting newline
		state.cursor = pos
		// Insert newline at end of line
		state = insertComposerText(state, "\n")
		m.setComposerState(state)
		m.vimMode.mode = vimModeINSERT
		return true
	case "h":
		// Move cursor left
		state := m.currentComposerState()
		if state.cursor > 0 {
			state.cursor--
		}
		m.setComposerState(state)
		return true
	case "l":
		// Move cursor right
		state := m.currentComposerState()
		runes := []rune(state.text)
		if state.cursor < len(runes) {
			state.cursor++
		}
		m.setComposerState(state)
		return true
	case "j":
		// Move cursor down (to next line)
		state := m.currentComposerState()
		runes := []rune(state.text)
		moveVimDown(&state, runes)
		m.setComposerState(state)
		return true
	case "k":
		// Move cursor up (to previous line)
		state := m.currentComposerState()
		runes := []rune(state.text)
		moveVimUp(&state, runes)
		m.setComposerState(state)
		return true
	case "0":
		// Move to beginning of line
		state := m.currentComposerState()
		runes := []rune(state.text)
		pos := state.cursor
		for pos > 0 && runes[pos-1] != '\n' {
			pos--
		}
		state.cursor = pos
		m.setComposerState(state)
		return true
	case "$":
		// Move to end of line
		state := m.currentComposerState()
		runes := []rune(state.text)
		pos := state.cursor
		for pos < len(runes) && runes[pos] != '\n' {
			pos++
		}
		state.cursor = pos
		m.setComposerState(state)
		return true
	case "w":
		// Move to next word start
		state := m.currentComposerState()
		state = moveComposerWordAfter(state)
		m.setComposerState(state)
		return true
	case "b":
		// Move to previous word start
		state := m.currentComposerState()
		state = moveComposerWordBefore(state)
		m.setComposerState(state)
		return true
	case "g":
		// First half of gg: buffer it; a second 'g' jumps to buffer start.
		m.vimMode.pendingKey = 'g'
		return true
	case "G":
		// Move to end of buffer
		state := m.currentComposerState()
		runes := []rune(state.text)
		state.cursor = len(runes)
		m.setComposerState(state)
		return true
	case "x":
		// Delete character at cursor
		state := m.currentComposerState()
		runes := []rune(state.text)
		if state.cursor < len(runes) {
			state = deleteComposerRange(state, state.cursor, state.cursor+1)
		}
		m.setComposerState(state)
		return true
	case "d":
		// First half of dd: buffer it; a second 'd' deletes the current line.
		m.vimMode.pendingKey = 'd'
		return true
	case "D":
		// Delete from cursor to end of line
		state := m.currentComposerState()
		runes := []rune(state.text)
		pos := state.cursor
		for pos < len(runes) && runes[pos] != '\n' {
			pos++
		}
		state = deleteComposerRange(state, state.cursor, pos)
		m.setComposerState(state)
		return true
	}

	// NORMAL mode swallows any unhandled printable key so stray presses never
	// type into the composer; non-printable keys (arrows, page keys, etc.)
	// fall through to their normal handlers.
	return keyPrintable(msg)
}

// moveVimDown moves the cursor down to the next line, preserving column position when possible.
func moveVimDown(state *composerState, runes []rune) {
	// Find the start of the current line
	lineStart := state.cursor
	for lineStart > 0 && runes[lineStart-1] != '\n' {
		lineStart--
	}
	// Find the end of the current line
	lineEnd := state.cursor
	for lineEnd < len(runes) && runes[lineEnd] != '\n' {
		lineEnd++
	}
	// Calculate column position within the line
	col := state.cursor - lineStart
	// Move to next line
	if lineEnd < len(runes) { // there is a next line
		nextLineStart := lineEnd + 1
		nextLineEnd := nextLineStart
		for nextLineEnd < len(runes) && runes[nextLineEnd] != '\n' {
			nextLineEnd++
		}
		// Position cursor at the same column in the next line (or end of line if shorter)
		nextCol := col
		if nextCol > (nextLineEnd - nextLineStart) {
			nextCol = nextLineEnd - nextLineStart
		}
		state.cursor = nextLineStart + nextCol
	}
}

// moveVimUp moves the cursor up to the previous line, preserving column position when possible.
func moveVimUp(state *composerState, runes []rune) {
	// Find the start of the current line
	lineStart := state.cursor
	for lineStart > 0 && runes[lineStart-1] != '\n' {
		lineStart--
	}
	// Calculate column position within the line
	col := state.cursor - lineStart
	// Move to previous line
	if lineStart > 0 { // there is a previous line
		// lineStart-1 is the newline of the previous line
		prevLineEnd := lineStart - 1
		prevLineStart := prevLineEnd
		for prevLineStart > 0 && runes[prevLineStart-1] != '\n' {
			prevLineStart--
		}
		// Position cursor at the same column in the previous line (or end of line if shorter)
		prevCol := col
		if prevCol > (prevLineEnd - prevLineStart) {
			prevCol = prevLineEnd - prevLineStart
		}
		state.cursor = prevLineStart + prevCol
	}
}

// vimStatusIndicator returns the vim mode indicator for display in the status line.
func (m model) vimStatusIndicator() string {
	if !m.vimMode.enabled {
		return ""
	}
	switch m.vimMode.mode {
	case vimModeNORMAL:
		return "-- NORMAL --"
	case vimModeINSERT:
		return "-- INSERT --"
	default:
		return ""
	}
}
