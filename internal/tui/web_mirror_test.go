package tui

import (
	"context"
	"strings"
	"testing"
)

func TestWebCommandRegistered(t *testing.T) {
	command, ok := resolveCommand("/share")
	if !ok || command.kind != commandShare {
		t.Fatalf("resolveCommand(/share) = %+v, %v", command, ok)
	}
}

func TestHandleWebPromptQueuesWhileRunning(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.pending = true
	next, cmd := m.handleWebPrompt("do the thing")
	if cmd != nil {
		t.Fatalf("queued prompt must not start a run")
	}
	if next.queuedMessage != "do the thing" {
		t.Fatalf("queuedMessage = %q", next.queuedMessage)
	}
}

func TestHandleWebPromptAppendsUserRow(t *testing.T) {
	m := newModel(context.Background(), Options{})
	next, _ := m.handleWebPrompt("hello from phone")
	found := false
	for _, row := range next.transcript {
		if row.kind == rowUser && row.text == "hello from phone" {
			found = true
		}
	}
	if !found {
		t.Fatal("remote prompt did not land in the transcript as a user row")
	}
}

func TestWebCommandWithoutSinkFailsClosed(t *testing.T) {
	m := newModel(context.Background(), Options{}) // no RuntimeMessageSink
	next := m.handleWebCommand("on")
	if next.webMirror != nil {
		t.Fatal("mirror must not start without a runtime message sink (remote input would be dead)")
	}
	last := next.transcript[len(next.transcript)-1]
	if !strings.Contains(last.text, "error") {
		t.Fatalf("expected error card, got %q", last.text)
	}
}

func TestMirrorEventsIncludeStreaming(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendUser, text: "hi"})
	m.streamingText = "partial answer"
	events := m.mirrorEvents()
	if len(events) == 0 {
		t.Fatal("no events")
	}
	lastEvent := events[len(events)-1]
	if lastEvent.Kind != "assistant" || !lastEvent.Live || lastEvent.Text != "partial answer" {
		t.Fatalf("streaming event = %+v", lastEvent)
	}
}
