package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestBareVoiceCommandOpensDedicatedView(t *testing.T) {
	m := newModel(context.Background(), Options{})
	before := len(m.transcript)
	updated, cmd := m.executeCommand(parseCommand("/voice"))
	next := updated.(model)
	if cmd != nil {
		t.Fatal("bare /voice should not start setup or another command")
	}
	if !next.voiceView.active || next.voiceView.tab != voiceViewSettings {
		t.Fatalf("voice view = %#v, want active Settings view", next.voiceView)
	}
	if len(next.transcript) != before {
		t.Fatal("bare /voice added a status row to the main transcript")
	}
	if !strings.Contains(next.transcriptView(), "[Settings]") {
		t.Fatal("dedicated voice page did not render its Settings tab")
	}
}

func TestVoiceSetupStaysInsideDedicatedView(t *testing.T) {
	m := newModel(context.Background(), Options{UserConfigPath: t.TempDir() + "/config.json"})
	updated, cmd := m.executeCommand(parseCommand("/voice setup"))
	next := updated.(model)
	if cmd == nil {
		t.Fatal("/voice setup should start the installer command")
	}
	if !next.voiceView.active || next.voiceView.tab != voiceViewSetup {
		t.Fatalf("voice setup view = %#v, want active Setup view", next.voiceView)
	}
	if len(next.transcript) != len(m.transcript) {
		t.Fatal("voice setup added installer prose to the main transcript")
	}
	if strings.Contains(next.voiceView.notice, "download:") || strings.Contains(next.voiceView.notice, "licenses:") {
		t.Fatal("installer summary leaked into the view notice instead of structured setup content")
	}
	if !strings.Contains(next.renderVoiceView(100), "Artifacts") {
		t.Fatal("Setup tab did not render artifact details")
	}
	// The command was not executed, so there is no installer goroutine to cancel.
}

func TestVoiceSettingsAreAbsentFromMainStatusLine(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.voice.ready = true
	status := m.statusLine(120)
	if strings.Contains(status, "Voice") {
		t.Fatalf("main status line advertises voice: %q", status)
	}
}

func TestVoiceViewHasReliableExitAndCommandEscapeHatches(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.voiceView.active = true
	updated, _, handled := m.handleVoiceViewKey(testKey(tea.KeyEsc))
	if !handled || updated.voiceView.active {
		t.Fatalf("Esc exit active=%v handled=%v; want closed", updated.voiceView.active, handled)
	}

	m.voiceView.active = true
	updated, _, handled = m.handleVoiceViewKey(testKeyText("/"))
	if handled || updated.voiceView.active {
		t.Fatalf("slash escape active=%v handled=%v; want closed and passed through", updated.voiceView.active, handled)
	}

	m = newModel(context.Background(), Options{})
	m.voiceView.active = true
	modelAfterSlash, _ := m.Update(testKeyText("/"))
	next := modelAfterSlash.(model)
	if next.voiceView.active || next.composerValue() != "/" {
		t.Fatalf("slash through Update active=%v composer=%q; want main composer with slash", next.voiceView.active, next.composerValue())
	}
}

func TestVoiceViewTabsAreMouseNavigable(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.width = 120
	m.voiceView.active = true
	updated, _, handled := m.handleVoiceViewMouse(testMouseClick(tea.MouseLeft, 115, 0))
	if !handled || updated.voiceView.tab != voiceViewDoctor {
		t.Fatalf("tab click = %d handled=%v; want Doctor", updated.voiceView.tab, handled)
	}
	updated, _, handled = updated.handleVoiceViewMouse(testMouseClick(tea.MouseLeft, 2, 0))
	if !handled || updated.voiceView.active {
		t.Fatalf("back click active=%v handled=%v; want closed", updated.voiceView.active, handled)
	}
}

func TestVoiceViewSettingsControlsPersist(t *testing.T) {
	m := newModel(context.Background(), Options{UserConfigPath: t.TempDir() + "/config.json"})
	m.voiceView.active = true
	m.voiceView.tab = voiceViewSettings
	updated, _, handled := m.handleVoiceViewKey(testKey(tea.KeyDown))
	if !handled || updated.voiceView.selected != voiceSettingLanguage {
		t.Fatalf("settings selection = %d, handled=%v; want language row", updated.voiceView.selected, handled)
	}
	updated, _, handled = updated.handleVoiceViewKey(testKey(tea.KeyEnter))
	if !handled || updated.voice.config.Language != "en" {
		t.Fatalf("Enter language control = %q, handled=%v; want en", updated.voice.config.Language, handled)
	}
	updated.voiceView.selected = voiceSettingSpeed
	updated, _, handled = updated.handleVoiceViewKey(testKey(tea.KeyRight))
	if !handled || updated.voice.config.Speed <= 1.05 {
		t.Fatalf("right-arrow speed control = %.2f, handled=%v; want increment", updated.voice.config.Speed, handled)
	}
	updated.voiceView.selected = voiceSettingResponseVoice
	updated, _, handled = updated.handleVoiceViewKey(testKeyText("v"))
	if !handled || updated.voice.config.ResponseVoice != "F2" {
		t.Fatalf("voice cycle = %q, handled=%v; want F2", updated.voice.config.ResponseVoice, handled)
	}
	updated, _, handled = updated.handleVoiceViewKey(testKeyText("+"))
	if !handled || updated.voice.config.Speed <= 1.05 {
		t.Fatalf("speed control = %.2f, handled=%v; want increment", updated.voice.config.Speed, handled)
	}
	updated, _, handled = updated.handleVoiceViewKey(testKeyText("k"))
	if !handled || updated.voice.config.Shortcut != "ctrl+space" {
		t.Fatalf("shortcut cycle = %q, handled=%v; want ctrl+space", updated.voice.config.Shortcut, handled)
	}
}
