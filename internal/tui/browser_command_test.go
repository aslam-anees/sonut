package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/localcontrol"
)

func TestHandleBrowserCommandTogglesHeadfulMode(t *testing.T) {
	// Force known start state (desktop default is headful).
	defer localcontrol.SetBrowserHeadful(false)
	localcontrol.SetBrowserHeadful(false)

	restarted := 0
	prevRestart := restartBrowserSession
	restartBrowserSession = func(context.Context) error { restarted++; return nil }
	defer func() { restartBrowserSession = prevRestart }()

	m := &model{}

	if text := m.handleBrowserCommand(""); !strings.Contains(text, "headless") {
		t.Fatalf("default status = %q, want it to report headless", text)
	}

	if text := m.handleBrowserCommand("show"); strings.Contains(text, "unavailable") {
		// Gating may legitimately deny headful in a headless CI sandbox; only
		// assert the toggle took effect when it was actually allowed.
		if m.browserHeadful {
			t.Fatalf("browserHeadful=true but response reported unavailable: %q", text)
		}
	} else {
		if !m.browserHeadful || !localcontrol.BrowserHeadful() {
			t.Fatalf("expected headful mode enabled after /browser show, got model=%v pkg=%v", m.browserHeadful, localcontrol.BrowserHeadful())
		}
		if len(m.browserState.actions) == 0 {
			t.Fatalf("expected the toggle to be logged in the ACTIONS log")
		}
		if restarted == 0 {
			t.Fatalf("expected /browser show to restart the browser session so the mode change applies")
		}
	}

	text := m.handleBrowserCommand("hide")
	if m.browserHeadful || localcontrol.BrowserHeadful() {
		t.Fatalf("expected headless mode restored after /browser hide, got model=%v pkg=%v", m.browserHeadful, localcontrol.BrowserHeadful())
	}
	if !strings.Contains(text, "Headless mode restored") {
		t.Fatalf("hide response = %q", text)
	}
}

func TestHandleBrowserCommandRejectsUnknownArg(t *testing.T) {
	m := &model{}
	text := m.handleBrowserCommand("bogus")
	if !strings.Contains(text, "Usage:") {
		t.Fatalf("unknown-arg response = %q, want usage hint", text)
	}
}
