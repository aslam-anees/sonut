package tui

import (
	"context"
	"strings"
	"testing"
)

func TestIntegrateCommandStoresSecretAndTurnsGreen(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.cwd = t.TempDir()

	next := m.handleIntegrateCommand("supabase sk-test-123")
	last := next.transcript[len(next.transcript)-1]
	if !strings.Contains(last.text, "supabase: configured ●") {
		t.Fatalf("card = %q", last.text)
	}
	if got := loadIntegrations(m.cwd)["supabase"]; got != "sk-test-123" {
		t.Fatalf("stored secret = %q", got)
	}
	if names := configuredIntegrationNames(m.cwd); len(names) != 1 || names[0] != "supabase" {
		t.Fatalf("configured = %v", names)
	}

	// The section shows green for supabase, grey for the rest.
	section := integrationsSection(m.cwd)
	if !strings.Contains(section, "● supabase") || !strings.Contains(section, "○ oauth") {
		t.Fatalf("section = %q", section)
	}

	// "off" removes it.
	next = next.handleIntegrateCommand("supabase off")
	if len(configuredIntegrationNames(m.cwd)) != 0 {
		t.Fatal("secret not removed")
	}

	// Unknown names warn instead of storing.
	next = next.handleIntegrateCommand("stripe sk")
	last = next.transcript[len(next.transcript)-1]
	if !strings.Contains(last.text, "Unknown integration") {
		t.Fatalf("card = %q", last.text)
	}
}
