package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

func TestExistingFileReferenceDetectsAtPath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "button.html"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !referencesExistingWorkspaceFile(root, "nothing happens in @button.html", nil) {
		t.Fatal("expected @button.html to identify an existing workspace file")
	}
	if referencesExistingWorkspaceFile(root, "create a new rain.html file", nil) {
		t.Fatal("a missing file must not force surgical existing-file mode")
	}
}

func TestExistingRepairHidesWholeWritersButHonorsCompletedWrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "button.html")
	if err := os.WriteFile(path, []byte("<html><body>old</body></html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	replacement := "<!DOCTYPE html>\n<html><body><button id=\"b\">ok</button></body></html>\n"
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		{
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "write-1", ToolName: "write_file"},
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "write-1", ArgumentsFragment: `{"path":"button.html","content":` + quoteJSONString(replacement) + `,"overwrite":true}`},
			{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "write-1"},
			{Type: sonutruntime.StreamEventDone},
		},
		{
			{Type: sonutruntime.StreamEventText, Content: "done"},
			{Type: sonutruntime.StreamEventDone},
		},
	}}
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreTools(root) {
		registry.Register(tool)
	}
	result, err := Run(context.Background(), "fix the broken click handler in @button.html", provider, Options{
		Registry:        registry,
		Cwd:             root,
		RunMode:         RunModeBuilder,
		UnifiedWorkflow: true,
		PermissionMode:  PermissionModeUnsafe,
		FileTracker:     tools.NewFileTracker(),
		MaxTurns:        2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("final answer = %q, want done", result.FinalAnswer)
	}
	if len(provider.requests) == 0 {
		t.Fatal("expected a provider request")
	}
	for _, definition := range provider.requests[0].Tools {
		if definition.Name == "write_file" || definition.Name == "create_file" || definition.Name == "overwrite_file" {
			t.Fatalf("existing repair advertised whole-file writer %q", definition.Name)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != replacement {
		t.Fatalf("completed hidden write was discarded: %q", got)
	}
}
