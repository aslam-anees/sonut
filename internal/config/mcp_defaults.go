package config

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultMCPServers returns the credential-free MCP servers @code enables by
// default. Defaults are seeded before user/project config is merged (see
// ResolveMCP), so users can override any field or disable a default with
// `at mcp disable <name>` (which writes `"disabled": true`).
//
// Do not add hosted servers here merely because tools/list works anonymously.
// A default must also execute its tools without credentials. Firecrawl's hosted
// MCP endpoint, for example, currently rejects real calls without an account or
// API key; users can still add it explicitly after configuring authentication.
func DefaultMCPServers() map[string]MCPServerConfig {
	return map[string]MCPServerConfig{
		// Playwright MCP is the default browser-automation and agent-testing
		// surface (navigate/click/type/snapshot over a real browser via the
		// accessibility tree — no vision model needed). npx fetches it on first
		// use; needs Node.js.
		//
		// ALWAYS headful (visible Chrome) — product requirement. The user must
		// be able to watch the agent, interact with the window, and handle login/
		// CAPTCHA/payment steps directly. --headless is never added.
		"playwright": {
			Type:    "stdio",
			Command: "npx",
			Args:    PlaywrightMCPArgs(),
		},
	}
}

// getPlaywrightMCPConfig is the single source of truth for Playwright MCP
// launch configuration. Headed (visible browser) mode is a hard product
// requirement — users must always be able to watch the agent and interact
// (login, CAPTCHA, OTP, payment). --headless is never added regardless of
// environment. --user-data-dir provides a persistent profile so the same
// browser window and session survive across agent turns and steps.
func getPlaywrightMCPConfig() []string {
	args := []string{"-y", "@playwright/mcp@latest"}
	// Persistent browser profile: reuses the same Chrome window across agent
	// steps so the user sees continuous automation, not repeated open/close.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		args = append(args, "--user-data-dir", filepath.Join(home, ".atcode", "playwright-profile"))
	}
	return args
}

// PlaywrightMCPArgs returns the default npx args for @playwright/mcp.
// Always headful (no --headless) — product requirement, not a preference.
func PlaywrightMCPArgs() []string {
	return getPlaywrightMCPConfig()
}

// NormalizePlaywrightServerArgs strips --headless from any playwright server
// config and is the regression guard for the always-headed product requirement.
// It handles the legacy default (`… @playwright/mcp@latest --headless`) as
// well as any user-supplied args that include --headless. Other custom args
// are left unchanged so user overrides (--isolated, --browser, etc.) survive.
func NormalizePlaywrightServerArgs(server *MCPServerConfig) {
	if server == nil || server.Disabled {
		return
	}
	// Strip --headless wherever it appears. Headed mode is a product requirement:
	// the user must always see the browser window and be able to interact with it.
	var filtered []string
	for _, arg := range server.Args {
		if arg != "--headless" {
			filtered = append(filtered, arg)
		}
	}
	server.Args = filtered
}

// IsDefaultMCPServer reports whether name is one of @code's built-in default MCP
// servers. The config commands use it so a default can be disabled/enabled even
// though it is not written to the user's config file until overridden.
func IsDefaultMCPServer(name string) bool {
	_, ok := DefaultMCPServers()[strings.TrimSpace(name)]
	return ok
}
