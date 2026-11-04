package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileSuggestsJoinedAssetPath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "features bg.svg"), []byte("<svg/>"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := NewReadFileTool(root).Run(context.Background(), map[string]any{
		"path": "assets/features/bg.svg",
	})
	if result.Status != StatusError {
		t.Fatalf("status = %q, want error", result.Status)
	}
	if !strings.Contains(result.Output, "Did you mean `assets/features bg.svg`?") {
		t.Fatalf("missing actionable path correction: %q", result.Output)
	}
}

func TestPathSuggestionFindsDroppedFolderByBasename(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "pages", "benchmark.html")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := pathNotFoundSuggestion(root, "benchmark.html"); got != "pages/benchmark.html" {
		t.Fatalf("suggestion = %q, want pages/benchmark.html", got)
	}
}
