package agent

import (
	"context"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

func TestPreloadBrowserToolsForUITask(t *testing.T) {
	registry := tools.NewRegistry()
	for _, name := range []string{
		"mcp_playwright_browser_navigate",
		"mcp_playwright_browser_snapshot",
		"mcp_playwright_browser_click",
		"mcp_playwright_browser_type",
		"mcp_other_unrelated",
	} {
		registry.Register(fakeDeferredTool{name: name, desc: name})
	}

	loaded := map[string]bool{}
	preloadBrowserToolsForUITask(loaded, registry, "Fix the React page layout and button")
	// Cold path: only navigate + snapshot (schema tax must stay small).
	for _, name := range []string{
		"mcp_playwright_browser_navigate",
		"mcp_playwright_browser_snapshot",
	} {
		if !loaded[name] {
			t.Fatalf("UI task did not preload cold-path tool %s", name)
		}
	}
	if loaded["mcp_playwright_browser_click"] || loaded["mcp_playwright_browser_type"] {
		t.Fatal("cold UI preload must NOT expose interaction tools (click/type) — expand after write")
	}
	if loaded["mcp_other_unrelated"] {
		t.Fatal("UI preload must not expose unrelated deferred MCP tools")
	}

	// After a successful file mutation, expand the full verification surface.
	maybeExpandBrowserToolsAfterWrite(loaded, registry, "Fix the React page layout and button", true)
	for _, name := range []string{
		"mcp_playwright_browser_click",
		"mcp_playwright_browser_type",
	} {
		if !loaded[name] {
			t.Fatalf("post-write expand did not load %s", name)
		}
	}
}

func TestPreloadBrowserToolsSkipsNonUITask(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(fakeDeferredTool{
		name: "mcp_playwright_browser_snapshot",
		desc: "snapshot",
	})
	loaded := map[string]bool{}
	preloadBrowserToolsForUITask(loaded, registry, "Refactor parser.go error handling")
	if len(loaded) != 0 {
		t.Fatalf("non-UI task unexpectedly preloaded browser tools: %#v", loaded)
	}
	maybeExpandBrowserToolsAfterWrite(loaded, registry, "Refactor parser.go error handling", true)
	if len(loaded) != 0 {
		t.Fatalf("non-UI task unexpectedly expanded browser tools after write: %#v", loaded)
	}
}

func TestQuickBrowserToolFilterKeepsOnlyOpenAndSnapshot(t *testing.T) {
	registry := tools.NewRegistry()
	for _, name := range []string{
		"browser_open",
		"browser_snapshot",
		"web_browser",
		"mcp_playwright_browser_navigate",
		"mcp_playwright_browser_snapshot",
		"mcp_playwright_browser_click",
		"bash",
	} {
		registry.Register(fakeDeferredTool{name: name, desc: name})
	}

	definitions, _ := partitionTools(registry, PermissionModeAuto, Options{QuickBrowser: true}, nil)
	for _, definition := range definitions {
		if !isQuickBrowserToolName(definition.Name) {
			t.Fatalf("quick browser path exposed unrelated tool %q", definition.Name)
		}
	}
	if len(definitions) != 5 {
		t.Fatalf("quick browser path exposed %d tools, want 5: %#v", len(definitions), definitions)
	}
}

func TestQuickBrowserRequestAdvertisesBrowserTools(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{{
		{Type: sonutruntime.StreamEventText, Content: "The page is open."},
		{Type: sonutruntime.StreamEventDone},
	}}}
	registry := tools.NewRegistry()
	for _, name := range []string{"browser_open", "browser_snapshot", "bash"} {
		registry.Register(fakeDeferredTool{name: name, desc: name})
	}
	prompt := "open airbnb webpage"
	_, err := Run(context.Background(), prompt, provider, Options{
		Registry:     registry,
		ProviderName: "llama",
		RunMode:      RouteRunMode(prompt),
		Evaluate:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("quick browser request should skip planner, got %d requests", len(provider.requests))
	}
	if len(provider.requests[0].Tools) != 2 {
		t.Fatalf("quick browser request exposed %d tools, want browser_open + browser_snapshot: %#v", len(provider.requests[0].Tools), provider.requests[0].Tools)
	}
	for _, definition := range provider.requests[0].Tools {
		if !isQuickBrowserToolName(definition.Name) {
			t.Fatalf("quick browser request exposed unrelated tool %q", definition.Name)
		}
	}
}
