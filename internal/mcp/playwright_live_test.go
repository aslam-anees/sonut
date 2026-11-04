package mcp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aistatine/sonut/internal/config"
)

// Live end-to-end check of the default Playwright MCP server through @code's
// own stdio client: connect, list tools, navigate to a data: URL, snapshot.
// Only runs when SONUT_LIVE_PLAYWRIGHT=1 (needs node/npx).
func TestPlaywrightMCPLive(t *testing.T) {
	if os.Getenv("SONUT_LIVE_PLAYWRIGHT") != "1" {
		t.Skip("set SONUT_LIVE_PLAYWRIGHT=1 for the live check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	args := append(config.PlaywrightMCPArgs(), "--output-dir", t.TempDir())
	client, err := Connect(ctx, Server{
		Name:    "playwright",
		Type:    ServerTypeStdio,
		Command: "npx",
		Args:    args,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	remote, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(remote) < 15 {
		t.Fatalf("expected the full playwright toolset, got %d tools", len(remote))
	}

	result, err := client.CallTool(ctx, "browser_navigate", map[string]any{
		"url": "data:text/html,<title>sonut-live</title><h1>Be Human, Build AI</h1>",
	})
	if err != nil {
		t.Fatalf("browser_navigate: %v", err)
	}
	if result.IsError {
		t.Fatalf("browser_navigate returned tool error: %+v", result.Content)
	}
	var text strings.Builder
	for _, item := range result.Content {
		text.WriteString(item.Text)
	}
	if !strings.Contains(text.String(), "Be Human, Build AI") && !strings.Contains(text.String(), "sonut-live") {
		t.Fatalf("navigate result missing page content: %q", text.String())
	}
}
