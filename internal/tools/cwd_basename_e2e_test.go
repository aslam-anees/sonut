package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sandbox"
)

// Live regression: after /cd into .../sonut/test the model calls
// list_directory with path="/test". Sandbox + tools must list the session
// directory, not die as outside_workspace and burn thinking turns.
func TestE2EListDirectoryBareTestAfterCD(t *testing.T) {
	parent := t.TempDir()
	ws := filepath.Join(parent, "test")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "marker.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	scope, err := sandbox.NewScope(ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	engine := sandbox.NewEngine(sandbox.EngineOptions{
		WorkspaceRoot: ws,
		Policy:        sandbox.DefaultPolicy(),
		Scope:         scope,
	})
	reg := NewRegistry()
	reg.Register(NewScopedListDirectoryTool(ws, scope))

	SetSessionWorkingDir(ws)
	defer SetSessionWorkingDir("")

	res := reg.RunWithOptions(context.Background(), "list_directory", map[string]any{
		"path": "/test",
	}, RunOptions{
		PermissionGranted: true,
		Cwd:               ws,
		Sandbox:           engine,
	})
	if res.Status != StatusOK {
		t.Fatalf("list_directory /test failed: %s", res.Output)
	}
	if strings.Contains(res.Output, "outside_workspace") || strings.Contains(res.Output, "Sandbox block") {
		t.Fatalf("still blocked: %s", res.Output)
	}
	if !strings.Contains(res.Output, "marker.txt") {
		t.Fatalf("missing marker in listing: %s", res.Output)
	}
}
