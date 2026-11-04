package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/aistatine/sonut/internal/config"
)

// `at mcp disable playwright` must work even though playwright is a built-in
// default that is not written to the user's config file until overridden.
func TestRunMCPDisableSeededPlaywrightDefault(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "atcode", "config.json")
	// A config with no playwright entry — the default lives in code, not the file.
	writeMCPCommandRawConfig(t, configPath, `{"activeProvider":"fast"}`)

	var out, errBuf bytes.Buffer
	code := runWithDeps([]string{"mcp", "disable", "playwright", "--json"}, &out, &errBuf, appDeps{
		userConfigPath: func() (string, error) { return configPath, nil },
	})
	if code != exitSuccess {
		t.Fatalf("disable exit=%d stderr=%s", code, errBuf.String())
	}
	var payload struct {
		ServerName string `json:"serverName"`
		Disabled   bool   `json:"disabled"`
		Changed    bool   `json:"changed"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("decode disable JSON: %v\n%s", err, out.String())
	}
	if payload.ServerName != "playwright" || !payload.Disabled || !payload.Changed {
		t.Fatalf("disable payload = %#v, want playwright disabled+changed", payload)
	}

	// End-to-end: resolving that config now turns the default off.
	cfg, err := config.ResolveMCP(config.ResolveOptions{UserConfigPath: configPath})
	if err != nil {
		t.Fatalf("ResolveMCP: %v", err)
	}
	if !cfg.Servers["playwright"].Disabled {
		t.Fatal("expected `mcp disable playwright` to turn the seeded default off in the resolved config")
	}
}
