package tools

import (
	"context"
	"strings"
	"testing"
)

func TestRegistryResolvesAliasedToolNames(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewReadFileTool("."))
	if got := registry.Resolve("str_replace"); got != "str_replace" {
		// edit_file not registered here, so alias must NOT resolve blindly
		t.Fatalf("unregistered alias target resolved: %q", got)
	}
	if got := registry.Resolve("cat"); got != "read_file" {
		t.Fatalf("cat should resolve to read_file, got %q", got)
	}
	if got := registry.Resolve(" Read_File "); got != "read_file" {
		t.Fatalf("case/space normalization failed: %q", got)
	}
	res := registry.Run(context.Background(), "no_such_tool", nil)
	if !strings.Contains(res.Output, "read_file") {
		t.Fatalf("unknown-tool error should list available tools: %q", res.Output)
	}
}

func TestRegistryResolvesUnambiguousMCPNearMisses(t *testing.T) {
	registry := NewRegistry()
	registry.Register(fakeNamedTool{baseTool: baseTool{name: "mcp_firecrawl_firecrawl_search"}})
	registry.Register(fakeNamedTool{baseTool: baseTool{name: "mcp_playwright_browser_navigate"}})

	for input, want := range map[string]string{
		"mcp_firecrawl_search":              "mcp_firecrawl_firecrawl_search",
		"mcp__playwright__browser_navigate": "mcp_playwright_browser_navigate",
		"browser_navigate":                  "mcp_playwright_browser_navigate",
		"mcp_firecrawl_firecrawl_search":    "mcp_firecrawl_firecrawl_search",
	} {
		if got := registry.Resolve(input); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRegistryDoesNotGuessAmbiguousMCPAlias(t *testing.T) {
	registry := NewRegistry()
	registry.Register(fakeNamedTool{baseTool: baseTool{name: "mcp_alpha_search"}})
	registry.Register(fakeNamedTool{baseTool: baseTool{name: "mcp_beta_search"}})
	if got := registry.Resolve("search"); got != "search" {
		t.Fatalf("ambiguous alias resolved to %q", got)
	}
}
