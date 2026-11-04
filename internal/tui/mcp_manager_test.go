package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/aistatine/sonut/internal/config"
)

func TestMCPManagerViewRendersServerActionHints(t *testing.T) {
	got := plainRender(t, renderMCPView(MCPViewState{
		Servers: []MCPServerView{
			{Name: "docs", Transport: "stdio", State: "enabled", Target: "atcode-docs-mcp --workspace .", ToolCount: 2},
			{Name: "linear", Transport: "http", State: "disabled", Target: "https://mcp.linear.example", Auth: "oauth"},
		},
	}, 140))

	for _, want := range []string{
		"at mcp check docs",
		"at mcp disable docs",
		"at mcp remove docs",
		"at mcp enable linear",
		"at mcp oauth login linear",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("MCP manager server actions = %q, missing %q", got, want)
		}
	}
}

func TestMCPManagerViewGroupsToolsUnderServers(t *testing.T) {
	got := plainRender(t, renderMCPView(MCPViewState{
		Servers: []MCPServerView{
			{Name: "docs", Transport: "stdio", State: "enabled", ToolCount: 2},
			{Name: "github", Transport: "http", State: "enabled", ToolCount: 1},
		},
		Tools: []MCPToolView{
			{ServerName: "docs", Name: "lookup", RegistryName: "mcp_docs_lookup", SideEffect: "network", Permission: "prompt", Description: "Look up documentation"},
			{ServerName: "docs", Name: "search", RegistryName: "mcp_docs_search", SideEffect: "network", Permission: "prompt", Description: "Search documentation"},
			{ServerName: "github", Name: "create_issue", RegistryName: "mcp_github_create_issue", SideEffect: "network", Permission: "prompt", Description: "Create an issue"},
		},
	}, 160))

	for _, want := range []string{
		"docs tools (2)",
		"lookup [network/prompt] - mcp_docs_lookup - docs/lookup - Look up documentation",
		"search [network/prompt] - mcp_docs_search - docs/search - Search documentation",
		"github tools (1)",
		"create_issue [network/prompt] - mcp_github_create_issue - github/create_issue - Create an issue",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("MCP manager grouped tools = %q, missing %q", got, want)
		}
	}
}

func TestMCPManagerViewRendersOAuthActionHints(t *testing.T) {
	expiry := time.Date(2026, 6, 13, 11, 45, 0, 0, time.UTC)
	got := plainRender(t, renderMCPView(MCPViewState{
		OAuth: MCPOAuthSummary{
			Servers: []MCPOAuthServerView{
				{ServerName: "linear", Configured: true, HasToken: true, HasRefreshToken: true, TokenType: "Bearer", ExpiresAt: expiry},
				{ServerName: "notion", Configured: true},
				{ServerName: "expired", Configured: true, HasToken: true, Expired: true},
			},
		},
	}, 140))

	for _, want := range []string{
		"at mcp oauth refresh linear",
		"at mcp oauth logout linear",
		"at mcp oauth login notion",
		"at mcp oauth refresh expired",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("MCP manager OAuth actions = %q, missing %q", got, want)
		}
	}
}

func TestMCPManagerTargetsRedactSecretsFromURLsAndArgs(t *testing.T) {
	state := BuildMCPViewState(MCPStateOptions{
		Config: config.MCPConfig{Servers: map[string]config.MCPServerConfig{
			"docs": {
				Type:    "stdio",
				Command: "atcode-docs-mcp",
				Args:    []string{"--workspace", ".", "--token", "arg-secret", "--api-key=inline-secret", "--endpoint", "https://remote.example/mcp?access_token=arg-url-secret#token=frag-secret", "--inline-endpoint=https://remote.example/mcp?access_token=inline-url-secret#token=inline-frag-secret"},
				Env:     map[string]string{"SONUT_DOCS_TOKEN": "env-secret"},
			},
			"linear": {
				Type: "http",
				URL:  "https://mcp.linear.example/sse?access_token=url-secret&workspace=public",
				Headers: map[string]string{
					"Authorization": "Bearer header-secret",
				},
			},
		}},
	})
	got := plainRender(t, renderMCPView(state, 260))

	for _, leaked := range []string{
		"arg-secret",
		"inline-secret",
		"arg-url-secret",
		"frag-secret",
		"inline-url-secret",
		"inline-frag-secret",
		"url-secret",
		"env-secret",
		"header-secret",
	} {
		if strings.Contains(got, leaked) {
			t.Fatalf("MCP manager target leaked %q in:\n%s", leaked, got)
		}
	}
	for _, want := range []string{
		"--token [REDACTED]",
		"--api-key=[REDACTED]",
		"access_token=[REDACTED]",
		"token=[REDACTED]",
		"--inline-endpoint=https://remote.example/mcp?access_token=[REDACTED]#token=[REDACTED]",
		"workspace=public",
		"SONUT_DOCS_TOKEN=[REDACTED]",
		"Authorization=[REDACTED]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("MCP manager redacted target = %q, missing %q", got, want)
		}
	}
}
