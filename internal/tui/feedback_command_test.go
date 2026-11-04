package tui

import (
	"context"
	"errors"
	"testing"
)

func TestFeedbackSuggestionLeavesComposerReadyForMessage(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.input.SetValue("/feed")
	m.suggestions = []commandSuggestion{{Name: "/feedback", Desc: "Send feedback"}}
	m.commandPaletteOpen = true

	updated, cmd := m.chooseSuggestion()
	next := updated.(model)
	if cmd != nil {
		t.Fatal("selecting /feedback should wait for the user to type a message")
	}
	if got := next.composerValue(); got != "/feedback" {
		t.Fatalf("composer = %q, want /feedback", got)
	}
	if got := commandArgumentHintForInput(next.composerValue()); got != "[message]" {
		t.Fatalf("argument hint = %q, want [message]", got)
	}
}

func TestFeedbackCommandSubmitsWithoutStartingAgentRun(t *testing.T) {
	var submitted string
	m := newModel(context.Background(), Options{
		SubmitFeedback: func(_ context.Context, message string) error {
			submitted = message
			return nil
		},
	})

	updated, cmd := m.executeCommand(parseCommand("/feedback Please improve startup speed."))
	next := updated.(model)
	if cmd == nil {
		t.Fatal("expected asynchronous feedback submission")
	}
	if !transcriptContains(next.transcript, "status: sending") {
		t.Fatalf("missing sending state: %#v", next.transcript)
	}
	if next.pending {
		t.Fatal("/feedback must not start an agent/model run")
	}

	msg := cmd()
	final, _ := next.updateModel(msg)
	if submitted != "Please improve startup speed." {
		t.Fatalf("submitted = %q", submitted)
	}
	if !transcriptContains(final.(model).transcript, "status: received") {
		t.Fatalf("missing success state: %#v", final.(model).transcript)
	}
}

func TestFeedbackCommandSurfacesSubmissionFailure(t *testing.T) {
	m := newModel(context.Background(), Options{
		SubmitFeedback: func(context.Context, string) error {
			return errors.New("database unavailable")
		},
	})
	updated, cmd := m.executeCommand(parseCommand("/feedback It failed."))
	next := updated.(model)
	final, _ := next.updateModel(cmd())
	if !transcriptContains(final.(model).transcript, "status: not sent") {
		t.Fatalf("missing failure state: %#v", final.(model).transcript)
	}
}
