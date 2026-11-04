package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestVimModeToggle(t *testing.T) {
	m := newTestModel()

	// Initially disabled
	if m.vimMode.enabled {
		t.Fatal("vim mode should start disabled")
	}

	// Enable via /vim on
	m = m.handleVimCommand("on")
	if !m.vimMode.enabled {
		t.Fatal("vim mode should be enabled after /vim on")
	}
	if m.vimMode.mode != vimModeINSERT {
		t.Fatalf("vim mode should start in INSERT, got %v", m.vimMode.mode)
	}

	// Disable via /vim off
	m = m.handleVimCommand("off")
	if m.vimMode.enabled {
		t.Fatal("vim mode should be disabled after /vim off")
	}

	// Toggle via bare /vim
	m = m.handleVimCommand("")
	if !m.vimMode.enabled {
		t.Fatal("bare /vim should toggle to enabled")
	}

	m = m.handleVimCommand("")
	if m.vimMode.enabled {
		t.Fatal("bare /vim should toggle to disabled")
	}
}

func TestVimModeEscapeToNormal(t *testing.T) {
	m := newTestModel()
	m.vimMode.enabled = true
	m.vimMode.mode = vimModeINSERT
	m.composer = composerState{text: "hello", cursor: 3}
	m.composerActive = true

	// Esc should switch to NORMAL mode
	msg := testKey(tea.KeyEsc)
	handled := (&m).applyVimKey(msg)
	if !handled {
		t.Fatal("Esc in INSERT mode should be handled by vim")
	}
	if m.vimMode.mode != vimModeNORMAL {
		t.Fatalf("Esc should switch to NORMAL mode, got %v", m.vimMode.mode)
	}
}

func TestVimModeInsertTransitions(t *testing.T) {
	m := newTestModel()
	m.vimMode.enabled = true
	m.vimMode.mode = vimModeNORMAL
	m.composer = composerState{text: "hello", cursor: 0}
	m.composerActive = true

	// Test 'i' key: enter insert at cursor
	msg := testKeyText("i")
	handled := (&m).applyVimKey(msg)
	if !handled {
		t.Fatal("'i' in NORMAL mode should be handled by vim")
	}
	if m.vimMode.mode != vimModeINSERT {
		t.Fatalf("'i' should switch to INSERT mode, got %v", m.vimMode.mode)
	}

	// Test 'a' key: enter insert after cursor
	m.vimMode.mode = vimModeNORMAL
	m.composer = composerState{text: "hello", cursor: 2}
	msg = testKeyText("a")
	handled = (&m).applyVimKey(msg)
	if !handled {
		t.Fatal("'a' in NORMAL mode should be handled by vim")
	}
	if m.vimMode.mode != vimModeINSERT {
		t.Fatal("'a' should switch to INSERT mode")
	}
	if m.composer.cursor != 3 {
		t.Fatalf("'a' should move cursor right, got %d want 3", m.composer.cursor)
	}

	// Test 'A' key: enter insert at end of line
	m.vimMode.mode = vimModeNORMAL
	m.composer = composerState{text: "hello\nworld", cursor: 0}
	msg = testKeyText("A")
	handled = (&m).applyVimKey(msg)
	if !handled {
		t.Fatal("'A' in NORMAL mode should be handled by vim")
	}
	if m.composer.cursor != 5 {
		t.Fatalf("'A' should move to end of line, got cursor %d want 5", m.composer.cursor)
	}

	// Test 'I' key: enter insert at start of line
	m.vimMode.mode = vimModeNORMAL
	m.composer = composerState{text: "hello\nworld", cursor: 8}
	msg = testKeyText("I")
	handled = (&m).applyVimKey(msg)
	if !handled {
		t.Fatal("'I' in NORMAL mode should be handled by vim")
	}
	if m.composer.cursor != 6 {
		t.Fatalf("'I' should move to start of line, got cursor %d want 6", m.composer.cursor)
	}
}

func TestVimMotions(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		startCursor int
		key         string
		wantCursor  int
	}{
		{
			name:        "h moves left",
			text:        "hello",
			startCursor: 3,
			key:         "h",
			wantCursor:  2,
		},
		{
			name:        "h at start stays at start",
			text:        "hello",
			startCursor: 0,
			key:         "h",
			wantCursor:  0,
		},
		{
			name:        "l moves right",
			text:        "hello",
			startCursor: 2,
			key:         "l",
			wantCursor:  3,
		},
		{
			name:        "l at end stays at end",
			text:        "hello",
			startCursor: 5,
			key:         "l",
			wantCursor:  5,
		},
		{
			name:        "0 moves to line start",
			text:        "hello\nworld",
			startCursor: 8,
			key:         "0",
			wantCursor:  6,
		},
		{
			name:        "$ moves to line end",
			text:        "hello\nworld",
			startCursor: 6,
			key:         "$",
			wantCursor:  11,
		},
		{
			name:        "G moves to buffer end",
			text:        "hello\nworld",
			startCursor: 0,
			key:         "G",
			wantCursor:  11,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel()
			m.vimMode.enabled = true
			m.vimMode.mode = vimModeNORMAL
			m.composer = composerState{text: tt.text, cursor: tt.startCursor}
			m.composerActive = true

			msg := testKeyText(tt.key)
			handled := (&m).applyVimKey(msg)
			if !handled && tt.key != "w" && tt.key != "b" {
				t.Fatalf("motion '%s' should be handled by vim", tt.key)
			}
			if m.composer.cursor != tt.wantCursor {
				t.Errorf("cursor after '%s': got %d, want %d", tt.key, m.composer.cursor, tt.wantCursor)
			}
		})
	}
}

func TestVimDeleteOperations(t *testing.T) {
	m := newTestModel()
	m.vimMode.enabled = true
	m.vimMode.mode = vimModeNORMAL
	m.composer = composerState{text: "hello", cursor: 2}
	m.composerActive = true

	// Test 'x': delete character at cursor
	msg := testKeyText("x")
	handled := (&m).applyVimKey(msg)
	if !handled {
		t.Fatal("'x' should be handled by vim")
	}
	if m.composer.text != "helo" {
		t.Errorf("after 'x' delete: got %q want %q", m.composer.text, "helo")
	}

	// Test 'D': delete to end of line
	m.composer = composerState{text: "hello\nworld", cursor: 1}
	msg = testKeyText("D")
	handled = (&m).applyVimKey(msg)
	if !handled {
		t.Fatal("'D' should be handled by vim")
	}
	if m.composer.text != "h\nworld" {
		t.Errorf("after 'D' delete: got %q want %q", m.composer.text, "h\nworld")
	}
}

func TestVimDisabledNoOp(t *testing.T) {
	m := newTestModel()
	m.vimMode.enabled = false
	m.composer = composerState{text: "hello", cursor: 2}
	m.composerActive = true

	// With vim disabled, applyVimKey should return false for all keys
	msg := testKeyText("h")
	handled := (&m).applyVimKey(msg)
	if handled {
		t.Fatal("vim key should not be handled when vim mode is disabled")
	}
}

func TestVimStatusIndicator(t *testing.T) {
	m := newTestModel()

	// Disabled
	if m.vimStatusIndicator() != "" {
		t.Error("vim status should be empty when disabled")
	}

	// INSERT mode
	m.vimMode.enabled = true
	m.vimMode.mode = vimModeINSERT
	if m.vimStatusIndicator() != "-- INSERT --" {
		t.Errorf("vim status: got %q want %q", m.vimStatusIndicator(), "-- INSERT --")
	}

	// NORMAL mode
	m.vimMode.mode = vimModeNORMAL
	if m.vimStatusIndicator() != "-- NORMAL --" {
		t.Errorf("vim status: got %q want %q", m.vimStatusIndicator(), "-- NORMAL --")
	}
}

func TestVimDeleteLineDD(t *testing.T) {
	m := newTestModel()
	m.vimMode.enabled = true
	m.vimMode.mode = vimModeNORMAL
	m.composer = composerState{text: "one\ntwo\nthree", cursor: 5} // in "two"
	m.composerActive = true

	// First 'd' buffers the operator.
	if !(&m).applyVimKey(testKeyText("d")) {
		t.Fatal("first 'd' should be handled")
	}
	if m.vimMode.pendingKey != 'd' {
		t.Fatalf("pendingKey after first 'd' = %q, want 'd'", m.vimMode.pendingKey)
	}
	if m.composer.text != "one\ntwo\nthree" {
		t.Fatal("first 'd' must not modify text")
	}
	// Second 'd' deletes the line including its newline.
	if !(&m).applyVimKey(testKeyText("d")) {
		t.Fatal("second 'd' should be handled")
	}
	if m.composer.text != "one\nthree" {
		t.Errorf("after dd: got %q want %q", m.composer.text, "one\nthree")
	}
	if m.vimMode.pendingKey != 0 {
		t.Error("pendingKey should be cleared after dd")
	}

	// dd on the last line takes the preceding newline.
	m.composer = composerState{text: "one\ntwo", cursor: 6}
	(&m).applyVimKey(testKeyText("d"))
	(&m).applyVimKey(testKeyText("d"))
	if m.composer.text != "one" {
		t.Errorf("dd on last line: got %q want %q", m.composer.text, "one")
	}
}

func TestVimGotoStartGG(t *testing.T) {
	m := newTestModel()
	m.vimMode.enabled = true
	m.vimMode.mode = vimModeNORMAL
	m.composer = composerState{text: "one\ntwo", cursor: 6}
	m.composerActive = true

	// Single 'g' buffers, cursor unmoved.
	if !(&m).applyVimKey(testKeyText("g")) {
		t.Fatal("first 'g' should be handled")
	}
	if m.composer.cursor != 6 {
		t.Fatal("single 'g' must not move the cursor")
	}
	// Second 'g' jumps to buffer start.
	if !(&m).applyVimKey(testKeyText("g")) {
		t.Fatal("second 'g' should be handled")
	}
	if m.composer.cursor != 0 {
		t.Errorf("gg: cursor = %d, want 0", m.composer.cursor)
	}

	// A non-'g' key after 'g' cancels the sequence (and is handled normally).
	m.composer = composerState{text: "one\ntwo", cursor: 6}
	(&m).applyVimKey(testKeyText("g"))
	(&m).applyVimKey(testKeyText("h"))
	if m.vimMode.pendingKey != 0 {
		t.Error("pendingKey should clear on a cancelling key")
	}
	if m.composer.cursor != 5 {
		t.Errorf("cancelling 'h' should still move left: cursor = %d, want 5", m.composer.cursor)
	}
}

func TestVimNormalSwallowsUnhandledPrintables(t *testing.T) {
	m := newTestModel()
	m.vimMode.enabled = true
	m.vimMode.mode = vimModeNORMAL
	m.composer = composerState{text: "hello", cursor: 2}
	m.composerActive = true

	for _, key := range []string{"q", "z", "5", "!"} {
		if !(&m).applyVimKey(testKeyText(key)) {
			t.Errorf("NORMAL mode should swallow unhandled printable %q", key)
		}
	}
	if m.composer.text != "hello" {
		t.Errorf("swallowed keys must not type: got %q", m.composer.text)
	}
}

func TestVimNormalEnterFallsThrough(t *testing.T) {
	m := newTestModel()
	m.vimMode.enabled = true
	m.vimMode.mode = vimModeNORMAL
	m.composer = composerState{text: "hello", cursor: 2}
	m.composerActive = true

	if (&m).applyVimKey(testKey(tea.KeyEnter)) {
		t.Fatal("Enter in NORMAL mode must fall through so submit still works")
	}
}

func TestVimEscInPlainComposerEntersNormal(t *testing.T) {
	// Full Update-path test: Esc with no modal up must reach vim and switch
	// INSERT -> NORMAL instead of being eaten by the run-cancel fallback.
	m := newModel(context.Background(), Options{})
	m.width = 96
	m.vimMode.enabled = true
	m.vimMode.mode = vimModeINSERT
	m.composer = composerState{text: "draft", cursor: 3}
	m.composerActive = true

	updated, _ := m.Update(testKey(tea.KeyEsc))
	next := updated.(model)
	if next.vimMode.mode != vimModeNORMAL {
		t.Fatalf("Esc in plain composer should enter NORMAL, got %v", next.vimMode.mode)
	}
	if next.composerValue() != "draft" {
		t.Errorf("Esc into NORMAL must not clear the draft: got %q", next.composerValue())
	}
}

// newTestModel creates a minimal model for testing.
func newTestModel() model {
	return model{
		vimMode: vimModeState{
			enabled: false,
			mode:    vimModeINSERT,
		},
		composerActive: true,
	}
}
