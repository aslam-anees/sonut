package tui

import (
	"context"
	"strings"
	"testing"
)

func TestParseBrowserToolResultSetsErrorStatus(t *testing.T) {
	b := &browserState{}
	b.parseBrowserToolResult("browser_click", "Error: element not found", true)
	if b.status != "error" {
		t.Fatalf("status = %q, want %q", b.status, "error")
	}
}

func TestParseBrowserToolResultSetsReadyStatusOnSuccess(t *testing.T) {
	b := &browserState{}
	b.parseBrowserToolResult("browser_click", "click completed", false)
	if b.status != "ready" {
		t.Fatalf("status = %q, want %q", b.status, "ready")
	}
}

func TestNewBrowserStateStartsWithoutSyntheticPage(t *testing.T) {
	state := newBrowserState()
	if state.url != "" || state.title != "" {
		t.Fatalf("idle state = %#v, want no synthetic URL or title", state)
	}
	if !state.idlePage || state.status != "idle" {
		t.Fatalf("idle state = %#v, want idlePage=true and status=idle", state)
	}
}

func TestBrowserPanelStaysEmptyUntilActivity(t *testing.T) {
	m := model{
		browserState:   newBrowserState(),
		browserHeadful: false,
	}
	text := strings.Join(m.renderBrowserPanel(34, 10), "\n")
	if strings.Contains(text, "atcode") || strings.Contains(text, browserLandingURL) {
		t.Fatalf("idle browser panel should not show a fabricated page:\n%s", text)
	}
}

func TestBrowserPanelShowsLogoWhenNoPreviewExists(t *testing.T) {
	if browserLogoAssetPath() == "" {
		t.Skip("logo.png is not available in this checkout")
	}

	text := strings.Join((model{browserState: newBrowserState()}).renderBrowserPanel(42, 16), "\n")
	if !strings.Contains(text, "\x1b[38;2;") || !strings.Contains(text, "▀") {
		t.Fatalf("browser panel should render logo.png when no preview exists:\n%s", text)
	}
}

func TestBrowserToolActivityReplacesLandingPage(t *testing.T) {
	state := newBrowserState()
	state.parseBrowserToolCall("browser_click", `{"ref":"e1"}`)
	if state.idlePage {
		t.Fatal("browser activity should replace the idle landing page")
	}
	if state.url == browserLandingURL || state.title == browserIdleTitle {
		t.Fatalf("browser activity must clear idle placeholders: %#v", state)
	}
}

func TestBrowserStateTracksCompactOpenAndResultURL(t *testing.T) {
	state := newBrowserState()
	state.parseBrowserToolCall("browser_open", "https://www.airbnb.com")
	if state.url != "https://www.airbnb.com" || state.status != "navigating" {
		t.Fatalf("open state = %#v, want Airbnb URL and navigating status", state)
	}
	state.parseBrowserToolResult("browser_open", "Title: Airbnb\nURL: https://www.airbnb.com/", false)
	if state.url != "https://www.airbnb.com" {
		t.Fatalf("result must preserve the navigated URL, got %q", state.url)
	}
	if state.title != "Airbnb" || state.status != "ready" {
		t.Fatalf("result state = %#v, want Airbnb/ready", state)
	}
}

func TestBrowserPanelShowsLatestSnapshotInsteadOfLandingPage(t *testing.T) {
	state := newBrowserState()
	state.parseBrowserToolCall("browser_open", "https://www.airbnb.com")
	state.parseBrowserToolResult("browser_open", "Title: Airbnb\nURL: https://www.airbnb.com/", false)
	state.parseBrowserToolResult("browser_snapshot", "heading \"Airbnb\"\nlink \"Homes\"", false)
	text := strings.Join((model{browserState: state}).renderBrowserPanel(42, 16), "\n")
	for _, want := range []string{"https://www.airbnb.com", "Airbnb", "VISION", `heading "Airbnb"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("browser panel missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, browserLandingURL) || strings.Contains(text, "browser ready") {
		t.Fatalf("browser panel still shows the idle landing surface:\n%s", text)
	}
}

func TestBrowserPanelRendersAttachedScreenshotInApp(t *testing.T) {
	state := newBrowserState()
	state.parseBrowserToolCall("browser_open", "https://www.airbnb.com")
	state.parseBrowserToolResultWithImage("browser_open", "Title: Airbnb\nURL: https://www.airbnb.com/", false, []byte("fake-png"))
	text := strings.Join((model{browserState: state}).renderBrowserPanel(42, 16), "\n")
	if !strings.Contains(text, "\x1b]1337;File=inline=1;") {
		t.Fatalf("browser panel did not render the attached screenshot in-app:\n%s", text)
	}
	if strings.Contains(text, "terminal-browser.com") || strings.Contains(text, "browser ready") {
		t.Fatalf("browser panel contains the old placeholder:\n%s", text)
	}
}

func TestBrowserActionCompactOpenURLIsRecognized(t *testing.T) {
	if got := browserToolCallURL("web_browser", "open https://www.airbnb.com"); got != "https://www.airbnb.com" {
		t.Fatalf("browserToolCallURL = %q, want compact Airbnb URL", got)
	}
	if got := browserToolCallURL("web_browser", `{"action":"navigate","args":["https://www.airbnb.com"]}`); got != "https://www.airbnb.com" {
		t.Fatalf("browserToolCallURL = %q, want nested Airbnb URL", got)
	}
}

func TestNativeBrowserSurfaceRemainsVisibleWithTerminalBrowser(t *testing.T) {
	m := model{terminalBrowserActive: true}
	text := strings.Join(m.buildRightColumn(34, 10), "\n")
	if !strings.Contains(text, "BROWSER") {
		t.Fatalf("native browser preview should remain visible alongside terminal-browser:\n%s", text)
	}
}

func TestUnsupportedTerminalBrowserSkipsExternalPane(t *testing.T) {
	wasSupported := terminalBrowserSupported
	wasOpen := openTerminalBrowser
	defer func() {
		terminalBrowserSupported = wasSupported
		openTerminalBrowser = wasOpen
	}()

	terminalBrowserSupported = func() bool { return false }
	called := false
	openTerminalBrowser = func(context.Context, string, string, float64) error {
		called = true
		return nil
	}

	msg := openTerminalBrowserCmd(context.Background())()
	opened, ok := msg.(terminalBrowserOpenedMsg)
	if !ok {
		t.Fatalf("message type = %T, want terminalBrowserOpenedMsg", msg)
	}
	if opened.opened || opened.err != nil {
		t.Fatalf("unsupported terminal-browser result = %#v, want silent skip", opened)
	}
	if called {
		t.Fatal("unsupported host should not invoke terminal-browser")
	}

	updated, _ := (model{}).updateModel(opened)
	if updated.(model).terminalBrowserActive {
		t.Fatal("silent terminal-browser skip should leave the external pane inactive")
	}
}

func TestBrowserToolCallURLRecognizesPlaywrightNavigate(t *testing.T) {
	got := browserToolCallURL("mcp_playwright_browser_navigate", `{"url":"https://terminal-browser.com/"}`)
	if got != browserLandingURL {
		t.Fatalf("browserToolCallURL = %q, want %q", got, browserLandingURL)
	}
}

func TestBrowserStateTracksNestedLocalFileNavigation(t *testing.T) {
	const detail = "Opened local page in the visible browser at http://localhost:4567/mario.html via mcp_playwright_browser_navigate.\n\nnavigated"
	if !isLocalFileBrowserHandoff(detail) {
		t.Fatal("expected local file browser handoff to be recognized")
	}
	var state browserState
	state.parseLocalFileBrowserHandoff(detail, false)
	if state.status != "ready" || state.url != "http://localhost:4567/mario.html" {
		t.Fatalf("state = %#v, want ready local URL", state)
	}
	if len(state.actions) != 1 || !strings.Contains(state.actions[0], "opened local page") {
		t.Fatalf("actions = %#v", state.actions)
	}
}

func TestBrowserStateUsesLocalFileHandoffPreview(t *testing.T) {
	const detail = "Opened local page in the visible browser at http://localhost:4567/preview.html via mcp_playwright_browser_navigate."
	var state browserState
	state.parseLocalFileBrowserHandoffWithImage(detail, false, []byte("fake-png"))
	if len(state.screenshotData) == 0 || state.lastAction != "📸 screenshot" {
		t.Fatalf("state = %#v, want attached local-page screenshot", state)
	}
	text := strings.Join((model{browserState: state}).renderBrowserPanel(42, 16), "\n")
	if !strings.Contains(text, "\x1b]1337;File=inline=1;") {
		t.Fatalf("local-page preview was not rendered:\n%s", text)
	}
}
