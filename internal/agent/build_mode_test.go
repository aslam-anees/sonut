package agent

import (
	"strings"
	"testing"
)

func TestBuildModePromptOverlay(t *testing.T) {
	off := BuildSystemPrompt(Options{})
	if strings.Contains(off, "# Build Mode") {
		t.Fatal("build-mode section must be absent by default")
	}
	on := BuildSystemPrompt(Options{BuildMode: true})
	if !strings.Contains(on, "# Build Mode") {
		t.Fatal("build-mode section missing when BuildMode is set")
	}
	// The overlay adds to the normal prompt; it must not replace the core.
	if !strings.Contains(on, off[:200]) {
		t.Fatal("build mode must extend, not replace, the core prompt")
	}
	for _, want := range []string{
		"mcp_playwright_browser_navigate",
		"mcp_playwright_browser_console_messages",
		"mcp_playwright_browser_snapshot",
		"visible (headful) browser",
	} {
		if !strings.Contains(on, want) {
			t.Fatalf("build-mode browser workflow missing %q", want)
		}
	}
	if strings.Contains(on, "web_browser action:") {
		t.Fatal("build mode must not instruct the model to call the removed web_browser API")
	}
}
