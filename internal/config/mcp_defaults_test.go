package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestIsDefaultMCPServer(t *testing.T) {
	if !IsDefaultMCPServer("playwright") {
		t.Fatal("playwright should be a built-in default")
	}
	if IsDefaultMCPServer("  playwright  ") == false {
		t.Fatal("IsDefaultMCPServer should trim whitespace")
	}
	if IsDefaultMCPServer("firecrawl") {
		t.Fatal("credential-gated firecrawl must not be enabled by default")
	}
	if IsDefaultMCPServer("not-a-default") {
		t.Fatal("unknown server should not be a default")
	}
}

func TestPlaywrightMCPArgsAlwaysHeadful(t *testing.T) {
	// Headed mode is a product requirement — no environment override can
	// add --headless. Verify that even SSH/CI/no-display envs produce headful args.
	t.Setenv("SONUT_BROWSER_HEADLESS", "1")
	t.Setenv("CI", "1")
	t.Setenv("SSH_TTY", "/dev/pts/0")
	t.Setenv("SSH_CONNECTION", "1.2.3.4 12345 5.6.7.8 22")

	args := PlaywrightMCPArgs()
	if slices.Contains(args, "--headless") {
		t.Fatalf("PlaywrightMCPArgs must never include --headless regardless of env, got %v", args)
	}
	if !slices.Contains(args, "--user-data-dir") {
		t.Fatalf("PlaywrightMCPArgs must include --user-data-dir for persistent profile, got %v", args)
	}
}

func TestNormalizePlaywrightServerArgsStripsHeadless(t *testing.T) {
	// Legacy default: migrate from --headless to headful.
	server := MCPServerConfig{
		Type:    "stdio",
		Command: "npx",
		Args:    []string{"-y", "@playwright/mcp@latest", "--headless"},
	}
	NormalizePlaywrightServerArgs(&server)
	if slices.Contains(server.Args, "--headless") {
		t.Fatalf("NormalizePlaywrightServerArgs must strip --headless from legacy args, got %v", server.Args)
	}

	// Custom args: --headless stripped (product requirement), other args preserved.
	custom := MCPServerConfig{Args: []string{"-y", "@playwright/mcp@latest", "--headless", "--isolated"}}
	NormalizePlaywrightServerArgs(&custom)
	if slices.Contains(custom.Args, "--headless") {
		t.Fatalf("--headless must be stripped even from custom args, got %v", custom.Args)
	}
	if !slices.Contains(custom.Args, "--isolated") {
		t.Fatalf("other custom args must be preserved, got %v", custom.Args)
	}
}

func TestResolveMCPSeedsEnabledPlaywrightDefault(t *testing.T) {
	cfg, err := ResolveMCP(ResolveOptions{})
	if err != nil {
		t.Fatalf("ResolveMCP: %v", err)
	}
	playwright, ok := cfg.Servers["playwright"]
	if !ok {
		t.Fatal("expected the playwright default to be seeded with no user config")
	}
	if playwright.Type != "stdio" || playwright.Command != "npx" {
		t.Fatalf("unexpected playwright default: %#v", playwright)
	}
	if playwright.Disabled {
		t.Fatal("the playwright default must be enabled out of the box")
	}
}

func TestResolveMCPUserCanDisableDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"mcp":{"servers":{"playwright":{"disabled":true}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveMCP(ResolveOptions{UserConfigPath: path})
	if err != nil {
		t.Fatalf("ResolveMCP: %v", err)
	}
	if !cfg.Servers["playwright"].Disabled {
		t.Fatal("a user must be able to disable the default by writing over it")
	}
}

func TestResolveMCPUserCanOverrideDefaultArgsKeepingOtherFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"mcp":{"servers":{"playwright":{"args":["-y","@playwright/mcp@latest","--isolated"]}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveMCP(ResolveOptions{UserConfigPath: path})
	if err != nil {
		t.Fatalf("ResolveMCP: %v", err)
	}
	playwright := cfg.Servers["playwright"]
	if !slices.Contains(playwright.Args, "--isolated") {
		t.Fatalf("user override of the default args did not apply: %#v", playwright)
	}
	if playwright.Type != "stdio" || playwright.Command != "npx" {
		t.Fatalf("override should keep the default's other fields, got %#v", playwright)
	}
}
