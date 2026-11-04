package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aistatine/sonut/internal/sec"
	"github.com/aistatine/sonut/internal/sonutruntime"
)

type secCommandProvider struct{}

func (secCommandProvider) StreamCompletion(context.Context, sonutruntime.CompletionRequest) (<-chan sonutruntime.StreamEvent, error) {
	events := make(chan sonutruntime.StreamEvent, 2)
	events <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: "Fix the privileged workload first [abc]."}
	events <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}
	close(events)
	return events, nil
}

func TestSecCommandIsDiscoverable(t *testing.T) {
	command, ok := resolveCommand("/sec")
	if !ok || command.kind != commandSec {
		t.Fatalf("/sec resolved to ok=%v command=%#v", ok, command)
	}
	if command.group != commandGroupTools {
		t.Fatalf("/sec group = %q, want tools", command.group)
	}
	if !commandSuggestionNamesContain(matchCommandSuggestions("/se"), "/sec") {
		t.Fatal("/se autocomplete should surface /sec")
	}
	matches := matchCommandSuggestions("/sec")
	if len(matches) != 1 || matches[0].Name != "/sec" {
		t.Fatalf("exact /sec input must have one unambiguous match, got %#v", matches)
	}
	if got := parseCommand("/security tools"); got.kind != commandSec || got.name != "/sec" || got.text != "tools" {
		t.Fatalf("retired /security spelling must invisibly redirect to /sec, got %#v", got)
	}
}

func TestSecCommandScansWritesAndOpensNativeView(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "docker-compose.yml")
	if err := os.WriteFile(config, []byte("services:\n  api:\n    host: 0.0.0.0\n    privileged: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m, cmd := (model{cwd: root}).startSecCommand("")
	if cmd == nil {
		t.Fatal("local scan should run as an async command")
	}
	m = completeSecScan(t, m, cmd)
	if !m.secView.active {
		t.Fatal("/sec should open the native security view")
	}
	if m.browserHeadful {
		t.Fatal("/sec should not open or mark a browser")
	}
	if _, err := os.Stat(filepath.Join(root, ".atcode", "sec", "report.json")); err != nil {
		t.Fatalf("report artifact missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".atcode", "sec", "dashboard.html")); !os.IsNotExist(err) {
		t.Fatalf("/sec should not emit an HTML dashboard: %v", err)
	}
	view := ansiPlain(m.renderSecView(100))
	for _, want := range []string{"Grade", "critical", "verified path", "Fix first", "Coverage", "Egress", "Agent", "Signals"} {
		if !strings.Contains(view, want) {
			t.Fatalf("native /sec view missing %q:\n%s", want, view)
		}
	}
	narrow := m.renderSecView(42)
	for _, line := range strings.Split(narrow, "\n") {
		if width := ansi.StringWidth(line); width > 40 {
			t.Fatalf("narrow /sec line width = %d, want <= 40: %q", width, ansi.Strip(line))
		}
	}
}

func TestSecHelpExplainsTrustBoundary(t *testing.T) {
	m, _ := (model{cwd: t.TempDir()}).startSecCommand("help")
	text := m.transcript[len(m.transcript)-1].text
	for _, want := range []string{"local only", "does not run project code", "does not store detected secret values", "bounded redacted report", "native TUI", "live session egress", "behavior triage"} {
		if !strings.Contains(text, want) {
			t.Fatalf("/sec help missing %q:\n%s", want, text)
		}
	}
}

func TestSecDirectActivityAndToolsOpenUnifiedTabs(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		want secTab
	}{
		{arg: "activity", want: secTabActivity},
		{arg: "atlas", want: secTabAtlas},
		{arg: "map", want: secTabAtlas},
		{arg: "tools", want: secTabSurfaces},
		{arg: "signals", want: secTabSignals},
		{arg: "routes", want: secTabRoutes},
	} {
		t.Run(tc.arg, func(t *testing.T) {
			m, cmd := (model{cwd: t.TempDir()}).startSecCommand(tc.arg)
			if cmd == nil {
				t.Fatal("local unified security view should run as an async command")
			}
			m = completeSecScan(t, m, cmd)
			if !m.secView.active || m.secView.tab != tc.want {
				t.Fatalf("/sec %s opened active=%v tab=%d, want tab=%d", tc.arg, m.secView.active, m.secView.tab, tc.want)
			}
		})
	}
}

func TestSecProcessInsightIsBoundedAndInteractive(t *testing.T) {
	m, cmd := (model{cwd: t.TempDir()}).startSecCommand("processes")
	if cmd == nil {
		t.Fatal("process insight should run asynchronously")
	}
	msg := cmd()
	result, ok := msg.(secProcessResultMsg)
	if !ok {
		t.Fatalf("process command returned %T, want secProcessResultMsg", msg)
	}
	m = m.applySecProcessResult(result)
	if len(m.transcript) == 0 || !strings.Contains(ansiPlain(m.transcript[len(m.transcript)-1].text), "Trust boundary") {
		t.Fatalf("process insight did not render a bounded result: %#v", m.transcript)
	}
}

func TestSecRoutesViewIsInteractive(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "docker-compose.yml"), []byte(`services:
  api:
    labels:
      proxy.aliases: api.example.test
      proxy.#1.port: "8080"
      proxy.#1.scheme: https
`), 0o644); err != nil {
		t.Fatal(err)
	}

	m, cmd := (model{cwd: root}).startSecCommand("routes")
	m = completeSecScan(t, m, cmd)
	view := ansiPlain(m.renderSecView(100))
	for _, want := range []string{"Configured routes", "api.example.test → api", "api", "CONFIGURED-PUBLIC"} {
		if !strings.Contains(view, want) {
			t.Fatalf("routes view missing %q:\n%s", want, view)
		}
	}
	next, _, handled := m.handleSecViewKey(testKey(tea.KeyEnter))
	if !handled || !next.secView.expanded {
		t.Fatalf("Enter should expand route details: handled=%v expanded=%v", handled, next.secView.expanded)
	}
	detail := ansiPlain(next.renderSecView(100))
	for _, want := range []string{"Policy", "TLS", "Reachability is not established"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("expanded routes view missing %q:\n%s", want, detail)
		}
	}
}

func TestSecNativeViewNavigationAndAdvisor(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "docker-compose.yml"), []byte("privileged: true\nhost: 0.0.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, cmd := (model{cwd: root, provider: secCommandProvider{}}).startSecCommand("")
	// The initial scan is intentionally asynchronous so the transition can
	// render before filesystem work starts.
	if !m.secView.transitioning {
		t.Fatal("initial /sec scan should enter the loading transition")
	}
	m = completeSecScan(t, m, cmd)
	if m.secView.advisor == nil {
		t.Fatal("configured model should enable the in-process advisor")
	}

	m, _, handled := m.handleSecViewKey(testKey(tea.KeyTab))
	if !handled || m.secView.tab != secTabFindings {
		t.Fatalf("Tab should move to risks: handled=%v tab=%d", handled, m.secView.tab)
	}
	m, _, _ = m.handleSecViewKey(testKey(tea.KeyEnter))
	if !m.secView.expanded {
		t.Fatal("Enter should expand selected risk evidence")
	}
	m = m.setSecTab(secTabAdvisor)
	m.input.SetValue("What should I fix first?")
	m.resetComposerFromInput()
	next, cmd, handled := m.handleSecViewKey(testKey(tea.KeyEnter))
	if !handled || cmd == nil || !next.secView.advisorAsking {
		t.Fatalf("advisor submit did not start: handled=%v asking=%v", handled, next.secView.advisorAsking)
	}
	result := cmd()
	answer, ok := result.(secAdvisorResultMsg)
	if !ok {
		t.Fatalf("advisor command returned %T", result)
	}
	next = next.applySecAdvisorResult(answer)
	if next.secView.advisorAsking || len(next.secView.advisorMessages) != 2 {
		t.Fatalf("advisor result was not applied: %#v", next.secView)
	}
}

func TestSecScanShowsFullPageDotTransition(t *testing.T) {
	m, cmd := (model{cwd: t.TempDir(), width: 72, height: 14}).startSecCommand("")
	if cmd == nil || !m.secView.active || !m.secView.transitioning {
		t.Fatalf("/sec should enter an active loading transition: active=%v transitioning=%v cmd=%v", m.secView.active, m.secView.transitioning, cmd != nil)
	}

	first := ansiPlain(m.transcriptView())
	if !strings.Contains(first, "SECURITY POSTURE") || !strings.Contains(first, "Scanning workspace") || !strings.Contains(first, "·") {
		t.Fatalf("loading transition is missing its full-page content:\n%s", first)
	}
	if got := len(strings.Split(first, "\n")); got != m.height {
		t.Fatalf("transition rendered %d lines, want terminal height %d", got, m.height)
	}

	next, _ := m.Update(secTransitionTickMsg{seq: m.secView.transitionSeq})
	updated := next.(model)
	if updated.secView.transitionFrame != 1 {
		t.Fatalf("transition frame = %d, want 1", updated.secView.transitionFrame)
	}
	if ansiPlain(updated.transcriptView()) == first {
		t.Fatal("dot transition did not change after a valid animation tick")
	}
}

func TestSecScanIgnoresLateResultAfterClose(t *testing.T) {
	m, _ := (model{cwd: t.TempDir()}).startSecCommand("")
	seq := m.secView.transitionSeq
	m = m.closeSecView()
	next, _ := m.Update(secScanResultMsg{seq: seq, report: sec.Report{Version: sec.ReportVersion}, reportPath: "stale.json"})
	updated := next.(model)
	if updated.secView.active {
		t.Fatal("late scan result reopened /sec after it was closed")
	}
}

func ansiPlain(value string) string {
	return ansi.Strip(value)
}

func completeSecScan(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	msg := execSecCmd(cmd)
	result, ok := msg.(secScanResultMsg)
	if !ok {
		t.Fatalf("scan command returned %T, want secScanResultMsg", msg)
	}
	next, _ := m.Update(result)
	updated, ok := next.(model)
	if !ok {
		t.Fatalf("scan update returned %T, want model", next)
	}
	return updated
}

func execSecCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if inner := execSecCmd(sub); inner != nil {
				if _, isTick := inner.(secTransitionTickMsg); !isTick {
					return inner
				}
			}
		}
		return nil
	}
	return msg
}
