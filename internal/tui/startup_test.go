package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestStartupWithoutHostedAuthContinuesLocally(t *testing.T) {
	t.Setenv("SUPABASE_URL", "")
	t.Setenv("SUPABASE_ANON_KEY", "")
	m := startupModel{}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("local startup must advance without hosted sign-in")
	}
	if _, ok := cmd().(skipLoginMsg); !ok {
		t.Fatal("unconfigured hosted authentication must select local startup")
	}
}

func TestStartupOffersLocalChoiceAlongsideHostedLogin(t *testing.T) {
	m := startupModel{phase: phaseLogin, loginMode: loginModeChoose}
	if view := plainRender(t, m.viewLogin(100)); !strings.Contains(view, "Continue locally") {
		t.Fatal("hosted login screen must offer an account-free local path")
	}
	next, cmd := m.handleKey(tea.KeyPressMsg(tea.Key{Code: 'l', Text: "l"}))
	got := next.(startupModel)
	if !got.skip || got.phase != phaseModelCheck || cmd == nil {
		t.Fatal("local choice must proceed to installed model detection")
	}
}

func TestEmptyStateShowsBrandAndTaglineOnly(t *testing.T) {
	m := newModel(context.Background(), Options{ProviderName: "anthropic", ModelName: "claude-sonnet-4.5"})
	m.width, m.height = 100, 30

	view := plainRender(t, m.View())
	assertContains(t, view, "██████╗ ██████╗ ██████╗ ███████╗")
	assertContains(t, view, emptyStateTagline)
	assertNotContains(t, view, "running @code against ")
	assertNotContains(t, view, "add a --version flag")
	assertNotContains(t, view, "explain internal/agent/loop.go")
	assertNotContains(t, view, "fix the failing test in internal/tools")
}

func TestEmptyStateWordmarkShimmerIsDeterministic(t *testing.T) {
	first := strings.Join(sonutWordmarkLinesForPhase(4, true), "\n")
	second := strings.Join(sonutWordmarkLinesForPhase(4, true), "\n")
	if first != second {
		t.Fatal("same ambient phase rendered different wordmark frames")
	}
	if first == strings.Join(sonutWordmarkLinesForPhase(5, true), "\n") {
		t.Fatal("adjacent ambient phases should move the wordmark shimmer")
	}
	if static := strings.Join(sonutWordmarkLinesForPhase(5, false), "\n"); static != strings.Join(sonutWordmarkLines(), "\n") {
		t.Fatal("disabled animation should render the static core wordmark")
	}
}

func TestModelDownloadViewShowsProgressWithoutFileSize(t *testing.T) {
	m := startupModel{
		downloadPercent: 42,
		downloadDone:    2 * 1024 * 1024 * 1024,
		downloadTotal:   5 * 1024 * 1024 * 1024,
		downloadFile:    "atcode-v1.gguf",
	}
	view := plainRender(t, m.viewDownload(90))

	assertContains(t, view, "42%")
	assertContains(t, view, "Preparing your local AI…")
	assertNotContains(t, view, "GB /")
	assertNotContains(t, view, "MB received")
}

func TestLlamaReadyOpensTUIWithoutBlockingWarmup(t *testing.T) {
	m := startupModel{phase: phaseLlamaCheck, llamaPort: 42611}
	next, cmd := m.Update(llamaReadyMsg{})
	got, ok := next.(startupModel)
	if !ok {
		t.Fatalf("Update returned %T, want startupModel", next)
	}
	if got.phase != phaseDone {
		t.Fatalf("llama-ready phase = %v, want phaseDone", got.phase)
	}
	if cmd == nil {
		t.Fatal("llama-ready update must quit the startup screen")
	}
}

func TestEmptyStateDisappearsAfterFirstRow(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.width, m.height = 100, 30
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendUser, text: "hello"})

	view := viewString(m.View())
	if strings.Contains(view, emptyStateTagline) {
		t.Fatal("empty state must disappear once the transcript has content")
	}
	assertContains(t, view, "hello")
}

func TestDigitsTypeNormallyOnEmptySurface(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.width, m.height = 100, 30

	m = typeRunes(t, m, "2")
	if got := m.input.Value(); got != "2" {
		t.Fatalf("digit should type normally on the empty surface, got %q", got)
	}

	// With text already in the composer the digit keeps typing normally.
	m = newModel(context.Background(), Options{})
	m.input.SetValue("count to ")
	m.resetComposerFromInput()
	m = typeRunes(t, m, "3")
	if got := m.input.Value(); got != "count to 3" {
		t.Fatalf("digit should append to a non-empty composer, got %q", got)
	}

	// Once the transcript has content, a bare digit types normally too.
	fresh := newModel(context.Background(), Options{})
	fresh.transcript = reduceTranscript(fresh.transcript, transcriptAction{kind: actionAppendUser, text: "hi"})
	fresh = typeRunes(t, fresh, "1")
	if got := fresh.input.Value(); got != "1" {
		t.Fatalf("digit should type normally after the first turn, got %q", got)
	}
}

func TestBorderedBlockFitsLongPlainLines(t *testing.T) {
	block := borderedBlock(24, []string{"this line should truncate inside the border"})

	assertContains(t, block, "\u2026")
	assertRenderedLineWidths(t, block, 24)
}

func TestBorderedBlockFitsLongStyledLines(t *testing.T) {
	block := borderedBlock(26, []string{
		sonutTheme.accent.Render("styled line should truncate inside the border"),
	})

	assertContains(t, block, "\u2026")
	assertRenderedLineWidths(t, block, 26)
}

func assertRenderedLineWidths(t *testing.T, block string, width int) {
	t.Helper()

	for _, line := range strings.Split(block, "\n") {
		if got := lipgloss.Width(line); got != width {
			t.Fatalf("expected line width %d, got %d for %q", width, got, line)
		}
	}
}
