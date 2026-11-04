package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/aistatine/sonut/internal/voice"
)

func readyVoiceModel(t *testing.T) model {
	t.Helper()
	m := newModel(context.Background(), Options{})
	m.voice.ready = true
	m.voice.state = voice.StateListening
	m.voice.utteranceID = "u-old"
	m.input.SetValue("stable ")
	m.input.CursorEnd()
	m.voice.transcript = voice.NewReplaceableTranscript(m.currentComposerSnapshot())
	m.setComposerSnapshot(m.voice.transcript.Update("partial"))
	return m
}

func TestVoiceManualTypingDuringListeningPreservesKeyAndStableText(t *testing.T) {
	m := readyVoiceModel(t)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	next := updated.(model)
	if got := next.composerValue(); got != "stable x" {
		t.Fatalf("composer = %q, want stable x", got)
	}
	if next.voice.state != voice.StateIdle || next.voice.utteranceID != "" {
		t.Fatalf("voice state after typing = %s/%q", next.voice.state, next.voice.utteranceID)
	}
	if next.voice.transcript != nil {
		t.Fatal("partial transcript was not removed")
	}
}

func TestVoiceManualTypingInvalidatesFinalizationAndStalePartial(t *testing.T) {
	m := readyVoiceModel(t)
	m.voice.state = voice.StateFinalizing
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	next := updated.(model)
	if got := next.composerValue(); got != "stable y" {
		t.Fatalf("composer = %q, want stable y", got)
	}
	updated, _ = next.Update(voiceEventMsg{event: voice.Event{
		Type: voice.EventASRPartial, UtteranceID: "u-old", Text: "stale overwrite",
	}})
	next = updated.(model)
	if got := next.composerValue(); got != "stable y" {
		t.Fatalf("stale partial overwrote manual text: %q", got)
	}
}

func TestVoiceStaleStateCannotReenterVoice(t *testing.T) {
	m := readyVoiceModel(t)
	m.voice.state = voice.StateIdle
	m.voice.utteranceID = "u-current"
	updated, _ := m.Update(voiceEventMsg{event: voice.Event{
		Type:        voice.EventState,
		UtteranceID: "u-old",
		State:       string(voice.StateListening),
	}})
	next := updated.(model)
	if next.voice.state != voice.StateIdle {
		t.Fatalf("stale state changed voice state to %s", next.voice.state)
	}
}

func TestVoiceManualTypingDuringSpeakingStopsSpeechMode(t *testing.T) {
	m := readyVoiceModel(t)
	m.voice.state = voice.StateSpeaking
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	next := updated.(model)
	if next.voice.state != voice.StateIdle || next.voice.utteranceID != "" {
		t.Fatalf("voice state after backspace = %s/%q", next.voice.state, next.voice.utteranceID)
	}
}

func TestVoiceSpaceFallbackInsertsExactlyOneSpace(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.voice.ready = true
	m.voice.config.Enabled = true
	m.voice.state = voice.StateIdle
	updated, _ := m.Update(testKey(tea.KeySpace))
	next := updated.(model)
	if got := next.composerValue(); got != " " {
		t.Fatalf("ordinary Space inserted %q, want one space", got)
	}
}

func TestVoiceBuilderConfirmationDoesNotToggleApplicationBuildOverlay(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.voice.ready = true
	m.voice.config.Enabled = true
	m.voice.utteranceID = "u-build"
	m.voice.pending = &voiceBuildConfirmation{Prompt: "edit the file", Language: "en", UtteranceID: "u-build", PlannerPrompt: "edit the file"}
	updated, _ := m.Update(testKeyText("n"))
	next := updated.(model)
	if next.buildMode {
		t.Fatal("voice confirmation toggled the application-builder overlay")
	}
	if next.voice.pending != nil {
		t.Fatal("negative confirmation did not close")
	}
}
