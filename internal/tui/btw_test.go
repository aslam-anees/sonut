package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The "/btw done · press Enter to return" banner must be a real promise: an
// empty-composer Enter afterwards appends the return acknowledgment row.
func TestBtwDoneEnterReturnsToMainConversation(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.btwDone = true

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if cmd != nil {
		t.Fatal("expected the btw return Enter to be handled without starting an agent run")
	}
	if next.btwDone {
		t.Fatal("btwDone should clear after the return Enter")
	}
	if !strings.Contains(transcriptText(next.transcript), "back to the main conversation") {
		t.Fatal("expected the return acknowledgment row in the transcript")
	}
}

// Typing a message instead of pressing bare Enter also ends the aside.
func TestBtwDoneClearedBySubmit(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.btwDone = true
	m.input.SetValue("/help")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if next.btwDone {
		t.Fatal("btwDone should clear when the user submits a new input")
	}
	if strings.Contains(transcriptText(next.transcript), "back to the main conversation") {
		t.Fatal("a real submission should not print the return row")
	}
}
