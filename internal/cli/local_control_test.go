package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/tools"
)

func TestCoreRegistryIncludesDefaultLocalControlTools(t *testing.T) {
	// Browser tools are served by the Playwright MCP server; terminal/desktop
	// tools are only advertised when their automation helper is installed, so a
	// bare registry always gets the artifact tools but not necessarily the rest.
	registry := newCoreRegistry(t.TempDir())
	tool, ok := registry.Get("capture_artifact")
	if !ok {
		t.Fatal("capture_artifact not registered")
	}
	if tool.Safety().Permission != tools.PermissionPrompt {
		t.Fatalf("capture_artifact permission = %s, want prompt", tool.Safety().Permission)
	}
}

func TestCoreRegistryHonorsExplicitLocalControlDisable(t *testing.T) {
	// Browser tools are now served by the Playwright MCP server rather than
	// registered directly; only terminal, desktop, and artifact tools remain.
	registry := newCoreRegistry(t.TempDir())
	var cfg config.LocalControlConfig
	if err := json.Unmarshal([]byte(`{"enabled":false}`), &cfg); err != nil {
		t.Fatalf("unmarshal local control config: %v", err)
	}
	registerLocalControlTools(registry, t.TempDir(), cfg)
	// desktop_action / terminal_session are absent entirely when the helper is
	// missing; when present anyway (helper installed on the host) they must deny.
	for _, name := range []string{"desktop_action", "terminal_session"} {
		if tool, ok := registry.Get(name); ok && tool.Safety().Permission != tools.PermissionDeny {
			t.Fatalf("%s permission = %s, want deny", name, tool.Safety().Permission)
		}
	}
	tool, ok := registry.Get("capture_artifact")
	if !ok {
		t.Fatal("capture_artifact not registered")
	}
	if tool.Safety().Permission != tools.PermissionDeny {
		t.Fatalf("capture_artifact permission = %s, want deny", tool.Safety().Permission)
	}
}

func TestRunExecListToolsHonorsDisabledLocalControlConfig(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cfg := execResolvedConfig()
	var localControl config.LocalControlConfig
	if err := json.Unmarshal([]byte(`{"enabled":false}`), &localControl); err != nil {
		t.Fatalf("unmarshal local control config: %v", err)
	}
	cfg.LocalControl = localControl

	exitCode := runWithDeps([]string{"exec", "--list-tools", "-o", "json"}, &stdout, &stderr, appDeps{
		getwd: func() (string, error) {
			return t.TempDir(), nil
		},
		resolveConfig: func(string, config.Overrides) (config.ResolvedConfig, error) {
			return cfg, nil
		},
	})
	if exitCode != exitSuccess {
		t.Fatalf("exitCode = %d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var payload struct {
		Tools []struct {
			Name       string `json:"name"`
			Permission string `json:"permission"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &payload); err != nil {
		t.Fatalf("parse tools JSON: %v\n%s", err, stdout.String())
	}
	for _, tool := range payload.Tools {
		if tool.Name == "browser_open" {
			t.Fatalf("browser_open should be hidden when local control is disabled, got permission %s", tool.Permission)
		}
	}
}

func TestRegisterLocalControlToolsAppliesDefaults(t *testing.T) {
	// Browser tools are now served by the Playwright MCP server rather than
	// registered directly; only terminal, desktop, and artifact tools remain.
	workspaceRoot := t.TempDir()
	// Terminal/desktop tools are only advertised when their helper resolves —
	// point both at a real file so the registration path is exercised.
	helperPath := filepath.Join(workspaceRoot, "fake-helper")
	if err := os.WriteFile(helperPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.LocalControlConfig{Enabled: true}
	cfg.Terminal.HelperPath = helperPath
	cfg.Desktop.HelperPath = helperPath
	registry := newCoreRegistry(workspaceRoot)
	registerLocalControlTools(registry, workspaceRoot, cfg)
	for _, name := range []string{"terminal_session", "capture_artifact"} {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if tool.Safety().Permission != tools.PermissionPrompt {
			t.Fatalf("%s permission = %s, want prompt", name, tool.Safety().Permission)
		}
	}
	desktop, ok := registry.Get("desktop_action")
	if !ok {
		t.Fatal("desktop_action not registered")
	}
	if desktop.Safety().Permission != tools.PermissionDeny {
		t.Fatalf("desktop_action permission = %s, want deny without nested desktop opt-in", desktop.Safety().Permission)
	}
}
