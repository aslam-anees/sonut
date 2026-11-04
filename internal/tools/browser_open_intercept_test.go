package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
)

// fakePlaywrightNavigate stands in for the registered playwright MCP tool.
type fakePlaywrightNavigate struct{ baseTool }

func newFakePlaywrightNavigate() Tool {
	return fakePlaywrightNavigate{baseTool: baseTool{
		name:        playwrightNavigateToolName,
		description: "fake",
		parameters:  Schema{Type: "object"},
		safety:      Safety{SideEffect: SideEffectNetwork, Permission: PermissionAllow},
	}}
}

func (fakePlaywrightNavigate) Run(context.Context, map[string]any) Result {
	return Result{Status: StatusOK, Output: "navigated"}
}

type fakePlaywrightScreenshot struct{ baseTool }

func newFakePlaywrightScreenshot() Tool {
	return fakePlaywrightScreenshot{baseTool: baseTool{
		name:        "mcp_playwright_browser_take_screenshot",
		description: "fake screenshot",
		parameters:  Schema{Type: "object"},
		safety:      Safety{SideEffect: SideEffectRead, Permission: PermissionAllow},
	}}
}

func (fakePlaywrightScreenshot) Run(context.Context, map[string]any) Result {
	return Result{Status: StatusOK, Images: []sonutruntime.ImageBlock{{MediaType: "image/png", Data: []byte("fake-png")}}}
}

type recordingPlaywrightNavigate struct {
	fakePlaywrightNavigate
	urls []string
}

type namedBrowserNavigate struct {
	baseTool
	result Result
	calls  int
}

func newNamedBrowserNavigate(name string, result Result) *namedBrowserNavigate {
	return &namedBrowserNavigate{
		baseTool: baseTool{
			name:        name,
			description: "fake",
			parameters:  Schema{Type: "object"},
			safety:      Safety{SideEffect: SideEffectNetwork, Permission: PermissionAllow},
		},
		result: result,
	}
}

func (tool *namedBrowserNavigate) Run(context.Context, map[string]any) Result {
	tool.calls++
	return tool.result
}

func (tool *recordingPlaywrightNavigate) Run(_ context.Context, args map[string]any) Result {
	url, _ := args["url"].(string)
	tool.urls = append(tool.urls, url)
	return Result{Status: StatusOK, Output: "navigated " + url}
}

func newInterceptTestRegistry(t *testing.T, withPlaywright bool) *Registry {
	t.Helper()
	registry := NewRegistry()
	registry.Register(NewBashTool(t.TempDir()))
	if withPlaywright {
		registry.Register(newFakePlaywrightNavigate())
	}
	return registry
}

func stubHostOpen(t *testing.T) *[]string {
	t.Helper()
	opened := &[]string{}
	prev := openURLForHost
	openURLForHost = func(u string) error {
		*opened = append(*opened, u)
		return nil
	}
	t.Cleanup(func() { openURLForHost = prev })
	return opened
}

func TestDetectBrowserOpenCommand(t *testing.T) {
	cases := []struct {
		command string
		wantURL string
		want    bool
	}{
		{"open https://x.com", "https://x.com", true},
		{"open -a Safari https://x.com/page", "https://x.com/page", true},
		{`open -a "Google Chrome" http://localhost:5173`, "http://localhost:5173", true},
		{"xdg-open https://example.org", "https://example.org", true},
		{"open -a Safari", "", true}, // GUI browser launch even without URL
		{`osascript -e 'open location "https://x.com"'`, "https://x.com", true},
		// Bare host — the live failure mode (sandbox outside_workspace on /).
		{"open x.com", "https://x.com", true},
		{"open www.example.com/path", "https://www.example.com/path", true},
		// Brand short-name — live failure: denied for twitter, worked for x.com.
		{"open twitter", "https://x.com", true},
		{"open twitter.com", "https://x.com", true},
		{"open youtube", "https://www.youtube.com", true},
		// Must NOT match:
		{"open notes.txt", "", false},
		{"open .", "", false},
		{"open -a TextEdit notes.txt", "", false},
		{"ls -la", "", false},
		{"curl https://x.com | grep title", "https://x.com", true}, // curl is first segment...
	}
	for _, tc := range cases {
		url, matched := detectBrowserOpenCommand(tc.command)
		if tc.command == "curl https://x.com | grep title" {
			// curl must never be intercepted
			if matched {
				t.Errorf("detectBrowserOpenCommand(%q) matched; curl must run untouched", tc.command)
			}
			continue
		}
		if matched != tc.want || (tc.want && url != tc.wantURL) {
			t.Errorf("detectBrowserOpenCommand(%q) = (%q, %v), want (%q, %v)", tc.command, url, matched, tc.wantURL, tc.want)
		}
	}
}

func TestBrowserOpenInterceptOpensHostWhenGranted(t *testing.T) {
	opened := stubHostOpen(t)
	registry := newInterceptTestRegistry(t, true)
	result := registry.RunWithOptions(context.Background(), "bash",
		map[string]any{"command": "open https://x.com"}, RunOptions{PermissionGranted: true})
	if result.Status != StatusOK {
		t.Fatalf("status = %q output=%q, want OK host open", result.Status, result.Output)
	}
	if result.Meta["opened"] != "host_browser" {
		t.Errorf("meta opened = %q, want host_browser", result.Meta["opened"])
	}
	if len(*opened) != 1 || (*opened)[0] != "https://x.com" {
		t.Fatalf("host open calls = %v, want [https://x.com]", *opened)
	}
	if !strings.Contains(result.Output, "Opened https://x.com") {
		t.Errorf("output %q missing success message", result.Output)
	}
}

func TestBrowserOpenInterceptBareHost(t *testing.T) {
	opened := stubHostOpen(t)
	registry := newInterceptTestRegistry(t, false)
	// bash is registered in the test registry (exec_command is not).
	result := registry.RunWithOptions(context.Background(), "bash",
		map[string]any{"command": "open x.com"}, RunOptions{PermissionMode: "unsafe"})
	if result.Status != StatusOK {
		t.Fatalf("bare host status = %q output=%q", result.Status, result.Output)
	}
	if len(*opened) != 1 || (*opened)[0] != "https://x.com" {
		t.Fatalf("opened = %v, want https://x.com", *opened)
	}
}

func TestBrowserOpenInterceptAlwaysOpensInAskMode(t *testing.T) {
	// User-requested page opens must never be blocked by permission mode or
	// a declining confirmation callback (live: twitter denied, x.com worked).
	opened := stubHostOpen(t)
	registry := newInterceptTestRegistry(t, false)
	result := registry.RunWithOptions(context.Background(), "bash",
		map[string]any{"command": "open https://example.org"}, RunOptions{
			PermissionMode: "ask",
			OnConfirmation: func(context.Context, string) (bool, error) {
				return false, nil // even if UI would say no, open for user intent
			},
		})
	if result.Status != StatusOK {
		t.Fatalf("status = %q output=%q, want always-open", result.Status, result.Output)
	}
	if len(*opened) != 1 || (*opened)[0] != "https://example.org" {
		t.Fatalf("opened = %v, want [https://example.org]", *opened)
	}
}

func TestBrowserOpenInterceptTwitterAlias(t *testing.T) {
	opened := stubHostOpen(t)
	registry := newInterceptTestRegistry(t, false)
	result := registry.RunWithOptions(context.Background(), "bash",
		map[string]any{"command": "open twitter"}, RunOptions{PermissionMode: "ask"})
	if result.Status != StatusOK {
		t.Fatalf("twitter alias status = %q output=%q", result.Status, result.Output)
	}
	if len(*opened) != 1 || (*opened)[0] != "https://x.com" {
		t.Fatalf("opened = %v, want [https://x.com]", *opened)
	}
}

func TestBrowserOpenInterceptSafariFlag(t *testing.T) {
	opened := stubHostOpen(t)
	registry := newInterceptTestRegistry(t, true)
	result := registry.RunWithOptions(context.Background(), "bash",
		map[string]any{"command": "open -a Safari https://x.com/docs"}, RunOptions{PermissionGranted: true})
	if result.Status != StatusOK || !strings.Contains(result.Output, "https://x.com/docs") {
		t.Fatalf("open -a Safari not opened on host: %q", result.Output)
	}
	if len(*opened) != 1 || (*opened)[0] != "https://x.com/docs" {
		t.Fatalf("opened = %v", *opened)
	}
}

func TestBrowserOpenInterceptLeavesUnrelatedCommands(t *testing.T) {
	registry := newInterceptTestRegistry(t, true)
	result := registry.RunWithOptions(context.Background(), "bash",
		map[string]any{"command": "echo hello"}, RunOptions{PermissionGranted: true})
	if result.Meta["opened"] == "host_browser" || result.Meta["blocked"] == "gui_browser_open" {
		t.Fatalf("unrelated command was intercepted: %q", result.Output)
	}
	if !strings.Contains(result.Output, "hello") {
		t.Errorf("echo did not run: %q", result.Output)
	}
}

// TestFileServerInterceptionViaPlaywrightNavigate verifies that calling the
// playwright MCP navigate tool with a file:// URL is intercepted and the model
// is given a localhost URL to use instead.
func TestFileServerInterceptionViaPlaywrightNavigate(t *testing.T) {
	t.Cleanup(shutdownFileServers)

	dir := t.TempDir()
	htmlFile := filepath.Join(dir, "mario.html")
	if err := os.WriteFile(htmlFile, []byte("<html><body>Mario</body></html>"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	registry := NewRegistry()
	registry.Register(NewBashTool(t.TempDir()))
	navigate := &recordingPlaywrightNavigate{fakePlaywrightNavigate: newFakePlaywrightNavigate().(fakePlaywrightNavigate)}
	registry.Register(navigate)
	fileURL := "file://" + htmlFile

	result := registry.RunWithOptions(context.Background(), playwrightNavigateToolName,
		map[string]any{"url": fileURL}, RunOptions{PermissionGranted: true})

	if result.Status != StatusOK {
		t.Fatalf("expected successful local-file handoff, got %q\noutput: %s", result.Status, result.Output)
	}
	for _, want := range []string{"http://localhost:", "mario.html"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output missing %q:\n%s", want, result.Output)
		}
	}
	if result.Meta["blocked"] != "file_url" {
		t.Errorf("meta blocked = %q, want file_url", result.Meta["blocked"])
	}
	if len(navigate.urls) != 1 || navigate.urls[0] != result.Meta["local_url"] {
		t.Fatalf("navigate calls = %#v, want local URL %q", navigate.urls, result.Meta["local_url"])
	}
	if result.Meta["browser_navigation"] != playwrightNavigateToolName {
		t.Errorf("browser_navigation = %q", result.Meta["browser_navigation"])
	}
	// The localhost URL should be in meta too.
	if !strings.Contains(result.Meta["local_url"], "http://localhost:") {
		t.Errorf("meta local_url = %q, expected http://localhost:...", result.Meta["local_url"])
	}
}

func TestFileServerInterceptionAttachesBrowserPreview(t *testing.T) {
	t.Cleanup(shutdownFileServers)

	htmlFile := filepath.Join(t.TempDir(), "preview.html")
	if err := os.WriteFile(htmlFile, []byte("<html><body>Preview</body></html>"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	registry := NewRegistry()
	registry.Register(newFakePlaywrightNavigate())
	registry.Register(newFakePlaywrightScreenshot())
	result := registry.openLocalFileInBrowser(context.Background(), htmlFile, "file://"+htmlFile, RunOptions{})
	if result.Status != StatusOK {
		t.Fatalf("expected successful local-file handoff, got %q", result.Status)
	}
	if len(result.Images) != 1 || string(result.Images[0].Data) != "fake-png" {
		t.Fatalf("preview images = %#v, want one attached screenshot", result.Images)
	}
	if result.Meta["browser_preview_screenshot"] != "true" {
		t.Fatalf("preview metadata = %#v, want browser_preview_screenshot", result.Meta)
	}
}

func TestFileServerInterceptionFallsBackToNextBrowserTool(t *testing.T) {
	t.Cleanup(shutdownFileServers)

	htmlFile := filepath.Join(t.TempDir(), "fallback.html")
	if err := os.WriteFile(htmlFile, []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := NewRegistry()
	first := newNamedBrowserNavigate(playwrightNavigateToolName, Result{Status: StatusError, Output: "playwright unavailable"})
	second := newNamedBrowserNavigate("mcp_custom_browser_navigate", Result{Status: StatusOK, Output: "custom navigated"})
	registry.Register(first)
	registry.Register(second)

	result := registry.openLocalFileInBrowser(context.Background(), htmlFile, "file://"+htmlFile, RunOptions{})
	if result.Status != StatusOK {
		t.Fatalf("fallback status = %q, output = %q", result.Status, result.Output)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("navigation calls = first:%d second:%d, want 1 each", first.calls, second.calls)
	}
	if result.Meta["browser_navigation"] != "mcp_custom_browser_navigate" {
		t.Fatalf("browser_navigation = %q, want custom fallback", result.Meta["browser_navigation"])
	}
}

// TestFileServerInterceptionViaBash verifies that bash `open file://...`
// is intercepted and served locally, not blocked as a GUI browser open.
func TestFileServerInterceptionViaBash(t *testing.T) {
	t.Cleanup(shutdownFileServers)

	dir := t.TempDir()
	htmlFile := filepath.Join(dir, "mario.html")
	if err := os.WriteFile(htmlFile, []byte("<html><body>Mario</body></html>"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	registry := newInterceptTestRegistry(t, true)
	result := registry.RunWithOptions(context.Background(), "bash",
		map[string]any{"command": "open file://" + htmlFile}, RunOptions{PermissionGranted: true})

	if result.Status != StatusOK {
		t.Fatalf("expected successful local-file handoff, got %q", result.Status)
	}
	for _, want := range []string{"http://localhost:", "mario.html"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output missing %q:\n%s", want, result.Output)
		}
	}
	if result.Meta["blocked"] != "file_url" {
		t.Errorf("meta blocked = %q, want file_url", result.Meta["blocked"])
	}
}

// TestBrowserTabsFileURLIntercepted verifies that mcp_playwright_browser_tabs
// called with a file:// URL is intercepted and redirected to the local file
// server, matching the same behaviour as mcp_playwright_browser_navigate.
func TestBrowserTabsFileURLIntercepted(t *testing.T) {
	t.Cleanup(shutdownFileServers)

	dir := t.TempDir()
	htmlFile := filepath.Join(dir, "mario.html")
	if err := os.WriteFile(htmlFile, []byte("<html><body>Mario</body></html>"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Register a fake browser_tabs tool so the intercept can look it up.
	registry := NewRegistry()
	registry.Register(NewBashTool(t.TempDir()))
	registry.Register(fakePlaywrightTool(playwrightTabsToolName))
	registry.Register(newFakePlaywrightNavigate())

	result := registry.RunWithOptions(context.Background(), playwrightTabsToolName,
		map[string]any{"url": "file://" + htmlFile}, RunOptions{PermissionGranted: true})

	if result.Status != StatusOK {
		t.Fatalf("expected successful local-file handoff, got %q\noutput: %s", result.Status, result.Output)
	}
	for _, want := range []string{"http://localhost:", "mario.html"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output missing %q:\n%s", want, result.Output)
		}
	}
	if result.Meta["blocked"] != "file_url" {
		t.Errorf("meta blocked = %q, want file_url", result.Meta["blocked"])
	}
}

// TestBrowserTabsNonFileURLNotIntercepted verifies that browser_tabs with an
// http URL is NOT intercepted (it is a legitimate navigation).
func TestBrowserTabsNonFileURLNotIntercepted(t *testing.T) {
	registry := NewRegistry()
	registry.Register(fakePlaywrightTool(playwrightTabsToolName))

	result := registry.RunWithOptions(context.Background(), playwrightTabsToolName,
		map[string]any{"url": "https://example.com"}, RunOptions{PermissionGranted: true})

	// Should NOT be blocked as file_url — the fake tool returns StatusOK.
	if result.Meta["blocked"] == "file_url" {
		t.Errorf("https:// URL should not be intercepted as file_url: %q", result.Output)
	}
}

// fakePlaywrightTool creates a minimal allow-all tool with the given name for
// use in interception tests that need a tool to actually exist in the registry.
func fakePlaywrightTool(name string) Tool {
	return fakeNamedTool{baseTool: baseTool{
		name:        name,
		description: "fake",
		parameters:  Schema{Type: "object"},
		safety:      Safety{SideEffect: SideEffectNetwork, Permission: PermissionAllow},
	}}
}

type fakeNamedTool struct{ baseTool }

func (fakeNamedTool) Run(context.Context, map[string]any) Result {
	return Result{Status: StatusOK, Output: "ok"}
}

// TestFileServerInterceptionMissingFile verifies that a file:// URL pointing
// to a non-existent file returns a clear error, not a localhost URL.
func TestFileServerInterceptionMissingFile(t *testing.T) {
	t.Cleanup(shutdownFileServers)

	registry := newInterceptTestRegistry(t, true)
	result := registry.RunWithOptions(context.Background(), playwrightNavigateToolName,
		map[string]any{"url": "file:///nonexistent/path/mario.html"}, RunOptions{PermissionGranted: true})

	if result.Status != StatusError {
		t.Fatalf("expected error status for missing file, got %q", result.Status)
	}
	if !strings.Contains(result.Output, "file not found") && !strings.Contains(result.Output, "cannot serve") {
		t.Errorf("output should mention file not found: %q", result.Output)
	}
}

func TestNormalizeBareHostURL(t *testing.T) {
	cases := map[string]string{
		"x.com":                "https://x.com",
		"www.example.com/path": "https://www.example.com/path",
		"twitter":              "https://x.com",
		"twitter.com":          "https://x.com",
		"www.twitter.com/home": "https://x.com/home",
		"youtube":              "https://www.youtube.com",
		"notes.txt":            "",
		".":                    "",
		"/abs/path":            "",
		"https://already.ok":   "",
	}
	for in, want := range cases {
		if got := normalizeBareHostURL(in); got != want {
			t.Errorf("normalizeBareHostURL(%q) = %q, want %q", in, got, want)
		}
	}
}
