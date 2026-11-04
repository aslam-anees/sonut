package mcp

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/tools"
)

func TestRegisterToolsAddsPromptGatedMCPTools(t *testing.T) {
	registry := tools.NewRegistry()
	client := &fakeToolClient{listed: []RemoteTool{{
		Name:        "lookup",
		Description: "Lookup documentation",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
		},
	}}}

	runtime, err := RegisterTools(context.Background(), registry, config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"docs": {Type: "stdio", Command: "docs-mcp"},
	}}, RegisterOptions{
		ClientFactory: func(context.Context, Server) (ToolClient, error) {
			return client, nil
		},
	})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	defer runtime.Close()

	tool, ok := registry.Get("mcp_docs_lookup")
	if !ok {
		t.Fatal("expected mcp_docs_lookup to be registered")
	}
	if tool.Safety().Permission != tools.PermissionPrompt {
		t.Fatalf("Safety.Permission = %q, want prompt", tool.Safety().Permission)
	}
	if tool.Safety().SideEffect != tools.SideEffectNetwork {
		t.Fatalf("Safety.SideEffect = %q, want network", tool.Safety().SideEffect)
	}

	denied := registry.Run(context.Background(), "mcp_docs_lookup", map[string]any{"query": "atcode"})
	if denied.Status != tools.StatusError {
		t.Fatalf("Run without approval = %#v, want permission error", denied)
	}
	approved := registry.RunWithOptions(context.Background(), "mcp_docs_lookup", map[string]any{"query": "atcode"}, tools.RunOptions{PermissionGranted: true})
	if approved.Status != tools.StatusOK || approved.Output != "lookup: atcode" {
		t.Fatalf("approved run = %#v, want lookup output", approved)
	}
	if approved.Meta["mcp.server"] != "docs" || approved.Meta["mcp.tool"] != "lookup" {
		t.Fatalf("approved meta = %#v, want mcp server/tool", approved.Meta)
	}
	if client.closed != 0 {
		t.Fatalf("client.closed before runtime close = %d, want 0", client.closed)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Runtime.Close() error = %v", err)
	}
	if client.closed != 1 {
		t.Fatalf("client.closed after runtime close = %d, want 1", client.closed)
	}
}

func TestRegisterToolsMarksPersistentlyApprovedToolsAllow(t *testing.T) {
	store, err := NewPermissionStore(StoreOptions{
		FilePath: filepath.Join(t.TempDir(), "permissions.json"),
		Now:      func() time.Time { return time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	servers, err := NormalizeConfig(config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"docs": {Type: "stdio", Command: "docs-mcp"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantTool(GrantToolInput{
		ServerName:     "docs",
		ServerIdentity: servers[0].Identity,
		ToolName:       "lookup",
		MaxAutonomy:    AutonomyLow,
	}); err != nil {
		t.Fatal(err)
	}

	registry := tools.NewRegistry()
	runtime, err := RegisterTools(context.Background(), registry, config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"docs": {Type: "stdio", Command: "docs-mcp"},
	}}, RegisterOptions{
		PermissionStore: store,
		Autonomy:        AutonomyLow,
		ClientFactory: func(context.Context, Server) (ToolClient, error) {
			return &fakeToolClient{listed: []RemoteTool{{Name: "lookup", Description: "Lookup documentation"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	defer runtime.Close()

	tool, ok := registry.Get("mcp_docs_lookup")
	if !ok {
		t.Fatal("expected mcp_docs_lookup to be registered")
	}
	if tool.Safety().Permission != tools.PermissionAllow {
		t.Fatalf("Safety.Permission = %q, want allow from persistent MCP grant", tool.Safety().Permission)
	}
}

func TestRegisterToolsSkipsUnreachableServerAndKeepsOthers(t *testing.T) {
	// NormalizeConfig sorts server names, so "alpha" registers before "zebra".
	// "zebra" is unreachable; it must be SKIPPED (recorded in Skipped()), not fatal,
	// and "alpha"'s tools must still register — one bad server can't disable the rest
	// or abort startup. A server still contributes its tools all-or-none.
	registry := tools.NewRegistry()
	alphaClient := &fakeToolClient{listed: []RemoteTool{{Name: "lookup", Description: "Lookup documentation"}}}

	runtime, err := RegisterTools(context.Background(), registry, config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"alpha": {Type: "stdio", Command: "alpha-mcp"},
		"zebra": {Type: "stdio", Command: "zebra-mcp"},
	}}, RegisterOptions{
		ClientFactory: func(_ context.Context, server Server) (ToolClient, error) {
			if server.Name == "zebra" {
				return nil, errors.New("zebra connect failed")
			}
			return alphaClient, nil
		},
	})
	if err != nil {
		t.Fatalf("RegisterTools returned error, want a skip: %v", err)
	}
	defer runtime.Close()
	if _, ok := registry.Get("mcp_alpha_lookup"); !ok {
		t.Fatal("expected the reachable server's tools to register when another is skipped")
	}
	skipped := runtime.Skipped()
	if len(skipped) != 1 || skipped[0].Name != "zebra" {
		t.Fatalf("Skipped() = %#v, want exactly one entry for zebra", skipped)
	}
	if skipped[0].Err == nil {
		t.Fatal("expected the skipped server to record why it was skipped")
	}
}

func TestRegisterToolsKeepsPriorStateAndReachableServersWhenOneIsSkipped(t *testing.T) {
	// A tool that predates registration must survive, the reachable server's tools
	// must register, and the unreachable server is skipped (recorded), not fatal.
	registry := tools.NewRegistry()
	registry.Register(&fakePreexistingTool{name: "preexisting"})

	runtime, err := RegisterTools(context.Background(), registry, config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"alpha": {Type: "stdio", Command: "alpha-mcp"},
		"zebra": {Type: "stdio", Command: "zebra-mcp"},
	}}, RegisterOptions{
		ClientFactory: func(_ context.Context, server Server) (ToolClient, error) {
			if server.Name == "zebra" {
				return nil, errors.New("zebra connect failed")
			}
			return &fakeToolClient{listed: []RemoteTool{{Name: "lookup"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("RegisterTools returned error, want a skip: %v", err)
	}
	defer runtime.Close()
	if _, ok := registry.Get("preexisting"); !ok {
		t.Fatal("expected the pre-existing tool to survive registration")
	}
	if _, ok := registry.Get("mcp_alpha_lookup"); !ok {
		t.Fatal("expected the reachable server's tools to register")
	}
	if skipped := runtime.Skipped(); len(skipped) != 1 || skipped[0].Name != "zebra" {
		t.Fatalf("Skipped() = %#v, want exactly one entry for zebra", skipped)
	}
}

func TestRegisterToolsSkipsServerThatExceedsConnectTimeout(t *testing.T) {
	// A slow server must not block startup: it is abandoned after ConnectTimeout
	// and skipped, while a fast server registers normally. This is the latency fix
	// — one unreachable server can't hold up the first response.
	registry := tools.NewRegistry()
	fastClient := &fakeToolClient{listed: []RemoteTool{{Name: "lookup", Description: "fast"}}}

	runtime, err := RegisterTools(context.Background(), registry, config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"fast": {Type: "stdio", Command: "fast-mcp"},
		"slow": {Type: "stdio", Command: "slow-mcp"},
	}}, RegisterOptions{
		ConnectTimeout: 50 * time.Millisecond,
		ClientFactory: func(ctx context.Context, server Server) (ToolClient, error) {
			if server.Name == "slow" {
				// Block past the timeout, honoring ctx cancellation so the abandoned
				// connect tears down cleanly instead of leaking.
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return fastClient, nil
		},
	})
	if err != nil {
		t.Fatalf("RegisterTools error: %v", err)
	}
	defer runtime.Close()
	if _, ok := registry.Get("mcp_fast_lookup"); !ok {
		t.Fatal("expected the fast server's tools to register despite the slow one timing out")
	}
	skipped := runtime.Skipped()
	if len(skipped) != 1 || skipped[0].Name != "slow" {
		t.Fatalf("Skipped() = %#v, want exactly one entry for slow", skipped)
	}
}

func TestConnectTimeoutForPlaywrightBootstrap(t *testing.T) {
	playwright := Server{Name: "playwright", Type: ServerTypeStdio, Command: "npx", Args: []string{"-y", "@playwright/mcp@latest"}}
	if got := connectTimeoutForServer(playwright, 0); got != playwrightBootstrapTimeout {
		t.Fatalf("Playwright timeout = %s, want %s", got, playwrightBootstrapTimeout)
	}
	custom := Server{Name: "ui", Type: ServerTypeStdio, Command: "npx", Args: []string{"@playwright/mcp@latest"}}
	if got := connectTimeoutForServer(custom, 0); got != playwrightBootstrapTimeout {
		t.Fatalf("custom Playwright timeout = %s, want %s", got, playwrightBootstrapTimeout)
	}
	if got := connectTimeoutForServer(playwright, 25*time.Millisecond); got != 25*time.Millisecond {
		t.Fatalf("explicit timeout = %s, want 25ms", got)
	}
	if got := connectTimeoutForServer(Server{Name: "docs"}, 0); got != defaultConnectTimeout {
		t.Fatalf("ordinary timeout = %s, want %s", got, defaultConnectTimeout)
	}
}

func TestRegistryToolIsDeferredEligible(t *testing.T) {
	client := &fakeToolClient{}
	tool := newRegistryTool(
		Server{Name: "docs", Type: "stdio"},
		RemoteTool{Name: "lookup", Description: "Lookup documentation"},
		client,
		RegisterOptions{},
	)

	// Sanity: the wrapper synthesizes the expected sanitized name so the test
	// exercises the real production path, not a hand-built struct.
	if tool.Name() != "mcp_docs_lookup" {
		t.Fatalf("registryTool.Name() = %q, want mcp_docs_lookup", tool.Name())
	}

	if !tool.Deferred() {
		t.Fatal("registryTool.Deferred() = false, want true (all MCP tools are deferred-eligible)")
	}

	// The exported helper in the tools package must agree via the optional
	// interface, since the agent loop partitions tools through tools.IsDeferred.
	if !tools.IsDeferred(tool) {
		t.Fatal("tools.IsDeferred(registryTool) = false, want true")
	}
}

// TestRegistryToolReportsMCPServerName verifies the registryTool reports its true
// configured server name (not the sanitized tool-name token) so the deferred-tools
// reminder labels a multi-token server correctly via tools.DeferredLine.
func TestRegistryToolReportsMCPServerName(t *testing.T) {
	client := &fakeToolClient{}
	// A server name that sanitizes to a token containing an underscore ("git_hub"):
	// the name-only parser would truncate the label to "git".
	tool := newRegistryTool(
		Server{Name: "git hub", Type: "stdio"},
		RemoteTool{Name: "create_issue", Description: "Create a GitHub issue."},
		client,
		RegisterOptions{},
	)

	if tool.MCPServerName() != "git hub" {
		t.Fatalf("MCPServerName() = %q, want %q", tool.MCPServerName(), "git hub")
	}

	// DeferredLine must prefer the reported server name over the name-derived token.
	line := tools.DeferredLine(tool)
	if !strings.Contains(line, "server: git hub") {
		t.Fatalf("DeferredLine = %q, want it to label server as %q via MCPServerName()", line, "git hub")
	}
	// The truncated token-only label ("git") must NOT be the server segment.
	if strings.Contains(line, "server: git |") {
		t.Fatalf("DeferredLine = %q, mislabeled multi-token server with the truncated token", line)
	}
}

// TestRegistryToolNameRoundTripsToServerToken pins the synthesized name format
// for a single-token server so the fallback label parser (mcpServerFromToolName,
// exercised in the tools package) recovers the right server token.
func TestRegistryToolNameRoundTripsToServerToken(t *testing.T) {
	if name := registryToolName("docs", "lookup"); name != "mcp_docs_lookup" {
		t.Fatalf("registryToolName(docs, lookup) = %q, want mcp_docs_lookup", name)
	}
}

func TestRegistryToolRetriesTransientReadOnlyCallOnce(t *testing.T) {
	client := &flakyToolClient{failures: 1}
	tool := newRegistryTool(
		Server{Name: "docs", Type: "http"},
		RemoteTool{Name: "lookup"},
		client,
		RegisterOptions{},
	)

	result := tool.Run(context.Background(), map[string]any{"query": "atcode"})
	if result.Status != tools.StatusOK || result.Output != "recovered" {
		t.Fatalf("retry result = %#v, want recovered success", result)
	}
	if client.calls != 2 {
		t.Fatalf("CallTool calls = %d, want 2", client.calls)
	}
	if result.Meta["mcp.attempts"] != "2" {
		t.Fatalf("mcp.attempts = %q, want 2", result.Meta["mcp.attempts"])
	}
}

func TestRegistryToolDoesNotReplayPotentiallyMutatingCall(t *testing.T) {
	client := &flakyToolClient{failures: 1}
	tool := newRegistryTool(
		Server{Name: "issues", Type: "http"},
		RemoteTool{Name: "create_issue"},
		client,
		RegisterOptions{},
	)

	result := tool.Run(context.Background(), map[string]any{"title": "one"})
	if result.Status != tools.StatusError {
		t.Fatalf("mutating transport failure = %#v, want error", result)
	}
	if client.calls != 1 {
		t.Fatalf("mutating CallTool calls = %d, want 1 (never replay)", client.calls)
	}
	if result.Error == nil || result.Error.Retryable {
		t.Fatalf("mutating failure structured error = %#v, want non-retryable", result.Error)
	}
}

func TestMCPRetrySafetyHonorsAnnotations(t *testing.T) {
	if !mcpToolSafeToRetry(RemoteTool{Name: "custom_action", Annotations: ToolAnnotations{ReadOnlyHint: true}}) {
		t.Fatal("read-only annotation should permit a transient retry")
	}
	if !mcpToolSafeToRetry(RemoteTool{Name: "set_state", Annotations: ToolAnnotations{IdempotentHint: true}}) {
		t.Fatal("idempotent annotation should permit a transient retry")
	}
	if mcpToolSafeToRetry(RemoteTool{
		Name:        "get_state",
		Annotations: ToolAnnotations{ReadOnlyHint: true, DestructiveHint: true},
	}) {
		t.Fatal("destructive annotation must override query-like naming and read-only hints")
	}
	if mcpToolSafeToRetry(RemoteTool{Name: "get_or_create"}) {
		t.Fatal("action tokens must override a query-like prefix")
	}
}

type fakePreexistingTool struct {
	name string
}

func (t *fakePreexistingTool) Name() string             { return t.name }
func (t *fakePreexistingTool) Description() string      { return "preexisting tool" }
func (t *fakePreexistingTool) Parameters() tools.Schema { return tools.Schema{} }
func (t *fakePreexistingTool) Safety() tools.Safety     { return tools.Safety{} }
func (t *fakePreexistingTool) Run(context.Context, map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusOK}
}

type fakeToolClient struct {
	listed []RemoteTool
	closed int
}

type fakeBrowserPreviewClient struct {
	calls []string
}

func (client *fakeBrowserPreviewClient) ListTools(context.Context) ([]RemoteTool, error) {
	return []RemoteTool{{Name: "browser_navigate"}, {Name: "browser_take_screenshot"}}, nil
}

func (client *fakeBrowserPreviewClient) CallTool(_ context.Context, name string, _ map[string]any) (CallToolResult, error) {
	client.calls = append(client.calls, name)
	if name == "browser_take_screenshot" {
		return CallToolResult{Content: []Content{{Type: "image", MimeType: "image/png", Data: "cHJldmlldw=="}}}, nil
	}
	return CallToolResult{Content: []Content{{Type: "text", Text: "navigated"}}}, nil
}

func (client *fakeBrowserPreviewClient) Close() error { return nil }

func TestMCPBrowserActionsAttachPreviewScreenshot(t *testing.T) {
	registry := tools.NewRegistry()
	client := &fakeBrowserPreviewClient{}
	runtime, err := RegisterTools(context.Background(), registry, config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"playwright": {Type: "stdio", Command: "playwright-mcp"},
	}}, RegisterOptions{
		ClientFactory: func(context.Context, Server) (ToolClient, error) { return client, nil },
	})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	defer runtime.Close()

	result := registry.RunWithOptions(context.Background(), "mcp_playwright_browser_navigate", map[string]any{"url": "http://localhost:1234/"}, tools.RunOptions{PermissionGranted: true})
	if result.Status != tools.StatusOK {
		t.Fatalf("navigate result = %#v", result)
	}
	if len(result.Images) != 1 || string(result.Images[0].Data) != string([]byte("preview")) {
		t.Fatalf("navigate images = %#v, want attached screenshot", result.Images)
	}
	if result.Meta["browser_preview_screenshot"] != "true" || result.Meta["screenshot_attached"] != "true" {
		t.Fatalf("navigate metadata = %#v, want preview markers", result.Meta)
	}
	if len(client.calls) != 2 || client.calls[0] != "browser_navigate" || client.calls[1] != "browser_take_screenshot" {
		t.Fatalf("MCP calls = %#v, want navigate then screenshot", client.calls)
	}
}

func (client *fakeToolClient) ListTools(context.Context) ([]RemoteTool, error) {
	return client.listed, nil
}

func (client *fakeToolClient) CallTool(_ context.Context, _ string, args map[string]any) (CallToolResult, error) {
	return CallToolResult{
		Content: []Content{{Type: "text", Text: "lookup: " + args["query"].(string)}},
	}, nil
}

func (client *fakeToolClient) Close() error {
	client.closed++
	return nil
}

type flakyToolClient struct {
	calls    int
	failures int
}

func (client *flakyToolClient) ListTools(context.Context) ([]RemoteTool, error) {
	return nil, nil
}

func (client *flakyToolClient) CallTool(context.Context, string, map[string]any) (CallToolResult, error) {
	client.calls++
	if client.calls <= client.failures {
		return CallToolResult{}, io.EOF
	}
	return CallToolResult{Content: []Content{{Type: "text", Text: "recovered"}}}, nil
}

func (client *flakyToolClient) Close() error { return nil }
